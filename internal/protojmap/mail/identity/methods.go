package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// getRequest is the inbound shape of Identity/get (RFC 8620 §5.1).
type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties []string  `json:"properties,omitempty"`
}

// getResponse is the response shape (RFC 8620 §5.1).
type getResponse struct {
	AccountID string         `json:"accountId"`
	State     string         `json:"state"`
	List      []jmapIdentity `json:"list"`
	NotFound  []jmapID       `json:"notFound"`
}

// changesRequest mirrors the RFC 8620 §5.2 envelope.
type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
}

// changesResponse is the RFC 8620 §5.2 response.
type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

// setRequest is the RFC 8620 §5.3 inbound envelope. Identity has no
// destroyable default; destroys for the "default" id are rejected with
// a SetError.
type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

// setResponse is the response envelope.
type setResponse struct {
	AccountID    string                   `json:"accountId"`
	OldState     string                   `json:"oldState,omitempty"`
	NewState     string                   `json:"newState"`
	Created      map[string]jmapIdentity  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapIdentity `json:"updated,omitempty"`
	Destroyed    []jmapID                 `json:"destroyed,omitempty"`
	NotCreated   map[string]setError      `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError      `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError      `json:"notDestroyed,omitempty"`
}

// setError is the per-key error envelope (RFC 8620 §5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// handlerSet bundles the methods for one Identity capability.
type handlerSet struct {
	store    store.Store
	identity *Store
	domains  func(ctx context.Context) (map[string]struct{}, error)
	logger   *slog.Logger
	// verificationTrigger fires after Identity/set { create } commits
	// (REQ-IDENT-30). May be nil; the create call then leaves the row
	// unverified without enqueuing an email.
	verificationTrigger VerificationTrigger
	// externalDomain reports whether the given external (non-hosted)
	// domain is permitted by operator policy (REQ-IDENT, the
	// external_domains knob). May be nil; the legacy hosted-only
	// behaviour applies then.
	externalDomain DomainPolicy
	// reg is the capability registry this handler set was installed
	// into. Used to gate Identity/set{separated} on the sub-accounts
	// capability being present (REQ-SUBACCT-11): tests that build a
	// bare handlerSet via test_helpers.go without a registry leave
	// this nil, and separated updates are then rejected as the
	// capability being absent, matching the "no registry configured
	// this feature" reading of REQ-SUBACCT-11.
	reg *protojmap.CapabilityRegistry
}

// makeDomainsFn returns a closure that lists the locally-hosted domains.
func makeDomainsFn(st store.Store) func(ctx context.Context) (map[string]struct{}, error) {
	return func(ctx context.Context) (map[string]struct{}, error) {
		ds, err := st.Meta().ListLocalDomains(ctx)
		if err != nil {
			return nil, fmt.Errorf("identity: list local domains: %w", err)
		}
		out := make(map[string]struct{}, len(ds))
		for _, d := range ds {
			out[d.Name] = struct{}{}
		}
		return out, nil
	}
}

// stateString stringifies the per-principal Identity state counter to
// the JMAP wire form.
func stateString(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// currentState returns the principal's current Identity state.
func (h *handlerSet) currentState(ctx context.Context, p store.Principal) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return "", err
	}
	return stateString(st.Identity), nil
}

