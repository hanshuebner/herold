package store

import (
	"sort"
	"strings"
)

// SortPrincipalSearchResults orders principals for Principal/query
// textPrefix results: principals whose DisplayName contains lowerPrefix
// (case-insensitive) come first; those matched only on the email
// local-part come second. Within each group, entries are sorted
// alphabetically by DisplayName then CanonicalEmail. The result is
// trimmed to at most limit entries.
//
// lowerPrefix must already be lower-cased; no additional folding is
// applied inside this function.
//
// This helper is shared by both the SQLite and Postgres backends so the
// sort semantics are byte-for-byte identical across CI lanes.
func SortPrincipalSearchResults(principals []Principal, lowerPrefix string, limit int) []Principal {
	type ranked struct {
		p    Principal
		prio int // 0 = display-name match, 1 = email-local-part match
	}
	ranked2 := make([]ranked, 0, len(principals))
	seen := make(map[PrincipalID]struct{}, len(principals))
	for _, p := range principals {
		if _, dup := seen[p.ID]; dup {
			continue
		}
		seen[p.ID] = struct{}{}
		nameMatch := strings.Contains(strings.ToLower(p.DisplayName), lowerPrefix)
		localPart := emailLocalPart(p.CanonicalEmail)
		emailMatch := strings.HasPrefix(strings.ToLower(localPart), lowerPrefix)
		if !nameMatch && !emailMatch {
			continue
		}
		prio := 1
		if nameMatch {
			prio = 0
		}
		ranked2 = append(ranked2, ranked{p: p, prio: prio})
	}
	sort.SliceStable(ranked2, func(i, j int) bool {
		ri, rj := ranked2[i], ranked2[j]
		if ri.prio != rj.prio {
			return ri.prio < rj.prio
		}
		di := strings.ToLower(ri.p.DisplayName)
		dj := strings.ToLower(rj.p.DisplayName)
		if di != dj {
			return di < dj
		}
		return strings.ToLower(ri.p.CanonicalEmail) < strings.ToLower(rj.p.CanonicalEmail)
	})
	out := make([]Principal, 0, min(limit, len(ranked2)))
	for i, r := range ranked2 {
		if i >= limit {
			break
		}
		out = append(out, r.p)
	}
	return out
}

// emailLocalPart returns the part of an email address before '@', or
// the full string when no '@' is present.
func emailLocalPart(email string) string {
	if idx := strings.IndexByte(email, '@'); idx >= 0 {
		return email[:idx]
	}
	return email
}
