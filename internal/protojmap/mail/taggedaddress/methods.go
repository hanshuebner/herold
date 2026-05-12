package taggedaddress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// CapsProvider reports the live operator-configured per-principal caps
// for filters and dismissals. The handler reads it on every set call so
// a SIGHUP-driven sysconfig change takes effect without a restart.
//
// The returned filtersCap MUST be > 0; non-positive values fall back to
// store.MaxTaggedAddressFiltersPerPrincipal.
type CapsProvider func() (filtersCap, dismissalsCap int)

// handlerSet bundles the dependencies shared by every method handler.
type handlerSet struct {
	store  store.Store
	clk    clock.Clock
	logger *slog.Logger
	caps   CapsProvider
}

// -- shared envelope shapes -----------------------------------------

// getRequest is the inbound RFC 8620 §5.1 envelope.
type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties []string  `json:"properties,omitempty"`
}

// getResponse is the response envelope.
type getResponse struct {
	AccountID string                    `json:"accountId"`
	State     string                    `json:"state"`
	List      []jmapTaggedAddressFilter `json:"list"`
	NotFound  []jmapID                  `json:"notFound"`
}

// changesRequest mirrors RFC 8620 §5.2.
type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
}

// changesResponse mirrors RFC 8620 §5.2.
type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

// setRequest is the inbound RFC 8620 §5.3 envelope.
type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

// setError is the per-key error envelope (RFC 8620 §5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// setResponse is the outbound RFC 8620 §5.3 envelope.
type setResponse struct {
	AccountID    string                              `json:"accountId"`
	OldState     string                              `json:"oldState,omitempty"`
	NewState     string                              `json:"newState"`
	Created      map[string]jmapTaggedAddressFilter  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapTaggedAddressFilter `json:"updated,omitempty"`
	Destroyed    []jmapID                            `json:"destroyed,omitempty"`
	NotCreated   map[string]setError                 `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError                 `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError                 `json:"notDestroyed,omitempty"`
}

// validateAccountID checks the inbound accountId against the
// authenticated principal.
func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if requested != string(protojmap.AccountIDForPrincipal(p.ID)) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// currentState reads the principal's Identity JMAP state counter and
// returns the wire-form state string. Tagged-address mutations bump
// JMAPStateKindIdentity so the SPA's Identity/changes push (REQ-TAG-72)
// fires and TaggedAddressFilter/changes returns a monotonic token.
func (h *handlerSet) currentState(ctx context.Context, p store.Principal) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return "", err
	}
	return stateString(st.Identity), nil
}

// filtersCap returns the operator-configured per-principal filter cap,
// or the documented default when no provider is wired.
func (h *handlerSet) filtersCap() int {
	if h.caps == nil {
		return store.MaxTaggedAddressFiltersPerPrincipal
	}
	n, _ := h.caps()
	if n <= 0 {
		return store.MaxTaggedAddressFiltersPerPrincipal
	}
	return n
}

// -- TaggedAddressFilter/get ----------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "TaggedAddressFilter/get" }

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
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	state, err := g.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	all, err := g.h.store.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(p.ID)),
		State:     state,
		List:      []jmapTaggedAddressFilter{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		for _, f := range all {
			resp.List = append(resp.List, recordToJMAP(f))
		}
		return resp, nil
	}
	byID := make(map[string]store.TaggedAddressFilter, len(all))
	for _, f := range all {
		byID[f.ID] = f
	}
	for _, id := range *req.IDs {
		rec, found := byID[id]
		if !found {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		resp.List = append(resp.List, recordToJMAP(rec))
	}
	return resp, nil
}

// -- TaggedAddressFilter/changes ------------------------------------

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "TaggedAddressFilter/changes" }

