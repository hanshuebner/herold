package protoadmin

// mlist.go — admin REST surface for hosted mailing lists (epic #183,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-40a,
// docs/design/server/architecture/14-mailing-lists.md). CRUD for the list
// row itself; roster CRUD and bulk import/export live in mlist_members.go.
//
// Endpoint shape (all behind authAdmin, then the finer per-domain/per-list
// check in mlist_authz.go):
//
//	GET    /api/v1/lists              list (domain-scoped for an operator)
//	POST   /api/v1/lists              create
//	GET    /api/v1/lists/{id}         show
//	PATCH  /api/v1/lists/{id}         rename / set config / reassign owner
//	DELETE /api/v1/lists/{id}         delete (cascades the roster)
//
// A list is a Group principal (REQ-MLIST-01) plus this REST-visible
// configuration. Creating a list here also creates its backing Group
// principal and writes the owner's list:owner grant (REQ-MLIST-05); the
// backing principal is left in place on delete, matching
// store.DeleteMailingList's documented "the caller owns that lifecycle
// decision separately".

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// ensureArchiveMailbox returns the id of groupPID's archive mailbox for
// postingAddress (REQ-MLIST-70), creating it if it does not already
// exist. Idempotent across a disable/re-enable cycle (PATCH
// archive_enabled=false only detaches ml.ArchiveMailboxID; it never
// deletes the mailbox or its content, see patchMailingListRequest's
// ArchiveEnabled doc comment) -- re-enabling reuses the SAME mailbox by
// its deterministic name (maillist.ArchiveMailboxNameForAddress) rather
// than colliding on the (principal, name) uniqueness constraint by
// trying to insert a second mailbox of the same name, and the list's
// prior archived history becomes visible again instead of being
// orphaned under a name the list config no longer references.
func (s *Server) ensureArchiveMailbox(ctx context.Context, groupPID store.PrincipalID, postingAddress string) (store.MailboxID, error) {
	name := maillist.ArchiveMailboxNameForAddress(postingAddress)
	existing, err := s.store.Meta().GetMailboxByName(ctx, groupPID, name)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	mb, err := s.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: groupPID,
		Name:        name,
		Attributes:  store.MailboxAttrArchive,
	})
	if err != nil {
		return 0, err
	}
	return mb.ID, nil
}

// parseMailingListID reads the {id} path parameter.
func parseMailingListID(w http.ResponseWriter, r *http.Request) (store.MailingListID, bool) {
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"list id must be a positive integer", raw)
		return 0, false
	}
	return store.MailingListID(n), true
}

// loadMailingList reads {id} and the list row, writing 400/404 on failure.
func (s *Server) loadMailingList(w http.ResponseWriter, r *http.Request) (store.MailingList, bool) {
	id, ok := parseMailingListID(w, r)
	if !ok {
		return store.MailingList{}, false
	}
	l, err := s.store.Meta().GetMailingList(r.Context(), id)
	if err != nil {
		s.writeMlistError(w, r, err)
		return store.MailingList{}, false
	}
	return l, true
}

// writeMlistError maps a mailing-list store error to an HTTP problem
// response, adding the ErrInvalidArgument case (malformed posting address,
// member XOR/nomail violations) on top of writeStoreError's set.
func (s *Server) writeMlistError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrInvalidArgument) {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	s.writeStoreError(w, r, err)
}

// requireActiveDKIMKeyForARCSeal enforces REQ-MLIST-21 at configuration
// time (issue #183): arc_seal may only be turned on, or a list relocated
// into a new domain while arc_seal stays on, when domain already has an
// active DKIM key. Without this gate, a freshly onboarded domain (the
// normal state before an operator has run `dkim generate`) silently loses
// ARC protection at fan-out time with only a log line as the signal — the
// defect this ticket fixes. Writes a 400 naming the problem and the
// remedy on failure; a store error other than ErrNotFound is surfaced as
// its own 500 via writeStoreError rather than misreported as "no key".
func (s *Server) requireActiveDKIMKeyForARCSeal(w http.ResponseWriter, r *http.Request, domain string) bool {
	_, err := s.store.Meta().GetActiveDKIMKey(r.Context(), domain)
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, r, http.StatusBadRequest, "dkim_key_required",
			fmt.Sprintf("domain %s has no active DKIM key", domain),
			fmt.Sprintf("arc_seal requires an active DKIM key on %s; provision one first "+
				"(POST /api/v1/domains/%s/dkim, or `herold dkim generate %s`), or set arc_seal=false", domain, domain, domain))
		return false
	}
	s.writeStoreError(w, r, err)
	return false
}

