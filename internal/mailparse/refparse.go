package mailparse

import "strings"

// NormalizeMessageID strips angle brackets and lowercases a Message-ID
// value. RFC 5322 §3.6.4 makes domain-part comparisons case-insensitive;
// we normalise the whole token for maximum threading recall across stored
// messages with mixed-case identifiers.
func NormalizeMessageID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.ToLower(id)
}

// ParseReferences extracts Message-IDs from a References or In-Reply-To
// header value. Both headers carry one or more "<id>" tokens separated
// by whitespace and optional folded whitespace (CFWS per RFC 5322).
// Returns the IDs in left-to-right order, each normalised by
// NormalizeMessageID. Empty or malformed tokens are silently skipped.
func ParseReferences(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inAngle := false
	for _, r := range s {
		switch {
		case r == '<':
			inAngle = true
			cur.Reset()
		case r == '>':
			if inAngle {
				inAngle = false
				v := NormalizeMessageID(cur.String())
				if v != "" {
					out = append(out, v)
				}
				cur.Reset()
			}
		default:
			if inAngle {
				cur.WriteRune(r)
			}
		}
	}
	return out
}
