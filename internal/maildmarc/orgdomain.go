package maildmarc

import "strings"

// OrganizationalDomain returns the Organizational Domain of name per
// RFC 7489 §3.2, using a compact in-house heuristic: the effective TLD
// is the last public-suffix label, and the Organizational Domain is the
// immediate left-neighbour label plus the effective TLD.
//
// The Public Suffix List is out of Phase 1 scope; the helper covers
// single-label TLDs (e.g. "com", "net", "org") plus the most common
// two-label country TLDs ("co.uk", "com.au", "co.jp", ...) so that the
// canonical test vectors in RFC 7489 and the PSL "commonly used"
// examples resolve correctly. Domains outside this set fall back to the
// "last-two-labels" heuristic, which matches classic gTLDs correctly.
func OrganizationalDomain(name string) string {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" {
		return ""
	}
	labels := strings.Split(name, ".")
	if len(labels) <= 1 {
		return name
	}
	// If the last two labels together form a known multi-label TLD,
	// take the last three labels; otherwise take the last two.
	n := 2
	if len(labels) >= 2 {
		tail2 := labels[len(labels)-2] + "." + labels[len(labels)-1]
		if _, ok := multiLabelTLDs[tail2]; ok && len(labels) >= 3 {
			n = 3
		}
	}
	if n > len(labels) {
		n = len(labels)
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

// multiLabelTLDs is a small, hand-maintained set of two-label public
// suffixes that DMARC evaluation commonly encounters. It is not meant
// to be complete: the Public Suffix List is fetched and applied in a
// dedicated helper that Phase 2 introduces.
var multiLabelTLDs = map[string]struct{}{
	"co.uk":  {},
	"org.uk": {},
	"gov.uk": {},
	"ac.uk":  {},
	"me.uk":  {},
	"com.au": {},
	"net.au": {},
	"org.au": {},
	"co.jp":  {},
	"ne.jp":  {},
	"co.nz":  {},
	"co.za":  {},
	"com.br": {},
	"com.mx": {},
	"co.in":  {},
	"com.sg": {},
	"com.tw": {},
	"co.kr":  {},
	"com.cn": {},
	"co.il":  {},
}
