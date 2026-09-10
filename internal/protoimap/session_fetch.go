package protoimap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// expandSet converts an imap.NumSet into a list of 1-based sequence indices
// into the current sel.msgs slice (for SeqSet) or into UIDs (for UIDSet).
// For UIDSet we return the sequence indices of matching messages.
func (ses *session) expandSet(ns imap.NumSet, byUID bool) []int {
	ses.selMu.Lock()
	defer ses.selMu.Unlock()
	msgs := ses.sel.msgs
	out := []int{}
	seen := map[int]bool{}
	if byUID {
		us, ok := ns.(imap.UIDSet)
		if !ok {
			return nil
		}
		for _, r := range us {
			lo, hi := r.Start, r.Stop
			if uint32(hi) == 0xFFFFFFFF {
				hi = imap.UID(ses.sel.uidNext - 1)
			}
			for i, m := range msgs {
				if imap.UID(m.UID) >= lo && imap.UID(m.UID) <= hi {
					if !seen[i+1] {
						seen[i+1] = true
						out = append(out, i+1)
					}
				}
			}
		}
		return out
	}
	ss, ok := ns.(imap.SeqSet)
	if !ok {
		return nil
	}
	for _, r := range ss {
		lo, hi := r.Start, r.Stop
		if hi == 0xFFFFFFFF {
			hi = uint32(len(msgs))
		}
		if lo == 0 {
			lo = 1
		}
		for i := lo; i <= hi; i++ {
			if int(i) <= len(msgs) && !seen[int(i)] {
				seen[int(i)] = true
				out = append(out, int(i))
			}
		}
	}
	return out
}

func (ses *session) handleFETCH(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	// CONDSTORE: any FETCH that asks for MODSEQ or carries CHANGEDSINCE
	// implicitly promotes the session into CONDSTORE mode (RFC 7162
	// §3.1.1). Always emit MODSEQ in the resulting FETCH responses
	// once promoted so the client's caches stay coherent.
	if c.FetchOptions != nil && (c.FetchOptions.ModSeq || c.FetchOptions.ChangedSince != 0) {
		ses.enableCondstore()
		c.FetchOptions.ModSeq = true
	}
	seqs := ses.expandSet(c.FetchSet, c.IsUID)
	changedSince := store.ModSeq(0)
	if c.FetchOptions != nil {
		changedSince = store.ModSeq(c.FetchOptions.ChangedSince)
	}
	for _, seq := range seqs {
		if changedSince > 0 {
			ses.selMu.Lock()
			if seq <= 0 || seq > len(ses.sel.msgs) {
				ses.selMu.Unlock()
				continue
			}
			m := ses.sel.msgs[seq-1]
			ses.selMu.Unlock()
			if m.ModSeq <= changedSince {
				continue
			}
		}
		if err := ses.emitFetch(ctx, seq, c.FetchOptions, c.IsUID); err != nil {
			return ses.resp.taggedNO(c.Tag, "", fmt.Sprintf("fetch: %v", err))
		}
	}
	return ses.resp.taggedOK(c.Tag, "", c.Op+" completed")
}

