package protoadmin

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/store"
)

// Hosted mailing lists, Stage 1 admin REST authorization (epic #183,
// REQ-MLIST-05, REQ-MLIST-40a). Every handler in mlist.go / mlist_members.go
// is gated by requireAdmin (the same outer admin-flag gate every other
// protoadmin surface uses) and THEN by one of the checks below, which
// narrow access to the caller's domain/list authority through the unified
// grant model (internal/authz, epic #182) rather than inventing a parallel
// mechanism.
//
// Resolution is fail-closed throughout: authz.Resolve treats a store error
// as "no access" (see internal/authz.Resolve's doc comment), and every
// helper here returns false rather than true on ambiguity.

// mlistDomainAccess reports whether caller may administer mailing lists on
// domain: a super-admin, or a principal holding domain:operator or above on
// domain (REQ-MLIST-05's "domain-scoped operator manages lists only on the
// domains they own").
func (s *Server) mlistDomainAccess(ctx context.Context, caller store.Principal, domain string) bool {
	lvl, err := authz.Resolve(ctx, s.store.Meta(), caller, authz.Resource{Kind: store.GrantResourceDomain, ID: domain})
	if err != nil {
		return false
	}
	return authz.AtLeast(store.GrantResourceDomain, lvl, store.GrantLevelOperator)
}

// mlistListAccess reports whether caller may administer list l: domain
// access on l.Domain (mlistDomainAccess) OR a list:owner grant on l.ID
// (REQ-MLIST-05's "a list:owner grant governs that one list"). A bare
// list:moderator grant (reserved for the later moderation milestone,
// REQ-MLIST-80) is NOT sufficient for the S1 admin CRUD surface.
func (s *Server) mlistListAccess(ctx context.Context, caller store.Principal, l store.MailingList) bool {
	if s.mlistDomainAccess(ctx, caller, l.Domain) {
		return true
	}
	lvl, err := authz.Resolve(ctx, s.store.Meta(), caller,
		authz.Resource{Kind: store.GrantResourceList, ID: mlistResourceID(l.ID)})
	if err != nil {
		return false
	}
	return authz.AtLeast(store.GrantResourceList, lvl, store.GrantLevelOwner)
}

// requireMlistDomainAccess writes 403 and returns false when caller may not
// administer lists on domain. Callers MUST have already passed requireAdmin.
func (s *Server) requireMlistDomainAccess(w http.ResponseWriter, r *http.Request, caller store.Principal, domain string) bool {
	if s.mlistDomainAccess(r.Context(), caller, domain) {
		return true
	}
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"insufficient privileges on this domain", "")
	return false
}

// requireMlistListAccess writes 403 and returns false when caller may not
// administer list l. Callers MUST have already passed requireAdmin.
func (s *Server) requireMlistListAccess(w http.ResponseWriter, r *http.Request, caller store.Principal, l store.MailingList) bool {
	if s.mlistListAccess(r.Context(), caller, l) {
		return true
	}
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"insufficient privileges on this list", "")
	return false
}

// mlistResourceID formats a MailingListID as the grant table's ResourceID
// for the "list" resource kind (a string column).
func mlistResourceID(id store.MailingListID) string {
	return strconv.FormatUint(uint64(id), 10)
}

// mlistAuthorizedDomains returns the domains caller may list/create
// mailing lists on: every domain via ResolveOperatorScope for a
// domain-scoped operator, or nil (meaning "unrestricted") for a
// super-admin. The returned slice is non-nil and possibly empty for a
// non-super-admin caller (fail-closed, matching OperatorScope's contract).
func (s *Server) mlistAuthorizedDomains(ctx context.Context, caller store.Principal) OperatorScope {
	return ResolveOperatorScope(ctx, s.store.Meta(), caller)
}

// grantListOwner writes (or idempotently confirms) a list:owner grant for
// ownerID on list l, attributing the grant to actorID (REQ-MLIST-05: "a
// list:owner grant governs that one list").
func (s *Server) grantListOwner(ctx context.Context, l store.MailingList, ownerID, actorID store.PrincipalID) error {
	granter := actorID
	_, err := s.store.Meta().InsertGrant(ctx, store.Grant{
		SubjectKind:  store.GrantSubjectPrincipal,
		SubjectID:    uint64(ownerID),
		ResourceKind: store.GrantResourceList,
		ResourceID:   mlistResourceID(l.ID),
		Level:        store.GrantLevelOwner,
		Provenance:   store.GrantProvenanceLocal,
		GrantedBy:    &granter,
	})
	return err
}

// revokeListOwner deletes the local list:owner-scope grant (of whatever
// level) held by ownerID on list l. Absent rows are not an error: the
// caller may be transferring ownership away from a principal whose
// authority on the list came only from domain scope.
func (s *Server) revokeListOwner(ctx context.Context, l store.MailingList, ownerID store.PrincipalID) error {
	err := s.store.Meta().DeleteGrant(ctx, store.Grant{
		SubjectKind:  store.GrantSubjectPrincipal,
		SubjectID:    uint64(ownerID),
		ResourceKind: store.GrantResourceList,
		ResourceID:   mlistResourceID(l.ID),
		Provenance:   store.GrantProvenanceLocal,
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}