// accountIDForPrincipal returns the canonical wire-form accountId for p.
func accountIDForPrincipal(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// resolveTargetPrincipal resolves the requested accountId to the
// principal whose Identity set the request addresses: the caller's own
// account, or one of the caller's own sub-accounts (REQ-SUBACCT-03/04).
// It never honours a mailbox-ACL grant (protojmap.ResolveOwnAccount,
// not ResolveAccount) -- sharing a mailbox never exposes another
// principal's Identities.
//
// Once an Identity has been separated (Identity/set{separated:true},
// REQ-SUBACCT-09) it is moved into the sub-principal's own
// jmap_identities rows, so it stops appearing under the parent's
// Identity/get and starts appearing under the sub-account's. A client
// that wants to see or manage a separated identity calls Identity/get
// or Identity/set with accountId set to that sub-account's id (as
// advertised in the JMAP session's `accounts` map, REQ-SUBACCT-03) --
// there is no separate "list separated identities" method.
func (h *handlerSet) resolveTargetPrincipal(ctx context.Context, caller store.Principal, requested jmapID) (store.Principal, *protojmap.MethodError) {
	pid, merr := protojmap.ResolveOwnAccount(ctx, h.store.Meta(), caller.ID, requested)
	if merr != nil {
		return store.Principal{}, merr
	}
	if pid == caller.ID {
		return caller, nil
	}
	target, err := h.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		return store.Principal{}, protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return target, nil
}

// -- Identity/get -----------------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "Identity/get" }

func (g getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	target, e := g.h.resolveTargetPrincipal(ctx, p, req.AccountID)
	if e != nil {
		return nil, e
	}
	state, err := g.h.currentState(ctx, target)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	all := g.h.identity.snapshot(ctx, target)
	resp := getResponse{
		AccountID: accountIDForPrincipal(target),
		State:     state,
		List:      []jmapIdentity{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		for _, rec := range all {
			resp.List = append(resp.List, rec.toJMAP())
		}
		return resp, nil
	}
	byID := make(map[uint64]identityRecord, len(all))
	for _, r := range all {
		byID[r.ID] = r
	}
	for _, id := range *req.IDs {
		v, ok := parseID(id)
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		rec, found := byID[v]
		if !found {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		resp.List = append(resp.List, rec.toJMAP())
	}
	return resp, nil
}

// -- Identity/changes -------------------------------------------------

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "Identity/changes" }

func (c changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req changesRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	target, e := c.h.resolveTargetPrincipal(ctx, p, req.AccountID)
	if e != nil {
		return nil, e
	}
	now, err := c.h.currentState(ctx, target)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.SinceState == now {
		return changesResponse{
			AccountID: accountIDForPrincipal(target),
			OldState:  req.SinceState,
			NewState:  now,
			Created:   []jmapID{},
			Updated:   []jmapID{},
			Destroyed: []jmapID{},
		}, nil
	}
	resp := changesResponse{
		AccountID: accountIDForPrincipal(target),
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	for _, rec := range c.h.identity.snapshot(ctx, target) {
		resp.Updated = append(resp.Updated, renderID(rec.ID))
	}
	return resp, nil
}

// -- Identity/set -----------------------------------------------------

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "Identity/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req setRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	target, e := s.h.resolveTargetPrincipal(ctx, p, req.AccountID)
	if e != nil {
		return nil, e
	}
	oldState, err := s.h.currentState(ctx, target)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: accountIDForPrincipal(target),
		OldState:  oldState,
	}
	mutated := false
	// targetStillExists tracks whether target's principal row survived
	// the update batch: a successful separated:false reversal always
	// deletes the sub-principal target addresses (see the trailing
	// state-bump comment below).
	targetStillExists := true
	// Process creates.
	for clientID, raw := range req.Create {
		var in struct {
			Name          string         `json:"name"`
			Email         string         `json:"email"`
			ReplyTo       []emailAddress `json:"replyTo,omitempty"`
			Bcc           []emailAddress `json:"bcc,omitempty"`
			TextSignature string         `json:"textSignature,omitempty"`
			HTMLSignature string         `json:"htmlSignature,omitempty"`
			Signature     *string        `json:"signature,omitempty"`
			// AvatarBlobId and XFaceEnabled are herold extensions (REQ-SET-03b).
			AvatarBlobId *string `json:"avatarBlobId,omitempty"`
			XFaceEnabled bool    `json:"xFaceEnabled,omitempty"`
			// SkipVerificationEmail suppresses the post-create verification
			// trigger for identities whose ownership will be proven via an
			// OAuth round-trip instead (e.g. Gmail, Microsoft 365). When
			// true, the verification trigger is not fired; the OAuth callback
			// calls MarkIdentityVerified on success (re #105).
			SkipVerificationEmail bool `json:"skipVerificationEmail,omitempty"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{Type: "invalidProperties", Description: err.Error()}
			continue
		}
		_, dom, ok := localPartAndDomain(in.Email)
		if !ok {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"email"},
				Description: "email must be a valid addr-spec",
			}
			continue
		}
		domains, derr := s.h.domains(ctx)
		if derr != nil {
			return nil, protojmap.NewMethodError("serverFail", derr.Error())
		}
		if _, hosted := domains[dom]; !hosted {
			// REQ-IDENT-12 / [server.identity_creation].external_domains:
			// hosted domains are always allowed; external domains follow
			// the operator's policy hook. A nil policy preserves the
			// legacy hosted-only behaviour.
			permitted := false
			if s.h.externalDomain != nil {
				permitted = s.h.externalDomain(dom)
			}
			if !permitted {
				if resp.NotCreated == nil {
					resp.NotCreated = make(map[string]setError)
				}
				resp.NotCreated[clientID] = setError{
					Type:        "forbiddenFrom",
					Description: "domain not permitted by server policy",
					Properties:  []string{"email"},
				}
				continue
			}
		}
		// Reject a create whose email duplicates an identity the
		// principal already owns (the synthesised default or any custom
		// row). Comparison is case-insensitive on the full addr-spec,
		// matching how the rest of the identity code lowercases the
		// domain; the local-part is preserved by senders but a
		// case-insensitive compare is the safe, user-facing choice.
		if dup := s.h.identity.hasIdentityWithEmail(ctx, target, in.Email); dup {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"email"},
				Description: "an identity with this email already exists",
			}
			continue
		}
		// Validate and resolve avatarBlobId if supplied.
		var avatarHash string
		var avatarSize int64
		if in.AvatarBlobId != nil && *in.AvatarBlobId != "" {
			hash, sz, serr := validateAvatarBlob(ctx, s.h.store, *in.AvatarBlobId)
			if serr != nil {
				if resp.NotCreated == nil {
					resp.NotCreated = make(map[string]setError)
				}
				resp.NotCreated[clientID] = *serr
				continue
			}
			avatarHash = hash
			avatarSize = sz
		}
		rec := identityRecord{
			Name:           in.Name,
			Email:          in.Email,
			ReplyTo:        in.ReplyTo,
			Bcc:            in.Bcc,
			TextSignature:  in.TextSignature,
			HTMLSignature:  in.HTMLSignature,
			AvatarBlobHash: avatarHash,
			AvatarBlobSize: avatarSize,
			XFaceEnabled:   in.XFaceEnabled,
			// REQ-IDENT-12: the row is committed in the unverified
			// state. VerifiedAt is the zero value, which the
			// recordToPersisted projection drops (the persisted
			// VerifiedAtUs stays 0) and toJMAP encodes as wire-form
			// JSON null. Verification flips this only via the email
			// callback (REQ-IDENT-40) or the admin CLI (REQ-IDENT-50).
		}
		if in.Signature != nil {
			v := *in.Signature
			rec.Signature = &v
		}
		created := s.h.identity.create(ctx, target, rec)
		// incRef the avatar blob after the row is committed.
		if avatarHash != "" {
			_ = s.h.store.Meta().IncRefBlob(ctx, avatarHash, avatarSize)
		}
		// REQ-IDENT-30: fire the verification-trigger hook so the
		// composer (task #14) can enqueue the verification email. The
		// trigger runs in this goroutine but MUST NOT block on SMTP;
		// failures are logged and we still report the create as
		// successful — the suite surfaces a Resend affordance
		// (REQ-IDENT-41) for users to retry.
		//
		// The trigger is skipped when skipVerificationEmail is true:
		// that flag signals that the client will immediately start an
		// OAuth flow which will call MarkIdentityVerified on success,
		// so the verification email is not needed (re #105).
		if s.h.verificationTrigger != nil && !in.SkipVerificationEmail {
			row := recordToPersisted(created)
			row.VerifiedAtUs = 0
			if err := s.h.verificationTrigger(ctx, row); err != nil {
				s.h.logger.Warn("identity verification trigger failed",
					slog.String("subsystem", "jmap-identity"),
					slog.String("identity_id", row.ID),
					slog.Uint64("principal_id", uint64(p.ID)),
					slog.String("err", err.Error()))
			}
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapIdentity)
		}
		resp.Created[clientID] = created.toJMAP()
		mutated = true
	}
	// Process updates.
	for id, raw := range req.Update {
		v, ok := parseID(id)
		if !ok {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		if hasSeparatedKey(raw) {
			result, serr := s.h.applySeparationUpdate(ctx, target, v, raw)
			if serr != nil {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = *serr
				continue
			}
			if result.targetDeleted {
				targetStillExists = false
			}
			if result.destroyed {
				resp.Destroyed = append(resp.Destroyed, id)
			} else {
				if resp.Updated == nil {
					resp.Updated = make(map[jmapID]*jmapIdentity)
				}
				j := result.rec.toJMAP()
				resp.Updated[id] = &j
			}
			mutated = true
			continue
		}
		patch, perr := decodePatch(ctx, s.h.store, raw)
		if perr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = *perr
			continue
		}
		// Snapshot old avatar hash before applying so we can manage
		// refcounts after a successful update.
		oldAvatarHash := s.h.identity.snapshotAvatarHash(ctx, target, v)
		rec, ok := s.h.identity.update(ctx, target, v, patch)
		if !ok {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		// Manage refcounts: incRef new avatar first (never transiently
		// zero), then decRef old.
		if patch.hasAvatarBlobId {
			if rec.AvatarBlobHash != "" {
				_ = s.h.store.Meta().IncRefBlob(ctx, rec.AvatarBlobHash, rec.AvatarBlobSize)
			}
			if oldAvatarHash != "" {
				_ = s.h.store.Meta().DecRefBlob(ctx, oldAvatarHash)
			}
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapIdentity)
		}
		j := rec.toJMAP()
		resp.Updated[id] = &j
		mutated = true
	}
	// Process destroys.
	for _, id := range req.Destroy {
		v, ok := parseID(id)
		if !ok {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{Type: "notFound"}
			continue
		}
		if v == 0 {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{
				Type: "forbidden", Description: "default identity is not deletable"}
			continue
		}
		// Snapshot the avatar hash before destroying so we can decRef.
		oldAvatarHash := s.h.identity.snapshotAvatarHash(ctx, target, v)
		if !s.h.identity.destroy(ctx, target, v) {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{Type: "notFound"}
			continue
		}
		if oldAvatarHash != "" {
			_ = s.h.store.Meta().DecRefBlob(ctx, oldAvatarHash)
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
	}
	// Bump JMAP state on any mutation. A successful separated:false
	// reversal deletes the sub-principal addressed by target (issue
	// #227, REQ-SUBACCT-10: store.RemoveSubAccount always ends by
	// deleting the sub-principal, in both the keep and purge cases), so
	// bumping target.ID's own JMAPStates row would hit the row's
	// ON DELETE CASCADE FK and fail; bump the caller's own account
	// instead in that case, since that is where the mail (and, for
	// keepMail, the Identity) now lives.
	// effectivePID is target.ID, unless the request's own separated:false
	// deleted that principal -- then both the state bump and the
	// trailing currentState read must target a principal that still
	// exists (Metadata.GetJMAPStates lazily creates its row on read, so
	// reading a deleted principal's state would itself hit the same FK
	// as writing it).
	effectivePID := target.ID
	if !targetStillExists {
		effectivePID = p.ID
	}
	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, effectivePID,
			store.JMAPStateKindIdentity); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	newState, err := s.h.currentState(ctx, store.Principal{ID: effectivePID})
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = newState
	return resp, nil
}

// hasSeparatedKey reports whether raw (an Identity/set update object)
// carries a "separated" property. Used to route the update to
// applySeparationUpdate instead of the ordinary field-patch path
// (issue #227, REQ-SUBACCT-09/10).
func hasSeparatedKey(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m["separated"]
	return ok
}

// separationResult is the outcome of applySeparationUpdate: either the
// Identity survives (possibly having moved accountId, for
// separated:true) and is reported in Identity/set's "updated", or it
// was destroyed as a side effect (separated:false with keepMail:false)
// and is reported in "destroyed" instead. targetDeleted is true
// whenever the sub-principal addressed by the request's accountId no
// longer exists after this call (every successful separated:false
// reversal, keepMail:true or false alike -- store.RemoveSubAccount
// always ends by deleting the sub-principal); the caller uses it to
// avoid bumping a JMAPStates row that the delete's ON DELETE CASCADE
// already removed.
type separationResult struct {
	destroyed     bool
	targetDeleted bool
	rec           identityRecord
}

// applySeparationUpdate implements the "separated" control property on
// Identity/set update (issue #227, REQ-SUBACCT-09/10). It is not an
// ordinary persisted field: setting it drives store.SeparateIdentity /
// store.RunSubAccountMigration / store.RemoveSubAccount rather than a
// column write, so it is intercepted before decodePatch ever sees it.
//
//   - separated:true on a not-yet-separated identity calls
//     store.SeparateIdentity synchronously (so the identity has already
//     moved to its new accountId by the time this call returns) and
//     starts store.RunSubAccountMigration in a server-owned background
//     goroutine (REQ-SUBACCT-09: promotion is idempotent and
//     crash-safe, so a restart before the goroutine finishes resumes it
//     via the boot-time sweep, not this call). Calling it again while
//     already separated is a no-op success; calling it while a sweep is
//     still running is rejected.
//   - separated:false reverses a completed separation via
//     store.RemoveSubAccount, honouring an optional "keepMail" boolean
//     (default true) alongside "separated" in the same update object.
//     keepMail:true moves the mail back to the parent and reports the
//     Identity as updated; keepMail:false purges it and reports the
//     Identity as destroyed.
//
// internalID identifies the Identity within target (the JMAP account
// the request addressed, already resolved to the caller's own account
// or one of the caller's own sub-accounts by resolveTargetPrincipal --
// an ACL-shared foreign account can never reach this method at all).
// fullRaw is the complete update-object JSON for this id.
func (h *handlerSet) applySeparationUpdate(
	ctx context.Context,
	target store.Principal,
	internalID uint64,
	fullRaw json.RawMessage,
) (separationResult, *setError) {
	if !h.hasSubAccountsCapability() {
		return separationResult{}, &setError{
			Type:        "forbidden",
			Description: "the sub-accounts capability is not enabled on this session",
		}
	}
	var body struct {
		Separated *bool `json:"separated"`
		KeepMail  *bool `json:"keepMail"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fullRaw, &raw); err != nil {
		return separationResult{}, &setError{Type: "invalidProperties", Description: err.Error()}
	}
	for k := range raw {
		if k != "separated" && k != "keepMail" {
			return separationResult{}, &setError{
				Type:        "invalidProperties",
				Properties:  []string{k},
				Description: "separated cannot be combined with other property updates in the same call",
			}
		}
	}
	if err := json.Unmarshal(fullRaw, &body); err != nil || body.Separated == nil {
		return separationResult{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"separated"},
			Description: "separated must be a boolean",
		}
	}
	want := *body.Separated
	// keepMail defaults to true (REQ-SUBACCT-10: "keep" is the default --
	// the same default the REST removal flow uses for IMAP-import
	// accounts, REQ-IMAP-IMP-102).
	keepMail := true
	if body.KeepMail != nil {
		keepMail = *body.KeepMail
	}
	if body.KeepMail != nil && want {
		return separationResult{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"keepMail"},
			Description: "keepMail only applies to separated:false",
		}
	}

	if internalID == 0 {
		return separationResult{}, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"separated"},
			Description: "the default identity cannot be separated",
		}
	}
	rowID := strconv.FormatUint(internalID, 10)
	cur, err := h.store.Meta().GetJMAPIdentity(ctx, rowID)
	if err != nil || cur.PrincipalID != target.ID {
		return separationResult{}, &setError{Type: "notFound"}
	}

	mig, migErr := h.store.Meta().GetSubAccountMigrationByIdentity(ctx, rowID)
	switch {
	case migErr == nil && mig.Status != store.SubAccountMigrationStatusDone:
		return separationResult{}, &setError{
			Type:        "invalidProperties",
			Description: "a separation migration is already running for this identity",
		}
	case migErr == nil && want:
		// Already separated; idempotent no-op.
		rec := persistedToRecord(cur)
		h.identity.attachSeparation(ctx, &rec)
		return separationResult{rec: rec}, nil
	case migErr == nil && !want:
		if err := store.RemoveSubAccount(ctx, h.store, mig.SubPrincipalID, !keepMail); err != nil {
			return separationResult{}, &setError{Type: "serverFail", Description: err.Error()}
		}
		if !keepMail {
			return separationResult{destroyed: true, targetDeleted: true}, nil
		}
		row, err := h.store.Meta().GetJMAPIdentity(ctx, rowID)
		if err != nil {
			return separationResult{}, &setError{Type: "serverFail", Description: err.Error()}
		}
		rec := persistedToRecord(row)
		h.identity.attachSeparation(ctx, &rec)
		return separationResult{rec: rec, targetDeleted: true}, nil
	case !errors.Is(migErr, store.ErrNotFound):
		return separationResult{}, &setError{Type: "serverFail", Description: migErr.Error()}
	case !want:
		return separationResult{}, &setError{
			Type:        "invalidProperties",
			Description: "identity has not been separated",
		}
	}

	// Not yet separated, separated:true: promote synchronously (the
	// identity has moved accountId by the time this returns) and sweep
	// its mail in the background (REQ-SUBACCT-09).
	newMig, err := store.SeparateIdentity(ctx, h.store, target.ID, rowID)
	if err != nil {
		return separationResult{}, &setError{Type: "invalidProperties", Description: err.Error()}
	}
	st := h.store
	logger := h.logger
	migID := newMig.ID
	subPID := newMig.SubPrincipalID
	parentPID := newMig.ParentPrincipalID
	go func() {
		bgCtx := context.Background()
		if _, err := store.RunSubAccountMigration(bgCtx, st, migID); err != nil {
			logger.Warn("identity: sub-account migration sweep failed",
				slog.String("subsystem", "jmap-identity"),
				slog.String("migration_id", migID),
				slog.Uint64("sub_principal_id", uint64(subPID)),
				slog.String("err", err.Error()))
		}
		// Bump both accounts' Identity state so an EventSource listener
		// on either side observes the sweep's completion.
		if _, err := st.Meta().IncrementJMAPState(bgCtx, subPID, store.JMAPStateKindIdentity); err != nil {
			logger.Warn("identity: post-migration state bump failed (sub)",
				slog.String("subsystem", "jmap-identity"), slog.String("err", err.Error()))
		}
		if _, err := st.Meta().IncrementJMAPState(bgCtx, parentPID, store.JMAPStateKindIdentity); err != nil {
			logger.Warn("identity: post-migration state bump failed (parent)",
				slog.String("subsystem", "jmap-identity"), slog.String("err", err.Error()))
		}
	}()

	row, err := h.store.Meta().GetJMAPIdentity(ctx, rowID)
	if err != nil {
		return separationResult{}, &setError{Type: "serverFail", Description: err.Error()}
	}
	rec := persistedToRecord(row)
	h.identity.attachSeparation(ctx, &rec)
	return separationResult{rec: rec}, nil
}