// emitFetch writes a single "* seq FETCH (...)" response.
func (ses *session) emitFetch(ctx context.Context, seq int, opts *imap.FetchOptions, uidCmd bool) error {
	ses.selMu.Lock()
	if seq <= 0 || seq > len(ses.sel.msgs) {
		ses.selMu.Unlock()
		return nil
	}
	m := ses.sel.msgs[seq-1]
	ses.selMu.Unlock()

	// For UID FETCH, UID is implicitly included (RFC 9051 §6.4.7).
	if uidCmd {
		opts.UID = true
	}

	// Assemble the inline (non-literal) parts into a string builder; body
	// section literals are emitted inline during the final write.
	parts := []string{}

	if opts.UID {
		parts = append(parts, fmt.Sprintf("UID %d", m.UID))
	}
	if opts.Flags {
		parts = append(parts, "FLAGS "+flagListString(flagNamesFromMask(m.Flags, m.Keywords)))
	}
	if opts.InternalDate {
		parts = append(parts, "INTERNALDATE "+formatInternalDate(ses.ensureMsgTime(m.InternalDate)))
	}
	if opts.RFC822Size {
		parts = append(parts, fmt.Sprintf("RFC822.SIZE %d", m.Size))
	}
	if opts.Envelope {
		env := convertEnvelope(m.Envelope)
		parts = append(parts, "ENVELOPE "+formatEnvelope(env))
	}
	if opts.ModSeq {
		parts = append(parts, fmt.Sprintf("MODSEQ (%d)", m.ModSeq))
	}

	// Body sections require pulling the blob.
	var bodyChunks []struct {
		header string
		data   []byte
	}
	var bodyStructure string
	if opts.BodyStructure != nil || len(opts.BodySection) > 0 {
		raw, err := ses.fetchBlob(ctx, m)
		if err != nil {
			return err
		}
		for _, sec := range opts.BodySection {
			data := extractSection(raw, sec)
			header := formatBodySectionHeader(sec)
			if sec.Partial != nil {
				off := sec.Partial.Offset
				sz := sec.Partial.Size
				if off >= int64(len(data)) {
					data = nil
				} else {
					end := int64(len(data))
					if sz > 0 && off+sz < end {
						end = off + sz
					}
					data = data[off:end]
				}
				header += fmt.Sprintf("<%d>", off)
			}
			bodyChunks = append(bodyChunks, struct {
				header string
				data   []byte
			}{header, data})
		}
		if opts.BodyStructure != nil {
			bodyStructure = formatBodyStructure(raw, opts.BodyStructure.Extended)
		}
	}
	if bodyStructure != "" {
		parts = append(parts, bodyStructure)
	}

	// Rate limit across the total bytes we are about to emit.
	totalBytes := int64(0)
	for _, c := range bodyChunks {
		totalBytes += int64(len(c.data))
	}
	if ses.bucket != nil && totalBytes > 0 {
		if err := ses.bucket.consume(ctx, totalBytes); err != nil {
			return err
		}
	}
	if totalBytes > 0 {
		observe.IMAPFetchBytesTotal.Add(float64(totalBytes))
	}

	// Trace-level response log (REQ-OPS-82, issue #320): one line for this
	// untagged FETCH response, with any literal body section truncated to
	// responseTraceLiteralMax bytes and its full length noted. Gated on
	// traceEnabled() so a session without protoimap at trace level never
	// pays the string-building cost — writeRaw itself carries no framing
	// to hook generically the way writeLine does, so the FETCH response
	// (the package's only writeRaw caller) traces itself explicitly here.
	if ses.resp.traceEnabled() {
		var tsb strings.Builder
		fmt.Fprintf(&tsb, "* %d FETCH (", seq)
		tsb.WriteString(strings.Join(parts, " "))
		for _, bc := range bodyChunks {
			if tsb.Len() > 0 && tsb.String()[tsb.Len()-1] != '(' {
				tsb.WriteByte(' ')
			}
			tsb.WriteString(bc.header)
			tsb.WriteByte(' ')
			tsb.WriteString(traceLiteral(bc.data))
		}
		tsb.WriteString(")")
		ses.resp.traceLine("untagged", tsb.String())
	}

	// Build the final line: "* seq FETCH (part1 part2 ... bodySection[...] {N}\r\n<bytes>)"
	var sb strings.Builder
	fmt.Fprintf(&sb, "* %d FETCH (", seq)
	sb.WriteString(strings.Join(parts, " "))
	for _, bc := range bodyChunks {
		if sb.Len() > 0 && sb.String()[sb.Len()-1] != '(' {
			sb.WriteByte(' ')
		}
		sb.WriteString(bc.header)
		sb.WriteByte(' ')
		// Emit synchronising literal.
		fmt.Fprintf(&sb, "{%d}\r\n", len(bc.data))
		// Flush partial line, emit literal, then continue.
		if err := ses.resp.writeRaw([]byte(sb.String())); err != nil {
			return err
		}
		if err := ses.resp.writeRaw(bc.data); err != nil {
			return err
		}
		sb.Reset()
	}
	sb.WriteString(")\r\n")
	return ses.resp.writeRaw([]byte(sb.String()))
}

