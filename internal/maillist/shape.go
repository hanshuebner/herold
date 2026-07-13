package maillist

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// maxHeaderFieldLen caps a single sanitized header field. Mirrors
// protosmtp's sanitizeHeaderValue budget; duplicated here rather than
// imported because protosmtp depends on maillist (not the reverse) and
// the helper is three lines of pure string scanning.
const maxHeaderFieldLen = 1024

// sanitizeForHeader makes s safe to embed in a structured header value:
// any byte outside the printable-ASCII safe range is replaced with '_',
// and the result is capped at maxHeaderFieldLen. Applied to admin-
// supplied list config (DisplayName, PostingAddress) before it is
// spliced into List-ID / List-Post so a malformed or malicious display
// name can never inject a header-shape byte (CR/LF) into a fanned-out
// copy.
func sanitizeForHeader(s string) string {
	if len(s) > maxHeaderFieldLen {
		s = s[:maxHeaderFieldLen]
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			b.WriteByte('_')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// splitHeaderBody splits raw RFC 5322 message bytes at the first blank
// line into the header block (without the trailing blank line) and the
// body. Both CRLF and bare-LF blank-line separators are accepted; the
// inbound pipeline normalises to CRLF before this point, but tests
// sometimes hand in LF-only fixtures.
func splitHeaderBody(raw []byte) (header, body []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i], raw[i+4:]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i], raw[i+2:]
	}
	return raw, nil
}

// splitPhysicalLines splits a header block into physical lines, each
// retaining its trailing line terminator so reassembly is byte-exact.
func splitPhysicalLines(header []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(header); i++ {
		if header[i] == '\n' {
			lines = append(lines, header[start:i+1])
			start = i + 1
		}
	}
	if start < len(header) {
		lines = append(lines, header[start:])
	}
	return lines
}

// isFoldContinuation reports whether line is a folded continuation of
// the previous header (RFC 5322 §2.2.3: begins with SP or HTAB).
func isFoldContinuation(line []byte) bool {
	return len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
}

// headerNameOf returns the field-name portion of a non-continuation
// header line (text before the first colon), or "" if line has no
// colon.
func headerNameOf(line []byte) string {
	idx := bytes.IndexByte(line, ':')
	if idx < 0 {
		return ""
	}
	return string(line[:idx])
}

