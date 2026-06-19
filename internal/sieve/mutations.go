package sieve

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// maxHeaderReadSize is the upper bound on the header block we buffer when
// reading from a seekable source. Matches maildkim.maxHeaderSize (256 KiB);
// any well-formed RFC 5322 header section fits comfortably within this limit.
const maxHeaderReadSize = 256 * 1024

// ApplyMutations streams the message from src (size bytes total) through
// the body- and header-edit actions in outcome, writing the result to dst.
// src must implement io.ReaderAt so the body can be copied via an
// io.SectionReader without loading it into RAM (REQ-STORE-17/19).
//
// For AddHeader/DeleteHeader (the common editheader case): only the header
// block is buffered (bounded by maxHeaderReadSize = 256 KiB). The unmodified
// body is copied from src to dst via io.SectionReader, never held in RAM.
//
// For Replace (top-level): the header block is buffered, the new body comes
// from a.ReplaceBody (a small script-supplied []byte). The original body is
// not read.
//
// For Replace (per-leaf, inside foreverypart): the body is read into memory
// so the existing multipart splitter/assembler can locate and swap the
// targeted leaf. The header remains bounded.
//
// For Enclose: the header block is buffered to extract Subject/From/To for
// the outer wrapper. The original message is then streamed verbatim into the
// embedded message/rfc822 MIME part via io.NewSectionReader — no full-message
// copy in RAM.
//
// changed is false exactly when HasMutations(outcome) is false: in that case
// dst is not written to and src is not read from.
//
// RFC 5293 (editheader) and RFC 5703 (replace, enclose) are both honoured.
// Mutations are applied in source order so a script that adds a header then
// encloses gets the header inside the inner part of the enclosure, matching
// script intent.
func ApplyMutations(src io.ReaderAt, size int64, outcome Outcome, dst io.Writer) (changed bool, err error) {
	if !HasMutations(outcome) {
		return false, nil
	}

	// Read the bounded header block from src.
	headerBytes, headerEnd, err := readHeaderBlock(src, size)
	if err != nil {
		return false, err
	}

	// Classify the mutations: does the outcome contain any body-replacement
	// action (Replace or Enclose)? If yes, use the body-replacement path
	// which routes Replace/Enclose separately. Header-only edits always go
	// through the streaming path.
	hasReplace := false
	hasEnclose := false
	for _, a := range outcome.Actions {
		switch a.Kind {
		case ActionReplace:
			hasReplace = true
		case ActionEnclose:
			hasEnclose = true
		}
	}

	// Phase 1: apply all AddHeader/DeleteHeader mutations to the header block.
	headers := headerBytes
	for _, a := range outcome.Actions {
		switch a.Kind {
		case ActionAddHeader:
			headers = applyAddHeader(headers, a.HeaderName, a.HeaderValue)
		case ActionDeleteHeader:
			headers = applyDeleteHeader(headers, a.HeaderName)
		}
	}

	// If there are no body mutations, use the fully streaming path:
	// write the modified header block, then copy the body via SectionReader.
	// (REQ-STORE-17: body is never loaded into RAM for header-only edits.)
	if !hasReplace && !hasEnclose {
		if _, werr := dst.Write(headers); werr != nil {
			return false, fmt.Errorf("sieve: write headers: %w", werr)
		}
		bodyStart := int64(headerEnd)
		bodyLen := size - bodyStart
		if bodyLen > 0 {
			sr := io.NewSectionReader(src, bodyStart, bodyLen)
			if _, cerr := io.Copy(dst, sr); cerr != nil {
				return false, fmt.Errorf("sieve: stream body: %w", cerr)
			}
		}
		return true, nil
	}

	// Enclose-only path (no Replace): stream the embedded message without
	// loading the body into RAM (REQ-STORE-17).
	//
	// The outer multipart wrapper uses Subject/From/To extracted from the
	// possibly-edited header block (`headers`, post AddHeader/DeleteHeader).
	// The embedded message/rfc822 part is the current message state after
	// header edits: edited headers concatenated with the body streamed from
	// src. This matches the bytes-path behaviour where `combined` = edited
	// headers + body and `applyEnclose` uses `combined` as the inner message.
	if hasEnclose && !hasReplace {
		bodyStart := int64(headerEnd)
		bodyLen := size - bodyStart
		for _, a := range outcome.Actions {
			if a.Kind == ActionEnclose {
				// innerMsg is the edited-headers + body stream. The edited
				// headers are small; the body is streamed from src.
				innerMsg := io.MultiReader(
					bytes.NewReader(headers),
					io.NewSectionReader(src, bodyStart, bodyLen),
				)
				// For the CRLF tail check, read the last 2 bytes from src.
				var innerHasCRLF bool
				if bodyLen >= 2 {
					var tail [2]byte
					if _, rerr := src.ReadAt(tail[:], bodyStart+bodyLen-2); rerr == nil {
						innerHasCRLF = tail[0] == '\r' && tail[1] == '\n'
					}
				} else if bodyLen == 0 && len(headers) >= 2 {
					innerHasCRLF = headers[len(headers)-2] == '\r' && headers[len(headers)-1] == '\n'
				}
				if werr := applyEncloseStreaming(headers, innerMsg, innerHasCRLF, a, dst); werr != nil {
					return false, fmt.Errorf("sieve: enclose stream: %w", werr)
				}
			}
		}
		return true, nil
	}

	// Body mutation path (Replace involved): read the body from src into memory
	// so the multipart splitter/assembler can locate and swap the targeted leaf.
	// The header block is already buffered; only the body bytes are read here.
	// (For per-leaf Replace, this is bounded by the message body size, which is
	// enforced upstream by MaxMessageSize.)
	bodyStart := int64(headerEnd)
	bodyLen := size - bodyStart
	combined := make([]byte, 0, int64(len(headers))+bodyLen)
	combined = append(combined, headers...)
	if bodyLen > 0 {
		body := make([]byte, bodyLen)
		if _, rerr := src.ReadAt(body, bodyStart); rerr != nil && rerr != io.EOF {
			return false, fmt.Errorf("sieve: read body for replace: %w", rerr)
		}
		combined = append(combined, body...)
	}

	// Apply Replace actions.
	for _, a := range outcome.Actions {
		if a.Kind == ActionReplace {
			combined = applyReplace(combined, a)
		}
	}

	// Apply Enclose actions after Replace (combined holds the replaced message).
	for _, a := range outcome.Actions {
		if a.Kind == ActionEnclose {
			combined = applyEnclose(combined, a)
		}
	}

	if _, werr := dst.Write(combined); werr != nil {
		return false, fmt.Errorf("sieve: write mutated: %w", werr)
	}
	return true, nil
}