func (ses *session) fetchBlob(ctx context.Context, m store.Message) ([]byte, error) {
	rc, err := ses.s.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return nil, err
	}
	// REQ-FLOW-34: prepend the synthetic X-Herold-Recipient header
	// at render time when the per-mailbox row carries an envelope
	// received_to. The header is NOT persisted in the blob (which
	// stays content-addressed and dedup-friendly across fan-out
	// recipients per REQ-FLOW-30). A legacy / non-fan-out membership
	// has ReceivedTo == "" and InjectXHeroldRecipient returns the
	// raw bytes unchanged.
	return mailparse.InjectXHeroldRecipient(buf.Bytes(), m.ReceivedTo), nil
}

// extractSection returns the requested section of the raw message body.
// With no part number (sec.Part empty) it addresses the outer RFC 5322
// message: full body, HEADER, TEXT, HEADER.FIELDS / HEADER.FIELDS.NOT --
// unchanged regardless of whether the message is multipart. With a part
// number it resolves the numbered MIME part against the parsed tree
// (resolveSection) and returns that part's raw (CTE-encoded, undecoded)
// bytes: the whole part for a bare BODY[n], the part's own MIME header for
// BODY[n.MIME], and -- only valid when part n is message/rfc822 -- the
// encapsulated message's header or text for BODY[n.HEADER] / BODY[n.TEXT].
func extractSection(raw []byte, sec *imap.FetchItemBodySection) []byte {
	if sec == nil || (sec.Specifier == imap.PartSpecifierNone && len(sec.Part) == 0 && len(sec.HeaderFields) == 0 && len(sec.HeaderFieldsNot) == 0) {
		return raw
	}
	if len(sec.Part) == 0 {
		hdr, body := splitRawMessage(raw)
		switch sec.Specifier {
		case imap.PartSpecifierHeader:
			if len(sec.HeaderFields) > 0 || len(sec.HeaderFieldsNot) > 0 {
				return filterHeaders(hdr, sec.HeaderFields, sec.HeaderFieldsNot)
			}
			return hdr
		case imap.PartSpecifierText:
			return body
		case imap.PartSpecifierMIME:
			return hdr
		case imap.PartSpecifierNone:
			return raw
		}
		return raw
	}

	msg, perr := mailparse.Parse(bytes.NewReader(raw), mailparse.NewLenientParseOptions())
	if perr != nil && msg.Body.ContentType == "" {
		return nil
	}
	rp, rpRaw, ok := resolveSection(raw, msg.Body, sec.Part)
	if !ok {
		return nil
	}
	switch sec.Specifier {
	case imap.PartSpecifierMIME:
		return readAllOrNil(rp.RawHeader(bytes.NewReader(rpRaw)))
	case imap.PartSpecifierHeader, imap.PartSpecifierText:
		if !strings.EqualFold(rp.ContentType, "message/rfc822") {
			return nil
		}
		nestedRaw := readAllOrNil(rp.RawBody(bytes.NewReader(rpRaw)))
		hdr, body := splitRawMessage(nestedRaw)
		if sec.Specifier == imap.PartSpecifierText {
			return body
		}
		if len(sec.HeaderFields) > 0 || len(sec.HeaderFieldsNot) > 0 {
			return filterHeaders(hdr, sec.HeaderFields, sec.HeaderFieldsNot)
		}
		return hdr
	case imap.PartSpecifierNone:
		if len(rp.Children) > 0 {
			// A bare BODY[n] on a multipart container has no well-defined
			// wire form (RFC 3501 addresses multipart subparts via
			// n.1, n.2, ...); return nothing rather than guess.
			return nil
		}
		return readAllOrNil(rp.RawBody(bytes.NewReader(rpRaw)))
	}
	return nil
}