// hasSubAccountsCapability reports whether the sub-accounts capability
// (REQ-SUBACCT-11) is registered on this handler set's capability
// registry. h.reg is nil for handler sets built without a registry
// (test_helpers.go's bare handlerSet, used by most of this package's
// own unit tests), which is treated as "capability absent" -- those
// tests do not exercise Identity/set{separated}.
func (h *handlerSet) hasSubAccountsCapability() bool {
	return h.reg != nil && h.reg.HasCapability(protojmap.CapabilitySubAccounts)
}

// decodePatch reads an Identity/set "update" object into the Store's
// patch shape, distinguishing missing fields from cleared ones.
// ctx and st are needed to validate avatarBlobId when present.
func decodePatch(ctx context.Context, st store.Store, raw json.RawMessage) (identityPatch, *setError) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return identityPatch{}, &setError{Type: "invalidProperties", Description: err.Error()}
	}
	var out identityPatch
	for k, v := range m {
		switch k {
		case "name":
			out.hasName = true
			if err := json.Unmarshal(v, &out.name); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("name: %v", err)}
			}
		case "replyTo":
			out.hasReplyTo = true
			if err := json.Unmarshal(v, &out.replyTo); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("replyTo: %v", err)}
			}
		case "bcc":
			out.hasBcc = true
			if err := json.Unmarshal(v, &out.bcc); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("bcc: %v", err)}
			}
		case "textSignature":
			out.hasTextSignature = true
			if err := json.Unmarshal(v, &out.textSignature); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("textSignature: %v", err)}
			}
		case "htmlSignature":
			out.hasHTMLSignature = true
			if err := json.Unmarshal(v, &out.htmlSignature); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("htmlSignature: %v", err)}
			}
		case "signature":
			out.hasSignature = true
			if string(v) == "null" {
				out.signature = nil
				continue
			}
			var sig string
			if err := json.Unmarshal(v, &sig); err != nil {
				return identityPatch{}, &setError{Type: "invalidProperties", Description: fmt.Sprintf("signature: %v", err)}
			}
			out.signature = &sig
		case "avatarBlobId":
			out.hasAvatarBlobId = true
			if string(v) == "null" {
				out.avatarBlobHash = ""
				out.avatarBlobSize = 0
				continue
			}
			var blobID string
			if err := json.Unmarshal(v, &blobID); err != nil {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"avatarBlobId"},
					Description: fmt.Sprintf("avatarBlobId: %v", err),
				}
			}
			if blobID == "" {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"avatarBlobId"},
					Description: "avatarBlobId must be a non-empty string or null",
				}
			}
			hash, sz, serr := validateAvatarBlob(ctx, st, blobID)
			if serr != nil {
				return identityPatch{}, serr
			}
			out.avatarBlobHash = hash
			out.avatarBlobSize = sz
		case "xFaceEnabled":
			out.hasXFaceEnabled = true
			if err := json.Unmarshal(v, &out.xFaceEnabled); err != nil {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"xFaceEnabled"},
					Description: fmt.Sprintf("xFaceEnabled: %v", err),
				}
			}
		case "isDefault":
			// REQ-IDENT-70: the herold Identity.isDefault extension.
			// The single-default invariant is enforced in the Store via
			// Metadata.SetDefaultJMAPIdentity.
			out.hasIsDefault = true
			if err := json.Unmarshal(v, &out.isDefault); err != nil {
				return identityPatch{}, &setError{
					Type:        "invalidProperties",
					Properties:  []string{"isDefault"},
					Description: fmt.Sprintf("isDefault: %v", err),
				}
			}
		case "email":
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: "email is immutable",
				Properties:  []string{"email"},
			}
		case "verifiedAt":
			// REQ-IDENT-13: clients cannot toggle verifiedAt. The
			// transition only happens via the verification email
			// callback (REQ-IDENT-40) or the admin CLI
			// (REQ-IDENT-50). Reject the entire update payload so
			// the client surfaces the conflict instead of silently
			// dropping the other fields it sent.
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: "verifiedAt is server-managed",
				Properties:  []string{"verifiedAt"},
			}
		case "id", "mayDelete":
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: k + " is read-only",
				Properties:  []string{k},
			}
		default:
			return identityPatch{}, &setError{
				Type:        "invalidProperties",
				Description: fmt.Sprintf("unknown property %q", k),
				Properties:  []string{k},
			}
		}
	}
	return out, nil
}

// validateAvatarBlob checks that blobID exists in the blob store and
// that its detected content-type starts with "image/". Returns the hash
// and size on success, or a setError for invalidProperties.
func validateAvatarBlob(ctx context.Context, st store.Store, blobID string) (hash string, size int64, serr *setError) {
	rc, err := st.Blobs().Get(ctx, blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			return "", 0, &setError{
				Type:        "invalidProperties",
				Properties:  []string{"avatarBlobId"},
				Description: "avatarBlobId: blob not found",
			}
		}
		return "", 0, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"avatarBlobId"},
			Description: fmt.Sprintf("avatarBlobId: blob lookup failed: %v", err),
		}
	}
	defer rc.Close()
	var buf [512]byte
	n, _ := io.ReadFull(rc, buf[:])
	ct := http.DetectContentType(buf[:n])
	if !strings.HasPrefix(ct, "image/") {
		return "", 0, &setError{
			Type:        "invalidProperties",
			Properties:  []string{"avatarBlobId"},
			Description: fmt.Sprintf("avatarBlobId: blob content-type %q is not an image", ct),
		}
	}
	rest, _ := io.Copy(io.Discard, rc)
	totalSize := int64(n) + rest
	return blobID, totalSize, nil
}
