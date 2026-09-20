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
var baseSubjectPrefixRe = regexp.MustCompile(`(?i)^\s*(?:re|res|aw|fwd?|wg)\s*:\s*`)

// NormalizeBaseSubject derives the base subject used to decide whether
// a message threads onto an ancestor (issue #437, REQ-STORE-40): every
// leading reply/forward marker is stripped, repeatedly so stacked
// markers such as "Re: Fwd: Topic" are all removed, then the result is
// case- and whitespace-folded (runs of whitespace collapse to a single
// space, case is lowered) so two subjects that differ only in a marker,
// spacing, or letter case compare equal.
//
// This is the single place ingest (insertMessageTx) and bulk rethread
// (RethreadPrincipal) call to compare subjects; there is no fuzzy
// matching and no prefix-of relation — the folded strings must be
// exactly equal.
func NormalizeBaseSubject(subject string) string {
	s := subject
	for {
		stripped := baseSubjectPrefixRe.ReplaceAllString(s, "")
		if stripped == s {
			break
		}
		s = stripped
	}
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
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