// dkimKeyMissingChecker answers "does domain currently lack an active DKIM
// key" for the mailingListDTO.DKIMKeyMissing field, caching one lookup per
// distinct domain so a collection page listing many lists on the same
// domain issues one active-key read per domain rather than one per row.
type dkimKeyMissingChecker struct {
	ctx   context.Context
	s     *Server
	cache map[string]bool
}

func (s *Server) newDKIMKeyMissingChecker(ctx context.Context) *dkimKeyMissingChecker {
	return &dkimKeyMissingChecker{ctx: ctx, s: s, cache: map[string]bool{}}
}

func (c *dkimKeyMissingChecker) missing(domain string) bool {
	if v, ok := c.cache[domain]; ok {
		return v
	}
	_, err := c.s.store.Meta().GetActiveDKIMKey(c.ctx, domain)
	m := err != nil
	c.cache[domain] = m
	return m
}

// dto converts l, populating DKIMKeyMissing when l.ARCSeal is set.
func (c *dkimKeyMissingChecker) dto(l store.MailingList) mailingListDTO {
	dto := toMailingListDTO(l)
	if l.ARCSeal {
		dto.DKIMKeyMissing = c.missing(l.Domain)
	}
	return dto
}

// handleListMailingLists handles GET /api/v1/lists (REQ-MLIST-40a).
//
// A super-admin with no ?domain= filter gets the store's native
// keyset-paginated scan over every list. Everyone else is restricted to
// the domain(s) they hold at least domain:operator on (REQ-MLIST-05): an
// explicit ?domain= must be one of those domains (403 otherwise), and an
// omitted ?domain= merges one native, AfterID-filtered page per
// authorized domain, sorts by ID, and truncates to the requested page
// size — correct because MailingListID is a single global sequence, so
// filtering AfterID per-domain and merging yields exactly the same
// candidate set as filtering the union post-hoc. A caller with an empty
// authorized-domain set gets an empty page, never an error (fail-closed).
func (s *Server) handleListMailingLists(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	q := r.URL.Query()
	domain := strings.ToLower(strings.TrimSpace(q.Get("domain")))
	after, limit, ok := parseAfterLimit(w, r, 0, 100, 1000)
	if !ok {
		return
	}

	scope := s.mlistAuthorizedDomains(r.Context(), caller)
	checker := s.newDKIMKeyMissingChecker(r.Context())

	if scope.SuperAdmin {
		rows, err := s.store.Meta().ListMailingLists(r.Context(), store.MailingListFilter{
			Domain: domain, AfterID: store.MailingListID(after), Limit: limit,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mailingListPage(rows, limit, checker))
		return
	}

	if domain != "" {
		if !domainInSet(scope.Domains, domain) {
			writeProblem(w, r, http.StatusForbidden, "forbidden",
				"insufficient privileges on this domain", "")
			return
		}
		rows, err := s.store.Meta().ListMailingLists(r.Context(), store.MailingListFilter{
			Domain: domain, AfterID: store.MailingListID(after), Limit: limit,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mailingListPage(rows, limit, checker))
		return
	}

	// No domain filter: merge one page per authorized domain (empty
	// Domains yields an empty merged result, never a store call).
	var merged []store.MailingList
	for _, d := range scope.Domains {
		rows, err := s.store.Meta().ListMailingLists(r.Context(), store.MailingListFilter{
			Domain: d, AfterID: store.MailingListID(after), Limit: limit,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		merged = append(merged, rows...)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	if len(merged) > limit {
		merged = merged[:limit]
	}
	writeJSON(w, http.StatusOK, mailingListPage(merged, limit, checker))
}

// mailingListPage converts rows to the pageDTO envelope. Next carries the
// last row's id when the page is full (len(rows) == limit), the same
// "maybe more" heuristic handleListPrincipals uses.
func mailingListPage(rows []store.MailingList, limit int, checker *dkimKeyMissingChecker) pageDTO[mailingListDTO] {
	items := make([]mailingListDTO, 0, len(rows))
	for _, l := range rows {
		items = append(items, checker.dto(l))
	}
	var next *string
	if len(rows) == limit && len(rows) > 0 {
		tok := strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
		next = &tok
	}
	return pageDTO[mailingListDTO]{Items: items, Next: next}
}

// parseAfterLimit parses the ?after= / ?limit= query params shared by
// every mailing-list collection endpoint. Returns ok=false after writing a
// 400 problem on a malformed value.
func parseAfterLimit(w http.ResponseWriter, r *http.Request, defaultAfter uint64, defaultLimit, maxLimit int) (after uint64, limit int, ok bool) {
	after = defaultAfter
	limit = defaultLimit
	q := r.URL.Query()
	if raw := q.Get("after"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"after is not a valid id", raw)
			return 0, 0, false
		}
		after = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return 0, 0, false
		}
		if n > maxLimit {
			n = maxLimit
		}
		limit = n
	}
	return after, limit, true
}

func domainInSet(domains []string, d string) bool {
	for _, x := range domains {
		if x == d {
			return true
		}
	}
	return false
}

// createMailingListRequest is the POST /api/v1/lists body.
type createMailingListRequest struct {
	PostingAddress string `json:"posting_address"`
	DisplayName    string `json:"display_name"`
	OwnerPrincipal uint64 `json:"owner_principal_id,omitempty"`
	SubjectTag     string `json:"subject_tag,omitempty"`
	ARCSeal        *bool  `json:"arc_seal,omitempty"`
	MaxMessageSize int64  `json:"max_message_size_bytes,omitempty"`
	// UnsubscribeEnabled gates REQ-MLIST-56/57/58 (epic #184). Defaults
	// to true (mirroring ARCSeal's own "default on" convention) unless
	// the caller explicitly sets it false.
	UnsubscribeEnabled *bool `json:"unsubscribe_enabled,omitempty"`
	// SubscribePolicy sets the Stage 3 self-subscription policy (epic
	// #185, REQ-MLIST-60): "closed" (the default), "request-approval",
	// or "open". Empty/omitted keeps the store default (closed).
	SubscribePolicy string `json:"subscribe_policy,omitempty"`
	// BouncePolicy overrides the REQ-MLIST-53 deployment-default bounce
	// scoring policy for this list. Any field left unset keeps the
	// deployment default for that field.
	BouncePolicy *bouncePolicyPatchDTO `json:"bounce_policy,omitempty"`
	// ArchiveEnabled creates the list's archive mailbox (Stage 4,
	// REQ-MLIST-70) at creation time when true. Omitted/false: no
	// archive (the S1..S3 default); use PATCH archive_enabled to add one
	// later.
	ArchiveEnabled bool `json:"archive_enabled,omitempty"`
	// ArchiveRetentionDays / ArchiveRetentionMaxMessages set the
	// REQ-MLIST-74 archive retention bound at creation time. Ignored
	// when ArchiveEnabled is false. 0 (the default) means unbounded.
	ArchiveRetentionDays        int64 `json:"archive_retention_days,omitempty"`
	ArchiveRetentionMaxMessages int64 `json:"archive_retention_max_messages,omitempty"`
}

// handleCreateMailingList handles POST /api/v1/lists (REQ-MLIST-01,
// REQ-MLIST-05). Creates the backing Group principal, the list row, and
// the owner's list:owner grant. owner_principal_id defaults to the
// caller.
func (s *Server) handleCreateMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createMailingListRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"display_name is required", "")
		return
	}
	address, domain, err := store.NormalizeMailingListAddress(req.PostingAddress)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	d, err := s.store.Meta().GetDomain(r.Context(), domain)
	if err != nil || !d.IsLocal {
		writeProblem(w, r, http.StatusBadRequest, "unknown_domain",
			"posting_address must be on a locally hosted domain", domain)
		return
	}
	if !s.requireMlistDomainAccess(w, r, caller, domain) {
		return
	}
	ownerID := caller.ID
	if req.OwnerPrincipal != 0 {
		owner, err := s.store.Meta().GetPrincipalByID(r.Context(), store.PrincipalID(req.OwnerPrincipal))
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"owner_principal_id does not reference a known principal", "")
			return
		}
		ownerID = owner.ID
	}
	arcSeal := true
	if req.ARCSeal != nil {
		arcSeal = *req.ARCSeal
	}
	if arcSeal && !s.requireActiveDKIMKeyForARCSeal(w, r, domain) {
		return
	}
	unsubscribeEnabled := true
	if req.UnsubscribeEnabled != nil {
		unsubscribeEnabled = *req.UnsubscribeEnabled
	}
	subscribePolicy, ok := mlistSubscribePolicyFromString(req.SubscribePolicy)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"subscribe_policy must be one of closed, request-approval, open", req.SubscribePolicy)
		return
	}

	if req.ArchiveRetentionDays < 0 || req.ArchiveRetentionMaxMessages < 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"archive_retention_days and archive_retention_max_messages must be non-negative", "")
		return
	}

	group, err := s.store.Meta().InsertPrincipal(r.Context(), store.Principal{
		Kind:           store.PrincipalKindGroup,
		CanonicalEmail: address,
		DisplayName:    req.DisplayName,
	})
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}

	var archiveMailboxID *store.MailboxID
	if req.ArchiveEnabled {
		id, err := s.ensureArchiveMailbox(r.Context(), group.ID, address)
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		archiveMailboxID = &id
	}

	var subjectTag *string
	if strings.TrimSpace(req.SubjectTag) != "" {
		tag := req.SubjectTag
		subjectTag = &tag
	}
	bouncePolicyJSON := "{}"
	if req.BouncePolicy != nil {
		var verr string
		bouncePolicyJSON, verr = applyBouncePolicyPatch("{}", *req.BouncePolicy)
		if verr != "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", verr, "")
			return
		}
	}
	l, err := s.store.Meta().InsertMailingList(r.Context(), store.MailingList{
		PrincipalID:                 group.ID,
		PostingAddress:              address,
		DisplayName:                 req.DisplayName,
		OwnerID:                     ownerID,
		SubjectTag:                  subjectTag,
		ARCSeal:                     arcSeal,
		MaxMessageSizeBytes:         req.MaxMessageSize,
		UnsubscribeEnabled:          unsubscribeEnabled,
		SubscribePolicy:             subscribePolicy,
		BouncePolicyJSON:            bouncePolicyJSON,
		ArchiveMailboxID:            archiveMailboxID,
		ArchiveRetentionDays:        req.ArchiveRetentionDays,
		ArchiveRetentionMaxMessages: req.ArchiveRetentionMaxMessages,
	})
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	if err := s.grantListOwner(r.Context(), l, ownerID, caller.ID); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mlist.create",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"posting_address": l.PostingAddress,
			"owner_id":        strconv.FormatUint(uint64(ownerID), 10),
		})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/lists/%d", l.ID))
	writeJSON(w, http.StatusCreated, s.newDKIMKeyMissingChecker(r.Context()).dto(l))
}

