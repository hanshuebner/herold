package mailparse

import (
	"regexp"
	"strings"
)

// baseSubjectPrefixRe matches one leading reply or forward marker: Re:,
// Aw: (German "Antwort"), Fwd:, Fw:, Res: (Portuguese/Spanish
// "Resposta"/"Respuesta"), Wg: (German "Weiterleitung"). Matching is
// case-insensitive; the colon may be surrounded by whitespace or by
// none, so both "Re: Subject" and "Re:Subject" strip the same way.
// This regex's `\s` class is ASCII-only (RE2 does not include U+000B
// vertical tab, NBSP, or the other Unicode space separators in it), so
// it only ever runs against input already passed through
// foldWhitespace, which has replaced every whitespace character
// unicode.IsSpace recognises with a single ASCII space; that keeps the
// prefix strip and the final whitespace fold agreeing on what counts
// as whitespace.
var baseSubjectPrefixRe = regexp.MustCompile(`(?i)^\s*(?:re|res|aw|fwd?|wg)\s*:\s*`)

// foldWhitespace collapses every run of Unicode whitespace (as
// unicode.IsSpace defines it — including vertical tab, form feed, and
// NBSP, not just the ASCII set RE2's \s matches) to a single space and
// trims the ends, matching what strings.Fields considers whitespace.
func foldWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// NormalizeBaseSubject derives the base subject used to decide whether
// a message threads onto an ancestor (issue #437, REQ-STORE-40): every
// leading reply/forward marker is stripped, repeatedly so stacked
// markers such as "Re: Fwd: Topic" are all removed, then the result is
// case- and whitespace-folded (runs of whitespace collapse to a single
// space, case is lowered) so two subjects that differ only in a marker,
// spacing, or letter case compare equal.
//
// Whitespace is folded before the prefix is stripped (and again after
// each strip) so the regex only ever sees plain ASCII spaces; this
// keeps the function idempotent regardless of which Unicode whitespace
// character separates the prefix word from the colon or surrounds the
// subject.
//
// This is the single place ingest (insertMessageTx) and bulk rethread
// (RethreadPrincipal) call to compare subjects; there is no fuzzy
// matching and no prefix-of relation — the folded strings must be
// exactly equal.
func NormalizeBaseSubject(subject string) string {
	s := foldWhitespace(subject)
	for {
		stripped := foldWhitespace(baseSubjectPrefixRe.ReplaceAllString(s, ""))
		if stripped == s {
			break
		}
		s = stripped
	}
	return strings.ToLower(s)
}

// SubjectsThreadTogether reports whether a message carrying childSubject
// may inherit the thread of an ancestor carrying ancestorSubject (issue
// #437). The two base subjects (see NormalizeBaseSubject) must match
// exactly; an empty base subject on either side is not a mismatch,
// since the sender supplied nothing to compare, so the message
// threads onto the ancestor as it always has.
func SubjectsThreadTogether(childSubject, ancestorSubject string) bool {
	child := NormalizeBaseSubject(childSubject)
	ancestor := NormalizeBaseSubject(ancestorSubject)
	if child == "" || ancestor == "" {
		return true
	}
	return child == ancestor
}