// resolveSection walks path (a FETCH section's dotted part number, e.g.
// [2, 1] for "2.1") against root, the parsed MIME tree rooted at raw.
// Numbering follows RFC 9051 sec 7.5.2: multipart children are numbered
// 1..N; a non-multipart part (leaf or message/rfc822) with no children
// yet consumed only answers to "1" as its own address; a message/rfc822
// part's own nested numbering restarts at 1 inside the encapsulated
// message, so remaining path segments are resolved against a fresh parse
// of that part's raw bytes. Returns the resolved part together with the
// raw byte slice its internal offsets are relative to.
func resolveSection(raw []byte, root mailparse.Part, path []int) (mailparse.Part, []byte, bool) {
	cur := root
	curRaw := raw
	for i, seg := range path {
		if seg < 1 {
			return mailparse.Part{}, nil, false
		}
		if len(cur.Children) > 0 {
			if seg > len(cur.Children) {
				return mailparse.Part{}, nil, false
			}
			cur = cur.Children[seg-1]
			continue
		}
		if strings.EqualFold(cur.ContentType, "message/rfc822") {
			nestedRaw := readAllOrNil(cur.RawBody(bytes.NewReader(curRaw)))
			if nestedRaw == nil {
				return mailparse.Part{}, nil, false
			}
			nestedMsg, nerr := mailparse.Parse(bytes.NewReader(nestedRaw), mailparse.NewLenientParseOptions())
			if nerr != nil && nestedMsg.Body.ContentType == "" {
				return mailparse.Part{}, nil, false
			}
			return resolveSection(nestedRaw, nestedMsg.Body, path[i:])
		}
		if i == 0 && seg == 1 {
			continue
		}
		return mailparse.Part{}, nil, false
	}
	return cur, curRaw, true
}

// readAllOrNil drains r, returning nil on any read error (including a nil
// reader from a failed RawBody/RawHeader call).
func readAllOrNil(r io.Reader, err error) []byte {
	if err != nil || r == nil {
		return nil
	}
	b, rerr := io.ReadAll(r)
	if rerr != nil {
		return nil
	}
	return b
}

func splitRawMessage(raw []byte) (header, body []byte) {
	// Header ends at CRLFCRLF (or LFLF for non-canonical inputs).
	if idx := bytes.Index(raw, []byte("\r\n\r\n")); idx >= 0 {
		return raw[:idx+4], raw[idx+4:]
	}
	if idx := bytes.Index(raw, []byte("\n\n")); idx >= 0 {
		return raw[:idx+2], raw[idx+2:]
	}
	return raw, nil
}

// filterHeaders returns only the header lines whose name is in keep (or
// not in notKeep when keep is empty and notKeep is set).
func filterHeaders(hdr []byte, keep, notKeep []string) []byte {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[strings.ToLower(k)] = true
	}
	skipSet := map[string]bool{}
	for _, k := range notKeep {
		skipSet[strings.ToLower(k)] = true
	}
	var buf bytes.Buffer
	lines := splitHeaderFields(hdr)
	for _, ln := range lines {
		name := headerName(ln)
		l := strings.ToLower(name)
		if len(keepSet) > 0 && !keepSet[l] {
			continue
		}
		if len(skipSet) > 0 && skipSet[l] {
			continue
		}
		buf.Write(ln)
	}
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// splitHeaderFields segments a raw header block into one slice per field,
// including the trailing CRLF; continuation lines are folded into the
// preceding field.
func splitHeaderFields(hdr []byte) [][]byte {
	var fields [][]byte
	var current []byte
	for len(hdr) > 0 {
		nl := bytes.IndexByte(hdr, '\n')
		if nl < 0 {
			current = append(current, hdr...)
			break
		}
		line := hdr[:nl+1]
		hdr = hdr[nl+1:]
		if len(line) == 0 {
			continue
		}
		// Continuation?
		if len(current) > 0 && (line[0] == ' ' || line[0] == '\t') {
			current = append(current, line...)
			continue
		}
		if len(current) > 0 {
			fields = append(fields, current)
		}
		current = append([]byte(nil), line...)
	}
	if len(current) > 0 {
		fields = append(fields, current)
	}
	return fields
}

