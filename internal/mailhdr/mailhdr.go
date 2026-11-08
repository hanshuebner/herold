// Package mailhdr renders RFC 5322 header fields and RFC 2045/2231
// parameters for outbound messages so that no physical line exceeds the
// limits receiving MXes enforce (78 octets recommended, 998 hard; a longer
// line draws "501 Syntax error - line too long").
package mailhdr

import (
	"mime"
	"strings"
	"unicode/utf8"
)

// FoldWidth is the column at which header lines are folded
// (RFC 5322 section 2.1.1 recommended maximum).
const FoldWidth = 78

// Fold renders "Name: value\r\n", folded at whitespace.
func Fold(name, value string) string {
	return name + ":" + FoldValue(name, value) + "\r\n"
}

// FoldValue returns the text that follows "Name:" in a header field --
// including the separating space -- with CRLF inserted before whitespace
// so that lines stay within FoldWidth where the value offers a fold point
// (RFC 5322 section 2.2.3). The result carries no trailing CRLF. A fold is
// only ever placed before a non-whitespace token, so no physical line is
// whitespace-only, and unfolding (removing each CRLF) restores the input.
// Existing CRLF-plus-whitespace folds in value are preserved. A token
// longer than the remaining width stays on one line.
func FoldValue(name, value string) string {
	var out strings.Builder
	lineLen := len(name) + 1
	hasTok := true // the colon counts: a fold directly after it is legal
	rest := " " + value
	for len(rest) > 0 {
		if strings.HasPrefix(rest, "\r\n") {
			out.WriteString("\r\n")
			rest = rest[2:]
			lineLen, hasTok = 0, false
			continue
		}
		// whitespace run
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
		ws := rest[:i]
		rest = rest[i:]
		// token: up to next whitespace or CR
		j := 0
		for j < len(rest) && rest[j] != ' ' && rest[j] != '\t' && rest[j] != '\r' {
			j++
		}
		if j == 0 && len(rest) > 0 && rest[0] == '\r' && !strings.HasPrefix(rest, "\r\n") {
			j = 1 // lone CR: keep as token content, do not loop forever
		}
		tok := rest[:j]
		rest = rest[j:]
		if tok != "" && ws != "" && hasTok && lineLen+len(ws)+len(tok) > FoldWidth {
			out.WriteString("\r\n")
			lineLen, hasTok = 0, false
		}
		out.WriteString(ws)
		out.WriteString(tok)
		lineLen += len(ws) + len(tok)
		if tok != "" {
			hasTok = true
		}
	}
	return out.String()
}

// EncodeWords returns s as is when it is printable ASCII; otherwise it
// returns RFC 2047 encoded-words (UTF-8, Q encoding) of at most 75
// characters each, separated by CRLF-space so the result folds within
// FoldWidth. mime.WordEncoder never splits a UTF-8 sequence across words.
func EncodeWords(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}

// maxParamSegment is the largest percent-encoded payload placed in one
// RFC 2231 continuation segment.
const maxParamSegment = 90

// ExtendedParam renders attr=value as RFC 2231 continuations
// (attr*0*=utf-8”..., attr*1*=...), each segment short enough to fold on
// its own line. Segments end on rune boundaries and percent-escapes are
// never split. mime.ParseMediaType reassembles them.
func ExtendedParam(attr, value string) string {
	var segs []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			segs = append(segs, cur.String())
			cur.Reset()
		}
	}
	for len(value) > 0 {
		_, n := utf8.DecodeRuneInString(value)
		var enc strings.Builder
		for _, b := range []byte(value[:n]) {
			if attrChar(b) {
				enc.WriteByte(b)
			} else {
				enc.WriteString("%" + string("0123456789ABCDEF"[b>>4]) + string("0123456789ABCDEF"[b&15]))
			}
		}
		if cur.Len()+enc.Len() > maxParamSegment {
			flush()
		}
		cur.WriteString(enc.String())
		value = value[n:]
	}
	flush()
	if len(segs) == 0 {
		return attr + "=\"\""
	}
	if len(segs) == 1 {
		return attr + "*=utf-8''" + segs[0]
	}
	parts := make([]string, len(segs))
	for i, s := range segs {
		if i == 0 {
			parts[i] = attr + "*0*=utf-8''" + s
		} else {
			parts[i] = attr + "*" + itoa(i) + "*=" + s
		}
	}
	return strings.Join(parts, "; ")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// attrChar reports whether b may appear unescaped in an RFC 2231
// extended value (RFC 5987 attr-char).
func attrChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}