// handleGetMailingList handles GET /api/v1/lists/{id}.
func (s *Server) handleGetMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return
	}
	writeJSON(w, http.StatusOK, s.newDKIMKeyMissingChecker(r.Context()).dto(l))
}

// patchMailingListRequest is the PATCH /api/v1/lists/{id} body. Every
// field is optional; only fields present are applied. subject_tag uses ""
// to mean "clear the tag" (a tag is never meaningfully the empty string).
type patchMailingListRequest struct {
	PostingAddress      *string `json:"posting_address,omitempty"`
	DisplayName         *string `json:"display_name,omitempty"`
	OwnerPrincipal      *uint64 `json:"owner_principal_id,omitempty"`
	SubjectTag          *string `json:"subject_tag,omitempty"`
	ARCSeal             *bool   `json:"arc_seal,omitempty"`
	MaxMessageSizeBytes *int64  `json:"max_message_size_bytes,omitempty"`
	// UnsubscribeEnabled gates REQ-MLIST-56/57/58 (epic #184).
	UnsubscribeEnabled *bool `json:"unsubscribe_enabled,omitempty"`
	// SubscribePolicy sets the Stage 3 self-subscription policy (epic
	// #185, REQ-MLIST-60): "closed", "request-approval", or "open".
	SubscribePolicy *string `json:"subscribe_policy,omitempty"`
	// BouncePolicy, when present, patches the REQ-MLIST-53 per-list
	// bounce-scoring policy; any of its own fields left unset keeps that
	// field's current (or default) value.
	BouncePolicy *bouncePolicyPatchDTO `json:"bounce_policy,omitempty"`
	// ArchiveEnabled, when present, creates the archive mailbox (true,
	// idempotent if already enabled) or detaches and revokes every
	// member's read grant on it (false, idempotent if already disabled;
	// the mailbox and its content are left in place -- REQ-MLIST-70/72).
	ArchiveEnabled *bool `json:"archive_enabled,omitempty"`
	// ArchiveRetentionDays / ArchiveRetentionMaxMessages patch the
	// REQ-MLIST-74 archive retention bound. 0 means unbounded.
	ArchiveRetentionDays        *int64 `json:"archive_retention_days,omitempty"`
	ArchiveRetentionMaxMessages *int64 `json:"archive_retention_max_messages,omitempty"`
}