func headerName(field []byte) string {
	colon := bytes.IndexByte(field, ':')
	if colon < 0 {
		return ""
	}
	return strings.TrimSpace(string(field[:colon]))
}

// formatBodySectionHeader renders "BODY[..]" or "BODY.PEEK[..]" — but we
// always return the non-PEEK form because FETCH response syntax drops the
// .PEEK suffix (it is a request modifier).
func formatBodySectionHeader(sec *imap.FetchItemBodySection) string {
	var sb strings.Builder
	sb.WriteString("BODY[")
	if len(sec.Part) > 0 {
		parts := make([]string, len(sec.Part))
		for i, p := range sec.Part {
			parts[i] = fmt.Sprintf("%d", p)
		}
		sb.WriteString(strings.Join(parts, "."))
		if sec.Specifier != imap.PartSpecifierNone {
			sb.WriteByte('.')
		}
	}
	switch sec.Specifier {
	case imap.PartSpecifierHeader:
		if len(sec.HeaderFields) > 0 {
			sb.WriteString("HEADER.FIELDS (")
			for i, f := range sec.HeaderFields {
				if i > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(f)
			}
			sb.WriteByte(')')
		} else if len(sec.HeaderFieldsNot) > 0 {
			sb.WriteString("HEADER.FIELDS.NOT (")
			for i, f := range sec.HeaderFieldsNot {
				if i > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(f)
			}
			sb.WriteByte(')')
		} else {
			sb.WriteString("HEADER")
		}
	case imap.PartSpecifierText:
		sb.WriteString("TEXT")
	case imap.PartSpecifierMIME:
		sb.WriteString("MIME")
	}
	sb.WriteByte(']')
	return sb.String()
}

// formatBodyStructure renders the RFC 9051 sec 7.5.2 BODY / BODYSTRUCTURE
// response for a message by walking its parsed MIME tree (mailparse.Message)
// rather than emitting a single-part structure for every message: a
// multipart message gets the nested "(child child ... subtype)" form so
// clients that navigate FETCH by BODYSTRUCTURE (e.g. the Gmail Android app)
// can find and fetch the text part (re #321).
func formatBodyStructure(raw []byte, extended bool) string {
	label := "BODY"
	if extended {
		label = "BODYSTRUCTURE"
	}
	msg, perr := mailparse.Parse(bytes.NewReader(raw), mailparse.NewLenientParseOptions())
	if perr != nil && msg.Body.ContentType == "" {
		// The blob could not be parsed at all (corrupt/hostile stored
		// bytes): fall back to a minimal single-part placeholder so FETCH
		// still returns a syntactically valid, if uninformative, structure
		// instead of silently omitting it.
		return fmt.Sprintf(`%s ("TEXT" "PLAIN" ("CHARSET" "us-ascii") NIL NIL "7BIT" %d 0)`, label, len(raw))
	}
	return label + " " + renderBodyStructure(msg.Body, raw, extended)
}

// renderBodyStructure renders one node of the MIME tree in RFC 9051
// sec 7.5.2 form, recursing into children for multipart containers.
func renderBodyStructure(p mailparse.Part, raw []byte, extended bool) string {
	if len(p.Children) > 0 {
		return renderMultipartStructure(p, raw, extended)
	}
	return renderSinglePartStructure(p, raw, extended)
}

// renderMultipartStructure renders a multipart/* container: the base form
// is its children's structures followed by the subtype; the parameter
// list, disposition, language, and location are BODYSTRUCTURE-only
// extension data (RFC 9051 sec 7.5.2, "Extension data (multipart)").
func renderMultipartStructure(p mailparse.Part, raw []byte, extended bool) string {
	var sb strings.Builder
	sb.WriteByte('(')
	for _, c := range p.Children {
		sb.WriteString(renderBodyStructure(c, raw, extended))
	}
	_, subtype := splitTypeSubtype(p.ContentType)
	sb.WriteByte(' ')
	sb.WriteString(imapQuote(strings.ToUpper(subtype)))
	if extended {
		_, params := splitMediaType(p.Headers.Get("Content-Type"))
		fmt.Fprintf(&sb, " %s %s NIL NIL", formatMimeParams(params), formatDisposition(p))
	}
	sb.WriteByte(')')
	return sb.String()
}

