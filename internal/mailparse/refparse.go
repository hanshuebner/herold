package mailparse

import (
	"strings"
	"unicode"
)

// NormalizeMessageID strips angle brackets and lowercases a Message-ID
// value. RFC 5322 §3.6.4 makes domain-part comparisons case-insensitive;
// we normalise the whole token for maximum threading recall across stored
// messages with mixed-case identifiers.
//
// Bracket stripping is greedy: trailing/leading runs of < and > are
// removed so the function is idempotent under repeated application.
// Real Message-IDs only ever carry one layer of brackets; the greedy
// strip exists so a malformed `<<id@host>>` (seen on the wire and in
// fuzz harnesses) round-trips through the function the same way as a
// well-formed `<id@host>`.
func NormalizeMessageID(id string) string {
	id = strings.TrimFunc(id, isMsgIDEdge)
	return strings.ToLower(id)
}

// isMsgIDEdge reports whether r is one of the wrapping characters
// stripped from a Message-ID before comparison. Whitespace, '<', and
// '>' are all wrapping by RFC 5322's msg-id grammar; trimming all
// three in a single pass keeps the function idempotent.
func isMsgIDEdge(r rune) bool {
	return unicode.IsSpace(r) || r == '<' || r == '>'
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

// UnionReferences returns the deduplicated, normalised Message-IDs named
// by inReplyTo and references together, In-Reply-To entries first (RFC
// 8621 sec 8.1's single-hop shortcut takes precedence over the full
// References ancestry chain when both are present and a caller only
// wants the first match). Used both to seed the ancestor-lookup walk at
// insert time and to record a message's outgoing edges in
// message_references (REQ-STORE-40, issue #485), so both call sites
// name the exact same set of ancestors for a given message.
func UnionReferences(inReplyTo, references string) []string {
	refs := ParseReferences(inReplyTo)
	seen := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		seen[r] = struct{}{}
	}
	for _, r := range ParseReferences(references) {
		if _, dup := seen[r]; !dup {
			refs = append(refs, r)
			seen[r] = struct{}{}
		}
	}
	return refs
}
