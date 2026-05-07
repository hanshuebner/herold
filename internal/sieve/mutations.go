package sieve

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// ApplyMutations rewrites raw per the body- and header-edit actions in
// outcome and returns the new bytes. RFC 5293 (editheader) and RFC 5703
// (replace, enclose) are both honoured. Mutations are applied in source
// order so a script that adds a header then encloses gets the header
// inside the inner part of the enclosure, matching script intent.
//
// The function never returns an error for absence (a script with no
// editheader/mime actions yields raw unchanged). It returns an error
// only when raw cannot be split into a header section + body — i.e.
// when the input is not a well-formed RFC 5322 message.
func ApplyMutations(raw []byte, outcome Outcome) ([]byte, error) {
	if !hasMutations(outcome) {
		return raw, nil
	}
	headerEnd := findHeaderEnd(raw)
	if headerEnd < 0 {
		return nil, fmt.Errorf("sieve: ApplyMutations: no header/body separator")
	}
	headerSection := raw[:headerEnd]
	body := raw[headerEnd:]

	headers := headerSection
	for _, a := range outcome.Actions {
		switch a.Kind {
		case ActionAddHeader:
			headers = applyAddHeader(headers, a.HeaderName, a.HeaderValue)
		case ActionDeleteHeader:
			headers = applyDeleteHeader(headers, a.HeaderName)
		}
	}
	combined := append([]byte{}, headers...)
	combined = append(combined, body...)

	for _, a := range outcome.Actions {
		switch a.Kind {
		case ActionReplace:
			combined = applyReplace(combined, a)
		case ActionEnclose:
			combined = applyEnclose(combined, a)
		}
	}
	return combined, nil
}

// hasMutations reports whether outcome carries any header- or
// body-edit action; ApplyMutations short-circuits when false to avoid
// the parse + rebuild round trip.
func hasMutations(outcome Outcome) bool {
	for _, a := range outcome.Actions {
		switch a.Kind {
		case ActionAddHeader, ActionDeleteHeader, ActionReplace, ActionEnclose:
			return true
		}
	}
	return false
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
			headerSection = headerSection[len(line):]
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

// applyReplace overwrites the message body with a.ReplaceBody and
// honours :subject / :from overrides on the outer headers. The
// existing header section's Subject and From are replaced by the
// override values when set.
func applyReplace(raw []byte, a Action) []byte {
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