// renderSinglePartStructure renders a non-multipart part: type, subtype,
// parameter list, id, description, encoding, and size are the base form
// for every leaf; text/* parts append a line count and message/rfc822
// parts append the encapsulated message's envelope, body structure, and
// line count (RFC 9051 sec 7.5.2). BODYSTRUCTURE additionally appends the
// extension data (MD5 NIL, disposition, language NIL, location NIL).
func renderSinglePartStructure(p mailparse.Part, raw []byte, extended bool) string {
	mt, params := splitMediaType(p.Headers.Get("Content-Type"))
	if mt == "" {
		mt = p.ContentType
		if strings.EqualFold(mt, "text/plain") {
			params = map[string]string{"charset": "us-ascii"}
		}
	}
	typ, subtype := splitTypeSubtype(mt)
	if typ == "" {
		typ, subtype = "text", "plain"
	}

	encoding := strings.ToUpper(strings.TrimSpace(p.ContentTransferEncoding))
	if encoding == "" {
		encoding = "7BIT"
	}

	rawBody := readAllOrNil(p.RawBody(bytes.NewReader(raw)))

	var sb strings.Builder
	sb.WriteByte('(')
	sb.WriteString(imapQuote(strings.ToUpper(typ)))
	sb.WriteByte(' ')
	sb.WriteString(imapQuote(strings.ToUpper(subtype)))
	sb.WriteByte(' ')
	sb.WriteString(formatMimeParams(params))
	sb.WriteByte(' ')
	sb.WriteString(imapNString(p.Headers.Get("Content-Id")))
	sb.WriteByte(' ')
	sb.WriteString(imapNString(p.Headers.Get("Content-Description")))
	sb.WriteByte(' ')
	sb.WriteString(imapQuote(encoding))
	fmt.Fprintf(&sb, " %d", len(rawBody))

	lowerMT := strings.ToLower(mt)
	switch {
	case strings.HasPrefix(lowerMT, "text/"):
		fmt.Fprintf(&sb, " %d", bytes.Count(rawBody, []byte("\n")))
	case lowerMT == "message/rfc822":
		nestedMsg, nestedRaw, ok := parseNestedMessage(p, raw)
		sb.WriteByte(' ')
		if ok {
			sb.WriteString(formatEnvelope(convertMailparseEnvelope(nestedMsg.Envelope)))
			sb.WriteByte(' ')
			sb.WriteString(renderBodyStructure(nestedMsg.Body, nestedRaw, extended))
			fmt.Fprintf(&sb, " %d", bytes.Count(nestedRaw, []byte("\n")))
		} else {
			sb.WriteString(`NIL ("TEXT" "PLAIN" NIL NIL NIL "7BIT" 0 0) 0`)
		}
	}

	if extended {
		fmt.Fprintf(&sb, " NIL %s NIL NIL", formatDisposition(p))
	}
	sb.WriteByte(')')
	return sb.String()
}

// formatDisposition renders a part's Content-Disposition as the RFC 9051
// "body disposition" extension datum: NIL when the header is absent,
// otherwise a (type (attr val ...)) pair.
func formatDisposition(p mailparse.Part) string {
	raw := p.Headers.Get("Content-Disposition")
	if raw == "" {
		return "NIL"
	}
	dtype, dparams := splitMediaType(raw)
	if dtype == "" {
		return "NIL"
	}
	return "(" + imapQuote(strings.ToUpper(dtype)) + " " + formatMimeParams(dparams) + ")"
}