// handlePatchMailingList handles PATCH /api/v1/lists/{id}: rename (change
// posting_address), reassign owner, or update config fields. The CLI's
// `list rename` and `list set` both map onto this one endpoint (`rename`
// sends only posting_address, `set` sends the rest), the same way
// `principal disable/enable/quota/grant-admin` all map onto one PATCH.
func (s *Server) handlePatchMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return
	}
	var req patchMailingListRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	oldOwner := l.OwnerID
	newOwner := l.OwnerID
	// finalDomain/relocating track whether this PATCH moves the list to a
	// new domain, so the arc_seal-requires-a-key gate below (REQ-MLIST-21,
	// issue #183) can check the DESTINATION domain rather than l.Domain,
	// which the store recomputes from PostingAddress only on Update.
	finalDomain := l.Domain
	relocating := false

	if req.PostingAddress != nil {
		address, domain, err := store.NormalizeMailingListAddress(*req.PostingAddress)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		if domain != l.Domain {
			// Moving the list to a different domain: the caller must be
			// authorized on the DESTINATION domain too, or a domain
			// operator could relocate a list into a domain they do not
			// manage and keep managing it there.
			d, derr := s.store.Meta().GetDomain(r.Context(), domain)
			if derr != nil || !d.IsLocal {
				writeProblem(w, r, http.StatusBadRequest, "unknown_domain",
					"posting_address must be on a locally hosted domain", domain)
				return
			}
			if !s.requireMlistDomainAccess(w, r, caller, domain) {
				return
			}
			finalDomain = domain
			relocating = true
		}
		l.PostingAddress = address
	}
	if req.DisplayName != nil {
		if strings.TrimSpace(*req.DisplayName) == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"display_name must not be empty", "")
			return
		}
		l.DisplayName = *req.DisplayName
	}
	if req.OwnerPrincipal != nil {
		owner, err := s.store.Meta().GetPrincipalByID(r.Context(), store.PrincipalID(*req.OwnerPrincipal))
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"owner_principal_id does not reference a known principal", "")
			return
		}
		newOwner = owner.ID
		l.OwnerID = newOwner
	}
	if req.SubjectTag != nil {
		if *req.SubjectTag == "" {
			l.SubjectTag = nil
		} else {
			tag := *req.SubjectTag
			l.SubjectTag = &tag
		}
	}
	if req.ARCSeal != nil {
		l.ARCSeal = *req.ARCSeal
	}
	if req.UnsubscribeEnabled != nil {
		l.UnsubscribeEnabled = *req.UnsubscribeEnabled
	}
	if req.SubscribePolicy != nil {
		policy, ok := mlistSubscribePolicyFromString(*req.SubscribePolicy)
		if !ok || policy == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"subscribe_policy must be one of closed, request-approval, open", *req.SubscribePolicy)
			return
		}
		l.SubscribePolicy = policy
	}
	if req.MaxMessageSizeBytes != nil {
		if *req.MaxMessageSizeBytes < 0 {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"max_message_size_bytes must be non-negative", "")
			return
		}
		l.MaxMessageSizeBytes = *req.MaxMessageSizeBytes
	}
	if req.BouncePolicy != nil {
		newRaw, verr := applyBouncePolicyPatch(l.BouncePolicyJSON, *req.BouncePolicy)
		if verr != "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", verr, "")
			return
		}
		l.BouncePolicyJSON = newRaw
	}
	if req.ArchiveRetentionDays != nil {
		if *req.ArchiveRetentionDays < 0 {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"archive_retention_days must be non-negative", "")
			return
		}
		l.ArchiveRetentionDays = *req.ArchiveRetentionDays
	}
	if req.ArchiveRetentionMaxMessages != nil {
		if *req.ArchiveRetentionMaxMessages < 0 {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"archive_retention_max_messages must be non-negative", "")
			return
		}
		l.ArchiveRetentionMaxMessages = *req.ArchiveRetentionMaxMessages
	}
	// REQ-MLIST-70/72: archive_enabled create/detach. Grant maintenance
	// (GrantExistingActiveMembersArchiveAccess / RevokeAllArchiveGrants)
	// runs AFTER UpdateMailingList commits below, once l.ArchiveMailboxID
	// itself is durable.
	var archiveJustEnabled bool
	var archiveJustDisabledID *store.MailboxID
	if req.ArchiveEnabled != nil {
		switch {
		case *req.ArchiveEnabled && l.ArchiveMailboxID == nil:
			id, err := s.ensureArchiveMailbox(r.Context(), l.PrincipalID, l.PostingAddress)
			if err != nil {
				s.writeMlistError(w, r, err)
				return
			}
			l.ArchiveMailboxID = &id
			archiveJustEnabled = true
		case !*req.ArchiveEnabled && l.ArchiveMailboxID != nil:
			id := *l.ArchiveMailboxID
			archiveJustDisabledID = &id
			l.ArchiveMailboxID = nil
		}
	}

	// REQ-MLIST-21 config-time gate (issue #183): only checked when this
	// PATCH is the thing turning arc_seal on, or relocating an
	// already-arc_seal-enabled list into a new domain -- not on every
	// unrelated PATCH to a list whose domain lost its key sometime after
	// arc_seal was already enabled (that degradation is handled at
	// fan-out time, see internal/maillist/expand.go).
	if l.ARCSeal && (req.ARCSeal != nil || relocating) {
		if !s.requireActiveDKIMKeyForARCSeal(w, r, finalDomain) {
			return
		}
	}

	if err := s.store.Meta().UpdateMailingList(r.Context(), l); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	if archiveJustEnabled {
		// Existing active members get read access retroactively -- they
		// do not have to be re-added to gain it (REQ-MLIST-72).
		if err := maillist.GrantExistingActiveMembersArchiveAccess(r.Context(), s.store.Meta(), l); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
	}
	if archiveJustDisabledID != nil {
		// The mailbox and its already-archived content are left in
		// place; only read access is revoked (see patchMailingListRequest's
		// ArchiveEnabled doc comment).
		if err := maillist.RevokeAllArchiveGrants(r.Context(), s.store.Meta(), *archiveJustDisabledID); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
	}
	if newOwner != oldOwner {
		if err := s.revokeListOwner(r.Context(), l, oldOwner); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		if err := s.grantListOwner(r.Context(), l, newOwner, caller.ID); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
	}
	updated, err := s.store.Meta().GetMailingList(r.Context(), l.ID)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mlist.update",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"posting_address": updated.PostingAddress,
			"owner_id":        strconv.FormatUint(uint64(updated.OwnerID), 10),
		})
	writeJSON(w, http.StatusOK, s.newDKIMKeyMissingChecker(r.Context()).dto(updated))
}

// handleDeleteMailingList handles DELETE /api/v1/lists/{id}. Cascades the
// roster (store.DeleteMailingList) and best-effort removes any grants
// scoped to the list resource; the backing Group principal is left in
// place (store's documented "the caller owns that lifecycle decision
// separately").
func (s *Server) handleDeleteMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return
	}
	if err := s.store.Meta().DeleteMailingList(r.Context(), l.ID); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	grants, err := s.store.Meta().ListGrantsOnResource(r.Context(), store.GrantResourceList, mlistResourceID(l.ID))
	if err == nil {
		for _, g := range grants {
			_ = s.store.Meta().DeleteGrant(r.Context(), g)
		}
	}
	s.appendAudit(r.Context(), "mlist.delete",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{"posting_address": l.PostingAddress})
	w.WriteHeader(http.StatusNoContent)
}