// ApplyMutationsBytes is a convenience wrapper around ApplyMutations that
// accepts and returns []byte slices. It is used by tests and by callers that
// already hold the complete message in RAM.
//
// When outcome carries no mutation actions, raw is returned unchanged and
// changed is false (no allocation). On a malformed input (no header/body
// separator) err is non-nil.
func ApplyMutationsBytes(raw []byte, outcome Outcome) (out []byte, changed bool, err error) {
	if !HasMutations(outcome) {
		return raw, false, nil
	}
	var buf bytes.Buffer
	buf.Grow(len(raw) + 512)
	changed, err = ApplyMutations(bytes.NewReader(raw), int64(len(raw)), outcome, &buf)
	if err != nil {
		return nil, false, err
	}
	return buf.Bytes(), changed, nil
}

// HasMutations reports whether outcome carries any header- or
// body-edit action. The SMTP delivery path consults this before
// calling ApplyMutations so a script with no editheader/mime actions
// skips the rewrite codepath without a memcmp against raw.
func HasMutations(outcome Outcome) bool {
	for _, a := range outcome.Actions {
		switch a.Kind {
		case ActionAddHeader, ActionDeleteHeader, ActionReplace, ActionEnclose:
			return true
		}
	}
	return false
}

// readHeaderBlock reads the header section from src (up to maxHeaderReadSize
// bytes) using ReadAt, locating the CRLF CRLF (or LF LF) separator.
// Returns the header block bytes (including the blank-line separator) and the
// byte offset within src where the body starts. Returns an error if no
// separator is found or the read fails.
func readHeaderBlock(src io.ReaderAt, size int64) (header []byte, headerEnd int, err error) {
	limit := size
	if limit > maxHeaderReadSize {
		limit = maxHeaderReadSize
	}
	buf := make([]byte, limit)
	n, rerr := src.ReadAt(buf, 0)
	buf = buf[:n]
	if rerr != nil && rerr != io.EOF {
		return nil, -1, fmt.Errorf("sieve: read header block: %w", rerr)
	}
	end := findHeaderEnd(buf)
	if end < 0 {
		return nil, -1, fmt.Errorf("sieve: ApplyMutations: no header/body separator")
	}
	return buf[:end], end, nil
}