// parseNestedMessage re-parses the raw bytes of a message/rfc822 leaf part
// (mailparse does not itself recurse into encapsulated messages) so its
// envelope and body structure can be rendered. Returns the nested Message,
// the raw bytes it was parsed from (which its own Part offsets are
// relative to), and false if the leaf's raw range cannot be read or
// parsed at all.
func parseNestedMessage(p mailparse.Part, raw []byte) (mailparse.Message, []byte, bool) {
	nestedRaw := readAllOrNil(p.RawBody(bytes.NewReader(raw)))
	if nestedRaw == nil {
		return mailparse.Message{}, nil, false
	}
	nestedMsg, nerr := mailparse.Parse(bytes.NewReader(nestedRaw), mailparse.NewLenientParseOptions())
	if nerr != nil && nestedMsg.Body.ContentType == "" {
		return mailparse.Message{}, nil, false
	}
	return nestedMsg, nestedRaw, true
}

// convertMailparseEnvelope builds an imap.Envelope from a freshly parsed
// mailparse.Envelope (used for the envelope embedded in a message/rfc822
// BODYSTRUCTURE entry). convertEnvelope in session_mailbox.go performs the
// equivalent conversion from the store's persisted store.Envelope for the
// top-level ENVELOPE fetch item.
func convertMailparseEnvelope(e mailparse.Envelope) imap.Envelope {
	var sender []imap.Address
	if e.Sender != nil {
		sender = convertMailAddrs([]mail.Address{*e.Sender})
	}
	return imap.Envelope{
		Date:      parseEnvelopeDate(e.Date),
		Subject:   e.Subject,
		From:      convertMailAddrs(e.From),
		Sender:    sender,
		ReplyTo:   convertMailAddrs(e.ReplyTo),
		To:        convertMailAddrs(e.To),
		Cc:        convertMailAddrs(e.Cc),
		Bcc:       convertMailAddrs(e.Bcc),
		InReplyTo: e.InReplyTo,
		MessageID: e.MessageID,
	}
}

func convertMailAddrs(addrs []mail.Address) []imap.Address {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]imap.Address, 0, len(addrs))
	for _, a := range addrs {
		at := strings.LastIndexByte(a.Address, '@')
		var mbox, host string
		if at >= 0 {
			mbox = a.Address[:at]
			host = a.Address[at+1:]
		} else {
			mbox = a.Address
		}
		out = append(out, imap.Address{Name: a.Name, Mailbox: mbox, Host: host})
	}
	return out
}

func parseEnvelopeDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := mail.ParseDate(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func splitTypeSubtype(mt string) (typ, subtype string) {
	if slash := strings.Index(mt, "/"); slash > 0 {
		return mt[:slash], mt[slash+1:]
	}
	return "", ""
}

func splitMediaType(s string) (mt string, params map[string]string) {
	parts := strings.Split(s, ";")
	mt = strings.TrimSpace(parts[0])
	params = map[string]string{}
	for _, p := range parts[1:] {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		params[strings.ToLower(kv[0])] = strings.Trim(kv[1], `"`)
	}
	return
}

// formatMimeParams renders a Content-Type/Content-Disposition parameter
// map as an IMAP parenthesized attribute/value list, or NIL when empty.
// Keys are sorted so the wire form (and therefore any test asserting exact
// BODYSTRUCTURE text) is deterministic across Go map iteration.
func formatMimeParams(params map[string]string) string {
	if len(params) == 0 {
		return "NIL"
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('(')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(imapQuote(strings.ToUpper(k)))
		sb.WriteByte(' ')
		sb.WriteString(imapQuote(params[k]))
	}
	sb.WriteByte(')')
	return sb.String()
}

// reloadSelected re-reads the selected mailbox's messages list; called
// after STORE / APPEND / EXPUNGE mutations and by IDLE on change-feed
// signal.
func (ses *session) reloadSelected(ctx context.Context) error {
	ses.selMu.Lock()
	id := ses.sel.id
	ses.selMu.Unlock()
	if id == 0 {
		return nil
	}
	msgs, err := listAllMessages(ctx, ses.s.store.Meta(), id, true)
	if err != nil {
		return err
	}
	mb, err := ses.s.store.Meta().GetMailboxByID(ctx, id)
	if err != nil {
		return err
	}
	ses.selMu.Lock()
	ses.sel.msgs = msgs
	ses.sel.uidNext = mb.UIDNext
	ses.selMu.Unlock()
	return nil
}

var _ = errors.New
