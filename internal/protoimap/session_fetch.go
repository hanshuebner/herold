package protoimap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"

	imap "github.com/emersion/go-imap/v2"
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
	seqs := ses.expandSet(c.FetchSet, c.IsUID)
	for _, seq := range seqs {
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
	return buf.Bytes(), nil
}

// extractSection returns the requested section of the raw message body
// for Phase 1. We support: full body (empty section), HEADER, TEXT,
// HEADER.FIELDS / HEADER.FIELDS.NOT, and part-specifier [n] for top-level
// single-part messages (returning the whole body). Multi-part traversal
// is deferred.
func extractSection(raw []byte, sec *imap.FetchItemBodySection) []byte {
	if sec == nil || (sec.Specifier == imap.PartSpecifierNone && len(sec.Part) == 0 && len(sec.HeaderFields) == 0 && len(sec.HeaderFieldsNot) == 0) {
		return raw
	}
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

// formatBodyStructure emits a conservative single-part BODYSTRUCTURE for
// any message; multipart walking is a Phase 2 enhancement.
func formatBodyStructure(raw []byte, extended bool) string {
	hdr, body := splitRawMessage(raw)
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	contentType := "text"
	subType := "plain"
	params := ""
	if err == nil {
		ct := m.Header.Get("Content-Type")
		if ct != "" {
			mt, plist := splitMediaType(ct)
			if slash := strings.Index(mt, "/"); slash > 0 {
				contentType = strings.ToUpper(mt[:slash])
				subType = strings.ToUpper(mt[slash+1:])
			}
			params = formatMimeParams(plist)
		}
	}
	_ = hdr
	size := len(body)
	lines := bytes.Count(body, []byte("\n"))
	label := "BODY"
	if extended {
		label = "BODYSTRUCTURE"
	}
	return fmt.Sprintf("%s (\"%s\" \"%s\" %s NIL NIL \"7BIT\" %d %d)", label, contentType, subType, params, size, lines)
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

func formatMimeParams(params map[string]string) string {
	if len(params) == 0 {
		return "NIL"
	}
	var sb strings.Builder
	sb.WriteByte('(')
	first := true
	for k, v := range params {
		if !first {
			sb.WriteByte(' ')
		}
		first = false
		sb.WriteString(imapQuote(strings.ToUpper(k)))
		sb.WriteByte(' ')
		sb.WriteString(imapQuote(v))
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
	msgs, err := ses.s.store.Meta().ListMessages(ctx, id, store.MessageFilter{WithEnvelope: true})
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