// findHeaderEnd returns the byte index just past the CRLF CRLF (or LF
// LF) header/body separator in raw, or -1 when no separator is found.
// The returned index includes the blank-line bytes so callers can
// split raw into [:headerEnd] and [headerEnd:] without further work.
func findHeaderEnd(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i + 4
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i + 2
	}
	return -1
}

// applyAddHeader inserts "name: value" at the top of the header block
// per RFC 5293 §4: addheader without :last prepends. Idempotent for
// callers — a second add with the same name+value adds a second
// occurrence (matching the RFC's "may add multiple instances").
func applyAddHeader(headerSection []byte, name, value string) []byte {
	line := []byte(name + ": " + value + "\r\n")
	return append(line, headerSection...)
}

// applyDeleteHeader removes every header line (including continuation
// lines) whose field name matches name (case-insensitive per RFC 5322
// §3.6). The trailing CRLF separator is preserved.
func applyDeleteHeader(headerSection []byte, name string) []byte {
	lower := strings.ToLower(name) + ":"
	var out bytes.Buffer
	out.Grow(len(headerSection))
	skipping := false
	for len(headerSection) > 0 {
		// Slice off one logical line (continuation-aware).
		end := bytes.Index(headerSection, []byte("\r\n"))
		if end < 0 {
			end = len(headerSection) - 2
			if end < 0 {
				end = 0
			}
		}
		line := headerSection[:end+2]
		// A continuation line starts with whitespace and belongs to
		// the previous header.
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if !skipping {
				out.Write(line)
			}
			headerSection = headerSection[len(line):]
			continue
		}
		// Blank line -> end of header section; preserve and stop.
		if bytes.Equal(line, []byte("\r\n")) {
			out.Write(line)
			break
		}
		// Field line: check the name.
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			out.Write(line)
			skipping = false
		} else if strings.EqualFold(string(line[:colon+1]), lower) {
			skipping = true
		} else {
			out.Write(line)
			skipping = false
		}
		headerSection = headerSection[len(line):]
	}
	return out.Bytes()
}

// applyReplace overwrites a target body with a.ReplaceBody. When
// ReplacePartPath is empty the target is the message body (the
// original Phase 1.5 top-level behaviour, which also applies the
// :subject / :from overrides on the outer headers). When the path is
// non-empty the replace was emitted from inside a foreverypart loop
// and targets only the named MIME part — the path indexes into
// msg.Body.Children, with each successive index descending one level.
//
// Per-leaf replace works by locating the part's body in raw via a
// boundary-aware walk that tracks each multipart's boundary string
// and the order of its children. The whole-message Subject / From
// overrides are NOT honoured for per-leaf replace; they apply only
// to top-level rewrites where the script is replacing the whole
// message body and the outer headers come along.
func applyReplace(raw []byte, a Action) []byte {
	if len(a.ReplacePartPath) > 0 {
		out, ok := replacePartByPath(raw, a.ReplacePartPath, a.ReplaceBody)
		if ok {
			return out
		}
		// Path lookup failed (target part not found): fall through to
		// top-level replace as a defensive degraded mode rather than
		// dropping the script's intent.
	}
	headerEnd := findHeaderEnd(raw)
	if headerEnd < 0 {
		return raw
	}
	headers := raw[:headerEnd]
	if a.ReplaceSubject != "" {
		headers = setOrReplaceHeader(headers, "Subject", a.ReplaceSubject)
	}
	if a.ReplaceFrom != "" {
		headers = setOrReplaceHeader(headers, "From", a.ReplaceFrom)
	}
	out := append([]byte{}, headers...)
	body := a.ReplaceBody
	if !bytes.HasSuffix(body, []byte("\r\n")) {
		body = append(append([]byte{}, body...), '\r', '\n')
	}
	out = append(out, body...)
	return out
}

