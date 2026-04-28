package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

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
}

// listLocalDomains returns the set of domains we host. Backed by
// store.ListLocalDomains.
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
// the JMAP wire form. We use the integer's decimal representation; the
// dispatcher treats it as opaque per RFC 8620 §3.2.
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

// accountIDForPrincipal returns the canonical wire-form accountId for p,
// matching the value the session descriptor advertises in
// session.primaryAccounts (e.g. "a42" for principal id 42). v1
// collapses Account onto Principal, so the accountId derives from the
// principal id; protojmap.AccountIDForPrincipal is the single source of
// truth for the wire format and is shared with the rest of the JMAP
// surface.
func accountIDForPrincipal(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// validateAccountID checks the inbound accountId against the
// authenticated principal. JMAP returns "accountNotFound" when the
// caller asks for an account that is not theirs.
func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return nil // some clients omit; default to caller's account.
	}
	if requested != accountIDForPrincipal(p) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
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
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	state, err := g.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	all := g.h.identity.snapshot(ctx, p)
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		State:     state,
		List:      []jmapIdentity{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// Return everything.
		for _, rec := range all {
			resp.List = append(resp.List, rec.toJMAP())
		}
		return resp, nil
	}
	// Filter to requested ids.
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
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	now, err := c.h.currentState(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	if req.SinceState == now {
		return changesResponse{
			AccountID: accountIDForPrincipal(p),
			OldState:  req.SinceState,
			NewState:  now,
			Created:   []jmapID{},
			Updated:   []jmapID{},
			Destroyed: []jmapID{},
		}, nil
	}
	// Without a per-row change feed we conservatively report every
	// current identity as updated. RFC 8620 §5.2 permits over-reporting
	// when the server cannot reconstruct the precise delta — clients
	// must re-fetch on observed updates anyway.
	resp := changesResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	for _, rec := range c.h.identity.snapshot(ctx, p) {
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
		AccountID: accountIDForPrincipal(p),
		OldState:  oldState,
	}
	mutated := false
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
				Type: "invalidProperties", Properties: []string{"email"},
				Description: "email must be a valid addr-spec",
			}
			continue
		}
		domains, derr := s.h.domains(ctx)
		if derr != nil {
			return nil, protojmap.NewMethodError("serverFail", derr.Error())
		}
		if _, hosted := domains[dom]; !hosted {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = setError{
				Type: "forbiddenFrom",
				Description: fmt.Sprintf(
					"domain %q is not hosted by this server", dom),
				Properties: []string{"email"},
			}
			continue
		}
		rec := identityRecord{
			Name:          in.Name,
			Email:         in.Email,
			ReplyTo:       in.ReplyTo,
			Bcc:           in.Bcc,
			TextSignature: in.TextSignature,
			HTMLSignature: in.HTMLSignature,
		}
		if in.Signature != nil {
			v := *in.Signature
			rec.Signature = &v
		}
		created := s.h.identity.create(ctx, p, rec)
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
		patch, perr := decodePatch(raw)
		if perr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "invalidProperties", Description: perr.Error()}
			continue
		}
		rec, ok := s.h.identity.update(ctx, p, v, patch)
		if !ok {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapIdentity)
		}
		j := rec.toJMAP()
		// JMAP §5.3 update response carries `null` for fully-updated
		// objects; we instead return the updated value so clients can
		// observe server-applied normalisations. RFC 8620 permits this.
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
		if !s.h.identity.destroy(ctx, p, v) {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = setError{Type: "notFound"}
			continue
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
	}
	// Bump JMAP state on any mutation.
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

// decodePatch reads an Identity/set "update" object into the Store's
// patch shape, distinguishing missing fields from cleared ones.
func decodePatch(raw json.RawMessage) (identityPatch, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return identityPatch{}, err
	}
	var out identityPatch
	for k, v := range m {
		switch k {
		case "name":
			out.hasName = true
			if err := json.Unmarshal(v, &out.name); err != nil {
				return identityPatch{}, fmt.Errorf("name: %w", err)
			}
		case "replyTo":
			out.hasReplyTo = true
			if err := json.Unmarshal(v, &out.replyTo); err != nil {
				return identityPatch{}, fmt.Errorf("replyTo: %w", err)
			}
		case "bcc":
			out.hasBcc = true
			if err := json.Unmarshal(v, &out.bcc); err != nil {
				return identityPatch{}, fmt.Errorf("bcc: %w", err)
			}
		case "textSignature":
			out.hasTextSignature = true
			if err := json.Unmarshal(v, &out.textSignature); err != nil {
				return identityPatch{}, fmt.Errorf("textSignature: %w", err)
			}
		case "htmlSignature":
			out.hasHTMLSignature = true
			if err := json.Unmarshal(v, &out.htmlSignature); err != nil {
				return identityPatch{}, fmt.Errorf("htmlSignature: %w", err)
			}
		case "signature":
			out.hasSignature = true
			if string(v) == "null" {
				out.signature = nil
				continue
			}
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return identityPatch{}, fmt.Errorf("signature: %w", err)
			}
			out.signature = &s
		case "email":
			// JMAP forbids changing the primary email of an Identity
			// (RFC 8621 §7.4 server "MAY" reject).
			return identityPatch{}, fmt.Errorf("email is immutable")
		case "id", "mayDelete":
			return identityPatch{}, fmt.Errorf("%s is read-only", k)
		default:
			return identityPatch{}, fmt.Errorf("unknown property %q", k)
		}
	}
	return out, nil
}
