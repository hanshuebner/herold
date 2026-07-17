package gmail

import (
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// ParseGmailLabels splits an X-Gmail-Labels header value into the set
// of label tokens Gmail wrote. The header value:
//
//   - May be RFC 2047-encoded as a whole if it contains non-ASCII
//     (e.g. "=?UTF-8?Q?Posteingang,Ge=C3=B6ffnet?=").
//   - Is comma-separated.
//   - Treats commas inside ASCII double quotes as part of the label
//     token, so that category strings like `Kategorie: "Soziale Medien"`
//     survive intact even though they contain spaces and quotes
//     (commas inside category names are uncommon but the parser
//     respects them anyway).
//
// Returns an empty slice when value is empty or unparseable.
func ParseGmailLabels(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	// Decode any RFC 2047 encoded-words. The header may contain a single
	// encoded-word covering the whole value (Gmail's common form) or be
	// pure ASCII. mailparse.DecodeHeader resolves charsets via the same
	// htmlindex-backed registry the body decoder uses (re #257, re #259)
	// and never blanks the value, unlike a bare mime.WordDecoder on a
	// charset like ISO-8859-15 it does not special-case inline.
	decoded := mailparse.DecodeHeader(value)
	return splitLabels(decoded)
}

// splitLabels splits on commas while respecting ASCII double-quote
// pairs. Whitespace around each token is trimmed; empty tokens are
// dropped.
func splitLabels(s string) []string {
	out := make([]string, 0, 4)
	var cur strings.Builder
	inQuote := false
	flush := func() {
		t := strings.TrimSpace(cur.String())
		cur.Reset()
		if t != "" {
			out = append(out, t)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case ',':
			if inQuote {
				cur.WriteByte(c)
			} else {
				flush()
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}