// replacePartByPath rewrites the byte range corresponding to the part
// at path inside raw. The path is an index sequence:
// path[0] indexes the children of the message-level multipart,
// path[1] the children of THAT part, and so on. Returns (newRaw, true)
// on success; (raw, false) when the path cannot be resolved (e.g. raw
// is not multipart, or an index is out of range, or the message uses
// boundaries the walker cannot recognise).
//
// The walker recognises CRLF "--<boundary>" lines that delimit MIME
// parts; it does not parse Content-Transfer-Encoding or otherwise
// decode part bodies. The replacement is inserted verbatim with a
// fresh "Content-Type: text/plain" header, preserving the surrounding
// part order and the parent multipart's boundary string.
func replacePartByPath(raw []byte, path []int, replacement []byte) ([]byte, bool) {
	headerEnd := findHeaderEnd(raw)
	if headerEnd < 0 {
		return raw, false
	}
	headers := raw[:headerEnd]
	body := raw[headerEnd:]
	boundary := readBoundary(headers)
	if boundary == "" {
		return raw, false
	}
	newBody, ok := replacePartInMultipart(body, boundary, path, replacement)
	if !ok {
		return raw, false
	}
	out := append([]byte{}, headers...)
	out = append(out, newBody...)
	return out, true
}

// replacePartInMultipart walks a multipart body delimited by boundary
// and rewrites the part at path. path[0] selects which child of THIS
// multipart to descend into; if len(path) == 1, that child is the
// target and gets replaced wholesale (headers + body) with a
// freshly-rendered text/plain part containing replacement. Otherwise
// the function recurses into the chosen child's nested multipart.
func replacePartInMultipart(body []byte, boundary string, path []int, replacement []byte) ([]byte, bool) {
	if len(path) == 0 {
		return body, false
	}
	delim := []byte("--" + boundary)
	parts := splitMultipart(body, boundary)
	idx := path[0]
	if idx < 0 || idx >= len(parts) {
		return body, false
	}
	if len(path) == 1 {
		// Replace this child entirely.
		newPart := buildReplacementPart(replacement)
		return assembleMultipart(parts, idx, newPart, delim), true
	}
	// Descend: the targeted child must itself be a multipart with its
	// own boundary in its headers.
	child := parts[idx]
	childHeaderEnd := findHeaderEnd(child)
	if childHeaderEnd < 0 {
		return body, false
	}
	childHeaders := child[:childHeaderEnd]
	childBody := child[childHeaderEnd:]
	childBoundary := readBoundary(childHeaders)
	if childBoundary == "" {
		return body, false
	}
	newChildBody, ok := replacePartInMultipart(childBody, childBoundary, path[1:], replacement)
	if !ok {
		return body, false
	}
	newChild := append([]byte{}, childHeaders...)
	newChild = append(newChild, newChildBody...)
	return assembleMultipart(parts, idx, newChild, delim), true
}

// splitMultipart returns the per-part byte slices between
// "--<boundary>" delimiters in body. The slices INCLUDE the part's
// headers and body but NOT the surrounding boundary lines. The
// preamble (text before the first boundary) and the closing
// "--<boundary>--" are dropped.
func splitMultipart(body []byte, boundary string) [][]byte {
	delim := []byte("\r\n--" + boundary)
	closer := []byte("\r\n--" + boundary + "--")
	// Strip everything up to and including the first delimiter.
	start := bytes.Index(body, []byte("--"+boundary))
	if start < 0 {
		return nil
	}
	// Position cursor at start of the first part (after the boundary
	// line's CRLF).
	first := body[start:]
	if i := bytes.Index(first, []byte("\r\n")); i >= 0 {
		first = first[i+2:]
	}
	var parts [][]byte
	cursor := first
	for {
		closeIdx := bytes.Index(cursor, closer)
		delimIdx := bytes.Index(cursor, delim)
		// Whichever boundary comes FIRST determines this part. The
		// closer ends iteration; a plain delim continues to the next.
		switch {
		case closeIdx < 0 && delimIdx < 0:
			// Malformed: no terminator found. Treat the rest as the
			// last part.
			parts = append(parts, append([]byte{}, cursor...))
			return parts
		case closeIdx >= 0 && (delimIdx < 0 || closeIdx <= delimIdx):
			parts = append(parts, append([]byte{}, cursor[:closeIdx]...))
			return parts
		default:
			parts = append(parts, append([]byte{}, cursor[:delimIdx]...))
			next := cursor[delimIdx+len(delim):]
			if j := bytes.Index(next, []byte("\r\n")); j >= 0 {
				next = next[j+2:]
			}
			cursor = next
		}
	}
}