func (c changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req changesRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	now, err := c.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	accountID := string(protojmap.AccountIDForPrincipal(p.ID))
	if req.SinceState == now {
		return changesResponse{
			AccountID: accountID,
			OldState:  req.SinceState,
			NewState:  now,
			Created:   []jmapID{},
			Updated:   []jmapID{},
			Destroyed: []jmapID{},
		}, nil
	}
	// v1: we do not retain per-id change ledger entries for
	// tagged-address filters. JMAP /changes requires us to enumerate
	// changed ids; in absence of a row-level ledger we conservatively
	// report every current row as "updated" so a client that drifted
	// re-fetches the full set. This mirrors the Identity/changes
	// behaviour for the synthesised default identity.
	rows, lerr := c.h.store.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	resp := changesResponse{
		AccountID: accountID,
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	for _, f := range rows {
		resp.Updated = append(resp.Updated, f.ID)
	}
	return resp, nil
}

// -- TaggedAddressFilter/set ----------------------------------------

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "TaggedAddressFilter/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req setRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	oldState, err := s.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(p.ID)),
		OldState:  oldState,
	}
	mutated := false

	// Pre-count current rows so we can enforce REQ-TAG-86's per-principal
	// cap inside the same handler (the store also enforces the hard cap,
	// but client-visible "tooManyFilters" requires the count here so the
	// /set response can mark each over-cap create with a typed error).
	existing, err := s.h.store.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	currentCount := len(existing)
	cap := s.h.filtersCap()
	// Index existing rows by id so updates can read prior values.
	byID := make(map[string]store.TaggedAddressFilter, len(existing))
	for _, f := range existing {
		byID[f.ID] = f
	}

	// -- creates ----
	type createIn struct {
		BaseIdentityID *string `json:"baseIdentityId,omitempty"`
		Suffix         *string `json:"suffix,omitempty"`
		Action         *string `json:"action,omitempty"`
		LabelName      *string `json:"labelName,omitempty"`
	}
	for clientID, raw := range req.Create {
		var in createIn
		if err := json.Unmarshal(raw, &in); err != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Description: err.Error(),
			}
			continue
		}
		if in.BaseIdentityID == nil || *in.BaseIdentityID == "" {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"baseIdentityId"},
				Description: "baseIdentityId is required",
			}
			continue
		}
		if in.Suffix == nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"suffix"},
				Description: "suffix is required",
			}
			continue
		}
		// Normalise suffix to lower-case before validation (REQ-TAG-02).
		suffix := strings.ToLower(*in.Suffix)
		if _, verr := validateSuffix(suffix); verr != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"suffix"},
				Description: verr.Error(),
			}
			continue
		}
		if in.Action == nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"action"},
				Description: "action is required",
			}
			continue
		}
		if err := validateAction(*in.Action); err != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"action"},
				Description: err.Error(),
			}
			continue
		}
		if in.LabelName == nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"labelName"},
				Description: "labelName is required",
			}
			continue
		}
		if err := validateLabelName(*in.LabelName); err != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"labelName"},
				Description: err.Error(),
			}
			continue
		}
		// The wire-form id "default" denotes the synthesised default
		// identity, which is owned by the principal-by-construction
		// (REQ-IDENT-*) but has no jmap_identities row until first
		// referenced. Tagged-filter rows reference jmap_identities by
		// FK, so we materialise the row on demand and rewrite the
		// effective base id to the persisted numeric id before the
		// store insert (see internal/protojmap/mail/identity/store.go
		// for the canonical "default" special-case pattern).
		if *in.BaseIdentityID == "default" {
			matID, mErr := s.h.store.Meta().MaterializeDefaultIdentity(ctx, p.ID)
			if mErr != nil {
				return nil, protojmap.NewMethodError("serverFail", mErr.Error())
			}
			*in.BaseIdentityID = matID
		}
		// Verify the identity belongs to this principal.
		ident, err := s.h.store.Meta().GetJMAPIdentity(ctx, *in.BaseIdentityID)
		if err != nil || ident.PrincipalID != p.ID {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "invalidProperties",
				Properties:  []string{"baseIdentityId"},
				Description: "baseIdentityId does not name an identity owned by the caller",
			}
			continue
		}
		// REQ-TAG (verification gate): only verified identities may
		// host tagged filters. The synthesised default identity is
		// verified-by-construction (VerifiedAtUs == 0 yet MayDelete
		// == false). Persisted identities are unverified until the
		// email round-trip completes.
		if ident.MayDelete && ident.VerifiedAtUs == 0 {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "forbidden",
				Description: "identity_not_verified",
				Properties:  []string{"baseIdentityId"},
			}
			continue
		}
		// REQ-TAG-86: cap check before the store-level cap so the
		// per-create error is typed "tooManyFilters" rather than a
		// generic serverFail.
		if currentCount >= cap {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type:        "tooManyFilters",
				Description: fmt.Sprintf("per-principal filter cap of %d reached", cap),
			}
			continue
		}
		// Insert the row. The store assigns the opaque id when ID
		// == "" and normalises the suffix on its own; we pass the
		// already-normalised form for symmetry with /get responses
		// in the same request.
		row := store.TaggedAddressFilter{
			PrincipalID:    p.ID,
			BaseIdentityID: *in.BaseIdentityID,
			Suffix:         suffix,
			Action:         *in.Action,
			LabelName:      *in.LabelName,
		}
		if err := s.h.store.Meta().InsertTaggedAddressFilter(ctx, row); err != nil {
			if errors.Is(err, store.ErrConflict) {
				if resp.NotCreated == nil {
					resp.NotCreated = make(map[string]setError)
				}
				resp.NotCreated[clientID] = setError{
					Type:        "conflict",
					Description: "a filter for this (identity, suffix) already exists",
					Properties:  []string{"suffix"},
				}
				continue
			}
			if errors.Is(err, store.ErrTooManyFilters) {
				if resp.NotCreated == nil {
					resp.NotCreated = make(map[string]setError)
				}
				resp.NotCreated[clientID] = setError{
					Type:        "tooManyFilters",
					Description: "per-principal filter cap reached",
				}
				continue
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				if resp.NotCreated == nil {
					resp.NotCreated = make(map[string]setError)
				}
				resp.NotCreated[clientID] = setError{
					Type:        "invalidProperties",
					Description: err.Error(),
				}
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		// Read back so the wire form carries the store-allocated id
		// and timestamps.
		created, err := s.h.store.Meta().GetTaggedAddressFilter(ctx, p.ID, *in.BaseIdentityID, suffix)
		if err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapTaggedAddressFilter)
		}
		resp.Created[clientID] = recordToJMAP(created)
		currentCount++
		mutated = true
		s.audit(ctx, p, "tagged_address_filter.create", created.ID, map[string]string{
			"base_identity_id": created.BaseIdentityID,
			"suffix":           created.Suffix,
			"action":           created.Action,
		})
	}

	// -- updates ----
	type updateIn struct {
		Action    *string `json:"action,omitempty"`
		LabelName *string `json:"labelName,omitempty"`
	}
	for id, raw := range req.Update {
		prior, exists := byID[id]
		if !exists {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		// Reject attempts to mutate immutable fields and unknown keys.
		var raw2 map[string]json.RawMessage
		if err := json.Unmarshal(raw, &raw2); err != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type: "invalidProperties", Description: err.Error()}
			continue
		}
		bad := ""
		for k := range raw2 {
			switch k {
			case "action", "labelName":
				// OK.
			case "id", "baseIdentityId", "suffix", "createdAt", "updatedAt":
				bad = k
			default:
				bad = k
			}
			if bad != "" {
				break
			}
		}
		if bad != "" {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type:        "invalidProperties",
				Properties:  []string{bad},
				Description: bad + " is immutable or unknown",
			}
			continue
		}
		var in updateIn
		if err := json.Unmarshal(raw, &in); err != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type: "invalidProperties", Description: err.Error()}
			continue
		}
		action := prior.Action
		if in.Action != nil {
			if err := validateAction(*in.Action); err != nil {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"action"},
					Description: err.Error(),
				}
				continue
			}
			action = *in.Action
		}
		label := prior.LabelName
		if in.LabelName != nil {
			if err := validateLabelName(*in.LabelName); err != nil {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type:        "invalidProperties",
					Properties:  []string{"labelName"},
					Description: err.Error(),
				}
				continue
			}
			label = *in.LabelName
		}
		if err := s.h.store.Meta().UpdateTaggedAddressFilter(ctx, id, action, label); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{Type: "notFound"}
				continue
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				if resp.NotUpdated == nil {
					resp.NotUpdated = make(map[jmapID]setError)
				}
				resp.NotUpdated[id] = setError{
					Type: "invalidProperties", Description: err.Error()}
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		// Read back the canonical row.
		got, err := s.h.store.Meta().GetTaggedAddressFilter(ctx, p.ID,
			prior.BaseIdentityID, prior.Suffix)
		if err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		jrow := recordToJMAP(got)
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapTaggedAddressFilter)
		}
		resp.Updated[id] = &jrow
		mutated = true
		s.audit(ctx, p, "tagged_address_filter.update", got.ID, map[string]string{
			"base_identity_id": got.BaseIdentityID,
			"suffix":           got.Suffix,
			"action":           got.Action,
		})
	}

	// -- destroys ----
	for _, id := range req.Destroy {
		_, exists := byID[id]
		if !exists {
			// Try the store directly in case the filter was created
			// in the same /set call (creates flush to currentCount but
			// not to byID).
			suffix, baseID, _, derr := s.h.store.Meta().DeleteTaggedAddressFilter(ctx, id)
			if derr != nil {
				if resp.NotDestroyed == nil {
					resp.NotDestroyed = make(map[jmapID]setError)
				}
				if errors.Is(derr, store.ErrNotFound) {
					resp.NotDestroyed[id] = setError{Type: "notFound"}
					continue
				}
				return nil, protojmap.NewMethodError("serverFail", derr.Error())
			}
			resp.Destroyed = append(resp.Destroyed, id)
			mutated = true
			s.audit(ctx, p, "tagged_address_filter.destroy", id, map[string]string{
				"base_identity_id": baseID,
				"suffix":           suffix,
			})
			continue
		}
		suffix, baseID, _, derr := s.h.store.Meta().DeleteTaggedAddressFilter(ctx, id)
		if derr != nil {
			if errors.Is(derr, store.ErrNotFound) {
				if resp.NotDestroyed == nil {
					resp.NotDestroyed = make(map[jmapID]setError)
				}
				resp.NotDestroyed[id] = setError{Type: "notFound"}
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", derr.Error())
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
		s.audit(ctx, p, "tagged_address_filter.destroy", id, map[string]string{
			"base_identity_id": baseID,
			"suffix":           suffix,
		})
	}

	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindIdentity); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	newState, err := s.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = newState
	return resp, nil
}

// audit writes a single audit_log row describing a tagged-address
// mutation. Failures are logged but never escalate to the caller; the
// audit log is best-effort observability.
func (s setHandler) audit(ctx context.Context, p store.Principal, action, subject string, metadata map[string]string) {
	entry := store.AuditLogEntry{
		ActorKind: store.ActorPrincipal,
		ActorID:   strconv.FormatUint(uint64(p.ID), 10),
		Action:    action,
		Subject:   subject,
		Outcome:   store.OutcomeSuccess,
		Metadata:  metadata,
	}
	if err := s.h.store.Meta().AppendAuditLog(ctx, entry); err != nil {
		s.h.logger.Warn("tagged-address audit log write failed",
			slog.String("subsystem", "jmap-tagged-addresses"),
			slog.String("activity", "audit"),
			slog.String("action", action),
			slog.String("subject", subject),
			slog.String("err", err.Error()))
	}
}