// stripHeader removes every occurrence of the named header (including
// its folded continuation lines) from header, returning the remaining
// bytes. Case-insensitive on the field name.
func stripHeader(header []byte, name string) []byte {
	lines := splitPhysicalLines(header)
	var out bytes.Buffer
	out.Grow(len(header))
	skipping := false
	for _, line := range lines {
		if !isFoldContinuation(line) {
			skipping = strings.EqualFold(headerNameOf(line), name)
			if skipping {
				continue
			}
		} else if skipping {
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

// applySubjectTag prefixes tag (already stripped of surrounding
// whitespace, without brackets) onto the Subject: header's raw value,
// unless decodedSubject already starts with "[tag] " (REQ-MLIST-23,
// idempotent -- no double-tagging a reply that already carries it).
//
// The splice happens on the RAW header bytes, inserting the ASCII
// prefix immediately after "Subject:" (and one optional leading space)
// and leaving everything else -- including any original RFC 2047
// encoded-word content and any folded continuation lines -- untouched.
// decodedSubject (the already-decoded value, e.g. from
// mailparse.Headers.Get) is used only for the idempotency comparison.
func applySubjectTag(header []byte, tag, decodedSubject string) []byte {
	prefix := "[" + tag + "] "
	if strings.HasPrefix(decodedSubject, prefix) {
		return header
	}
	lines := splitPhysicalLines(header)
	var out bytes.Buffer
	out.Grow(len(header) + len(prefix))
	inserted := false
	for _, line := range lines {
		if !inserted && !isFoldContinuation(line) && strings.EqualFold(headerNameOf(line), "Subject") {
			colon := bytes.IndexByte(line, ':')
			fieldName := line[:colon+1]
			rest := string(line[colon+1:])
			eol := ""
			switch {
			case strings.HasSuffix(rest, "\r\n"):
				eol = "\r\n"
				rest = strings.TrimSuffix(rest, eol)
			case strings.HasSuffix(rest, "\n"):
				eol = "\n"
				rest = strings.TrimSuffix(rest, eol)
			}
			rest = strings.TrimPrefix(rest, " ")
			out.Write(fieldName)
			out.WriteByte(' ')
			out.WriteString(prefix)
			out.WriteString(rest)
			out.WriteString(eol)
			inserted = true
			continue
		}
		out.Write(line)
	}
	if !inserted {
		out.WriteString("Subject: " + prefix + "\r\n")
	}
	return out.Bytes()
}

// listLocalPart returns the local-part of ml.PostingAddress -- the
// "listname" component of the RFC 2919 list-id (REQ-MLIST-20) and of
// the S1 bounce address (REQ-MLIST-22).
func listLocalPart(ml store.MailingList) string {
	addr := ml.PostingAddress
	if i := strings.IndexByte(addr, '@'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// ListIdentifier returns the RFC 2919 list-id token without angle
// brackets: "<listname>.<domain>". This is the value REQ-MLIST-30's
// loop guard compares against an inbound message's own List-ID header.
func ListIdentifier(ml store.MailingList) string {
	return listLocalPart(ml) + "." + ml.Domain
}

// ListIDHeaderValue renders the REQ-MLIST-20 List-ID header value:
// `"<display_name>" <listname.domain>`.
func ListIDHeaderValue(ml store.MailingList) string {
	name := sanitizeForHeader(ml.DisplayName)
	return fmt.Sprintf("%q <%s>", name, ListIdentifier(ml))
}

// LoopDetected reports whether an inbound message's own List-ID header
// value already names ml (REQ-MLIST-30): the message already passed
// through this list's fan-out (or some other loop reintroduced it) and
// MUST NOT be re-expanded. Comparison is on the bracketed list-id token
// only (case-insensitive substring), not the decorative display-name
// phrase, so a display-name rename between two fan-outs of the same
// list still detects the loop.
func LoopDetected(existingListIDHeader string, ml store.MailingList) bool {
	if existingListIDHeader == "" {
		return false
	}
	token := "<" + strings.ToLower(ListIdentifier(ml)) + ">"
	return strings.Contains(strings.ToLower(existingListIDHeader), token)
}

// AutoSubmittedBlocks reports whether an Auto-Submitted header value
// blocks posting to a list (REQ-MLIST-31): any value other than "no"
// (case-insensitive, ignoring any ";"-separated parameters) MUST NOT
// reach the list, preventing DSN and vacation-responder loops. An
// absent header (empty value) is "no" by the RFC 3834 default.
func AutoSubmittedBlocks(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = value[:i]
	}
	return !strings.EqualFold(strings.TrimSpace(value), "no")
}

// SizeExceeds reports whether msgSize exceeds the list's configured
// post-size ceiling (REQ-MLIST-32), falling back to deploymentDefault
// when the list has no override (MaxMessageSizeBytes == 0). A
// non-positive effective limit (both the list override and the
// deployment default are unset) means "no limit".
func SizeExceeds(msgSize int64, ml store.MailingList, deploymentDefault int64) bool {
	limit := ml.MaxMessageSizeBytes
	if limit <= 0 {
		limit = deploymentDefault
	}
	if limit <= 0 {
		return false
	}
	return msgSize > limit
}

// BounceAddress computes the S1 envelope MAIL FROM for a list's
// fan-out copies (REQ-MLIST-22): a fixed, list-wide address with a
// "+bounce" suffix on the posting address's local-part. It has no
// per-member attribution. Stage 2's VERP token (REQ-MLIST-50,
// "<list>+bounce-<hmac(member_id)>@<domain>") extends this exact prefix
// with a per-member HMAC suffix, so the later change only adds a
// suffix here rather than restructuring the envelope-sender seam.
func BounceAddress(ml store.MailingList) string {
	return listLocalPart(ml) + "+bounce@" + ml.Domain
}

// buildPrependedHeaders renders the REQ-MLIST-20/24 headers a fan-out
// copy always carries: List-ID, List-Post, and Auto-Submitted. Callers
// prepend the result to the header block after any existing
// Auto-Submitted header has been stripped (ShapeMessage does this).
func buildPrependedHeaders(ml store.MailingList) []byte {
	var b bytes.Buffer
	b.WriteString("List-ID: ")
	b.WriteString(ListIDHeaderValue(ml))
	b.WriteString("\r\n")
	b.WriteString("List-Post: <mailto:")
	b.WriteString(sanitizeForHeader(ml.PostingAddress))
	b.WriteString(">\r\n")
	b.WriteString("Auto-Submitted: auto-forwarded\r\n")
	return b.Bytes()
}

// ShapeMessage builds one fan-out copy of raw (REQ-MLIST-11: applied at
// render/enqueue time, never by rewriting the persisted blob). It
// prepends List-ID / List-Post / Auto-Submitted: auto-forwarded and, if
// ml.SubjectTag is set, idempotently prefixes the Subject. ARC-sealing
// (REQ-MLIST-21) is the caller's job (Expander.Expand): it needs a live
// Sealer and the message's own prior authentication verdict, and must
// run over the fully header-shaped bytes ShapeMessage returns so the
// ARC-Message-Signature covers the list's own modifications (the
// standard "intermediary attests to what it is relaying" ARC pattern).
//
// decodedSubject is the RFC 2047-decoded Subject value (e.g.
// mailparse.Message.Headers.Get("Subject")).
func ShapeMessage(raw []byte, ml store.MailingList, decodedSubject string) []byte {
	header, body := splitHeaderBody(raw)
	if ml.SubjectTag != nil {
		if tag := strings.TrimSpace(*ml.SubjectTag); tag != "" {
			header = applySubjectTag(header, tag, decodedSubject)
		}
	}
	header = stripHeader(header, "Auto-Submitted")

	var out bytes.Buffer
	out.Grow(len(header) + len(body) + 256)
	out.Write(buildPrependedHeaders(ml))
	out.Write(header)
	out.WriteString("\r\n\r\n")
	out.Write(body)
	return out.Bytes()
}