// assembleMultipart builds a fresh multipart body with parts[idx]
// swapped for newPart. The leading preamble and trailing closer are
// emitted in the canonical RFC 2046 form.
func assembleMultipart(parts [][]byte, idx int, newPart, delim []byte) []byte {
	var out bytes.Buffer
	for i, p := range parts {
		out.Write(delim)
		out.WriteString("\r\n")
		if i == idx {
			out.Write(newPart)
		} else {
			out.Write(p)
		}
		if !bytes.HasSuffix(out.Bytes(), []byte("\r\n")) {
			out.WriteString("\r\n")
		}
	}
	out.Write(delim)
	out.WriteString("--\r\n")
	return out.Bytes()
}

// buildReplacementPart returns the byte form of a freshly-rendered
// text/plain MIME leaf carrying replacement as its body.
func buildReplacementPart(replacement []byte) []byte {
	var out bytes.Buffer
	out.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	out.WriteString("\r\n")
	out.Write(replacement)
	if !bytes.HasSuffix(replacement, []byte("\r\n")) {
		out.WriteString("\r\n")
	}
	return out.Bytes()
}

// readBoundary extracts the boundary parameter from a Content-Type
// header in the supplied header section. Returns "" when no
// Content-Type carries a boundary.
func readBoundary(headerSection []byte) string {
	ct := readHeader(headerSection, "Content-Type")
	if ct == "" {
		return ""
	}
	// Find boundary= parameter; strip surrounding quotes if any.
	lower := strings.ToLower(ct)
	idx := strings.Index(lower, "boundary=")
	if idx < 0 {
		return ""
	}
	rest := ct[idx+len("boundary="):]
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return ""
		}
		return rest[1 : 1+end]
	}
	// Bare boundary token: ends at next ; or whitespace.
	end := strings.IndexAny(rest, "; \t")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// applyEnclose wraps raw in a multipart/mixed container whose first
// leaf is a.EncloseBody (text/plain) and whose second leaf is the
// original message (message/rfc822). The :subject override applies to
// the outer container; :headers extras are inserted verbatim into the
// outer header section.
func applyEnclose(raw []byte, a Action) []byte {
	boundary := newEncloseBoundary()
	innerBody := a.EncloseBody
	if !bytes.HasSuffix(innerBody, []byte("\r\n")) {
		innerBody = append(append([]byte{}, innerBody...), '\r', '\n')
	}
	headerEnd := findHeaderEnd(raw)
	if headerEnd < 0 {
		return raw
	}
	innerHeaders := raw[:headerEnd]
	innerWhole := raw

	var out bytes.Buffer
	if a.EncloseSubject != "" {
		fmt.Fprintf(&out, "Subject: %s\r\n", a.EncloseSubject)
	} else {
		// Echo the inner Subject so the operator's mail client still
		// shows context.
		if subj := readHeader(innerHeaders, "Subject"); subj != "" {
			fmt.Fprintf(&out, "Subject: %s\r\n", subj)
		}
	}
	if from := readHeader(innerHeaders, "From"); from != "" {
		fmt.Fprintf(&out, "From: %s\r\n", from)
	}
	if to := readHeader(innerHeaders, "To"); to != "" {
		fmt.Fprintf(&out, "To: %s\r\n", to)
	}
	for _, h := range a.EncloseHeaders {
		h = strings.TrimRight(h, "\r\n")
		if h != "" {
			out.WriteString(h)
			out.WriteString("\r\n")
		}
	}
	fmt.Fprintf(&out, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&out, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary)
	out.WriteString("\r\n")
	fmt.Fprintf(&out, "--%s\r\n", boundary)
	out.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	out.Write(innerBody)
	fmt.Fprintf(&out, "--%s\r\n", boundary)
	out.WriteString("Content-Type: message/rfc822\r\n\r\n")
	out.Write(innerWhole)
	if !bytes.HasSuffix(innerWhole, []byte("\r\n")) {
		out.WriteString("\r\n")
	}
	fmt.Fprintf(&out, "--%s--\r\n", boundary)
	return out.Bytes()
}

