package authz

import (
	"context"
	"sort"

	"github.com/hanshuebner/herold/internal/store"
)

// Resource names a typed resource a grant or a check is scoped to. ID is the
// domain name / list id / mailbox id; it is "" for the server resource.
type Resource struct {
	Kind store.GrantResourceKind
	ID   string
}

// grantReader is the slice of store.Metadata authz depends on. Taking an
// interface keeps Resolve testable without a full store.
type grantReader interface {
	ListGrantsForPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.Grant, error)
}

// rank is the total order of levels within a resource kind (REQ-AC-03). A
// higher rank implies every lower level on the same resource. The zero level
// ("" = no access) ranks 0 in every kind. Ranks are only comparable within one
// kind.
func rank(kind store.GrantResourceKind, level store.GrantLevel) int {
	switch kind {
	case store.GrantResourceServer:
		if level == store.GrantLevelSuperadmin {
			return 1
		}
	case store.GrantResourceDomain:
		switch level {
		case store.GrantLevelOperator:
			return 1
		case store.GrantLevelOwner:
			return 2
		}
	case store.GrantResourceList:
		switch level {
		case store.GrantLevelModerator:
			return 1
		case store.GrantLevelOwner:
			return 2
		}
	case store.GrantResourceMailbox:
		switch level {
		case store.GrantLevelRead:
			return 1
		case store.GrantLevelWrite:
			return 2
		case store.GrantLevelAdmin:
			return 3
		}
	}
	return 0
}

// topLevel is the highest level in a resource kind — what a server:superadmin
// resolves to on any resource (REQ-AC-04).
func topLevel(kind store.GrantResourceKind) store.GrantLevel {
	switch kind {
	case store.GrantResourceServer:
		return store.GrantLevelSuperadmin
	case store.GrantResourceDomain:
		return store.GrantLevelOwner
	case store.GrantResourceList:
		return store.GrantLevelOwner
	case store.GrantResourceMailbox:
		return store.GrantLevelAdmin
	}
	return ""
}

// DelegableLevel is the level a principal must hold on a resource of kind
// to have authority to delegate access on it to someone else (REQ-AC-05:
// "you may grant on a resource only at or below a level you hold with
// authority to delegate (owner/superadmin)"; REQ-AC-68's claim-mapping-rule
// author re-validation uses the same bar). It is topLevel exported for
// callers outside this package (e.g. internal/protoadmin's claim-mapping
// rule CRUD) that need to check "does the caller own/superadmin this
// resource" without duplicating the per-kind level table.
func DelegableLevel(kind store.GrantResourceKind) store.GrantLevel {
	return topLevel(kind)
}

// higher returns whichever of a or b ranks higher within kind.
func higher(kind store.GrantResourceKind, a, b store.GrantLevel) store.GrantLevel {
	if rank(kind, b) > rank(kind, a) {
		return b
	}
	return a
}

// AtLeast reports whether got meets or exceeds required within kind. The zero
// required level is never satisfied (default-deny: an operation must name a
// non-zero required level).
func AtLeast(kind store.GrantResourceKind, got, required store.GrantLevel) bool {
	r := rank(kind, required)
	return r > 0 && rank(kind, got) >= r
}

// ValidLevel reports whether level is one of kind's known per-kind levels
// (the "small fixed set of access levels per resource kind" of the REQ-AC
// intro). The zero level and any unrecognised string are invalid for every
// kind. Used by admin surfaces (e.g. claim-mapping rule CRUD) to reject a
// malformed level at the API boundary rather than storing an inert rule.
func ValidLevel(kind store.GrantResourceKind, level store.GrantLevel) bool {
	return rank(kind, level) > 0
}

// flagSuperAdmin reports whether p carries the super-admin flag. Migration
// 0079 back-fills a server:superadmin grant for exactly these principals, and
// the flag is always set alongside PrincipalFlagAdmin, so the flag is an
// authoritative superadmin signal that costs no store read.
func flagSuperAdmin(p store.Principal) bool {
	return p.Flags.Has(store.PrincipalFlagAdmin) && p.Flags.Has(store.PrincipalFlagSuperAdmin)
}

// Resolve returns the acting principal's effective level on res, computed as
// the max over the superadmin short-circuit and explicit grant rows on the
// resource. The returned level is "" (no access) when the principal holds
// nothing on res. Callers gate with AtLeast.
//
// Resolution is fail-closed: a store error is returned to the caller, which
// MUST treat it as a deny (REQ-AC-12) — it never yields access.
func Resolve(ctx context.Context, meta grantReader, p store.Principal, res Resource) (store.GrantLevel, error) {
	// Flag-based superadmin short-circuits without a store read, so a
	// superadmin is never denied by a transient store outage (preserving the
	// pre-grant behavior where superadmin was a pure flag check).
	if flagSuperAdmin(p) {
		return topLevel(res.Kind), nil
	}
	grants, err := meta.ListGrantsForPrincipal(ctx, p.ID)
	if err != nil {
		return "", err
	}
	// Grant-based superadmin (forward-compatibility). Today the migration
	// implies the flag, so this is reached only for principals without it.
	for _, g := range grants {
		if g.ResourceKind == store.GrantResourceServer && g.Level == store.GrantLevelSuperadmin {
			return topLevel(res.Kind), nil
		}
	}
	best := store.GrantLevel("")
	for _, g := range grants {
		if g.ResourceKind == res.Kind && g.ResourceID == res.ID {
			best = higher(res.Kind, best, g.Level)
		}
	}
	return best, nil
}

// OperatorDomains returns, sorted and de-duplicated, every domain on which p
// holds at least domain:operator, from p's domain grant rows. It is the set
// form of Resolve used by the domain-scoped observability filter; callers
// that need to know whether p is a superadmin (and therefore unrestricted)
// check that separately with Resolve on the server resource. Fail-closed: a
// store error is returned and MUST be treated as the empty set.
func OperatorDomains(ctx context.Context, meta grantReader, p store.Principal) ([]string, error) {
	grants, err := meta.ListGrantsForPrincipal(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, g := range grants {
		if g.ResourceKind == store.GrantResourceDomain &&
			rank(store.GrantResourceDomain, g.Level) >= rank(store.GrantResourceDomain, store.GrantLevelOperator) {
			set[g.ResourceID] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}