// applyEncloseStreaming writes the enclose-wrapped message to dst without
// holding the entire inner message in RAM (REQ-STORE-17).
//
// innerHeaders is the (possibly header-edited) header block; it is used
// only to extract Subject/From/To for the outer multipart/mixed wrapper.
// innerMsg is an io.Reader that supplies the full embedded message (edited
// headers + body); it is streamed into the message/rfc822 MIME part.
// innerHasCRLF reports whether the last two bytes of innerMsg are CRLF so
// the caller does not need to re-read them after copying.
func applyEncloseStreaming(innerHeaders []byte, innerMsg io.Reader, innerHasCRLF bool, a Action, dst io.Writer) error {
	boundary := newEncloseBoundary()
	innerBody := a.EncloseBody
	if !bytes.HasSuffix(innerBody, []byte("\r\n")) {
		innerBody = append(append([]byte{}, innerBody...), '\r', '\n')
	}

	// Build the outer header block in memory (small, bounded).
	var outerHdr bytes.Buffer
	if a.EncloseSubject != "" {
		fmt.Fprintf(&outerHdr, "Subject: %s\r\n", a.EncloseSubject)
	} else {
		if subj := readHeader(innerHeaders, "Subject"); subj != "" {
			fmt.Fprintf(&outerHdr, "Subject: %s\r\n", subj)
		}
	}
	if from := readHeader(innerHeaders, "From"); from != "" {
		fmt.Fprintf(&outerHdr, "From: %s\r\n", from)
	}
	if to := readHeader(innerHeaders, "To"); to != "" {
		fmt.Fprintf(&outerHdr, "To: %s\r\n", to)
	}
	for _, h := range a.EncloseHeaders {
		h = strings.TrimRight(h, "\r\n")
		if h != "" {
			outerHdr.WriteString(h)
			outerHdr.WriteString("\r\n")
		}
	}
	fmt.Fprintf(&outerHdr, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&outerHdr, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary)
	outerHdr.WriteString("\r\n")

	if _, err := dst.Write(outerHdr.Bytes()); err != nil {
		return err
	}

	// First MIME part: text/plain preamble.
	fmt.Fprintf(dst, "--%s\r\n", boundary)
	io.WriteString(dst, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	dst.Write(innerBody)

	// Second MIME part: the inner message streamed from innerMsg.
	fmt.Fprintf(dst, "--%s\r\n", boundary)
	io.WriteString(dst, "Content-Type: message/rfc822\r\n\r\n")
	if _, err := io.Copy(dst, innerMsg); err != nil {
		return err
	}
	// Ensure the embedded part ends with CRLF so the closing boundary is
	// on its own line (RFC 2046 §5.1.1).
	if !innerHasCRLF {
		io.WriteString(dst, "\r\n")
	}

	fmt.Fprintf(dst, "--%s--\r\n", boundary)
	return nil
}

// setOrReplaceHeader replaces the value of the named header (if
// present) or appends a fresh one to the top of the header section.
// Continuation lines on a replaced header are dropped along with the
// field line.
func setOrReplaceHeader(headerSection []byte, name, value string) []byte {
	deleted := applyDeleteHeader(headerSection, name)
	return applyAddHeader(deleted, name, value)
}

// readHeader returns the trimmed value of the named header, or "" if
// absent. Used to echo Subject/From/To onto an enclose'd outer message
// when the script did not override them explicitly.
func readHeader(headerSection []byte, name string) string {
	lower := strings.ToLower(name) + ":"
	rest := headerSection
	for len(rest) > 0 {
		end := bytes.Index(rest, []byte("\r\n"))
		if end < 0 {
			break
		}
		line := rest[:end]
		rest = rest[end+2:]
		if bytes.Equal(line, []byte("")) {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if strings.EqualFold(string(line[:colon+1]), lower) {
			val := strings.TrimSpace(string(line[colon+1:]))
			// Capture continuation lines.
			for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
				cend := bytes.Index(rest, []byte("\r\n"))
				if cend < 0 {
					break
				}
				val += " " + strings.TrimSpace(string(rest[:cend]))
				rest = rest[cend+2:]
			}
			return val
		}
	}
	return ""
}

// newEncloseBoundary returns a 16-byte hex multipart boundary. Only
// callable by the in-process interpreter, so a non-cryptographic
// random source would suffice; we use crypto/rand for consistency
// with the DSN builder's boundary helper.
func newEncloseBoundary() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "herold-sieve-" + hex.EncodeToString(b[:])
}
