package sieve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// maxScriptBytes caps the per-blob size Sieve/set will read. Mirrors
// internal/sieve.DefaultMaxScriptBytes so the JMAP path enforces the
// same 256 KiB ceiling the parser does — bigger uploads are rejected
// before the parser allocates.
const maxScriptBytes = sieve.DefaultMaxScriptBytes

// handlerSet bundles the dependencies the per-method handlers reach
// for. One instance is constructed by Register and wrapped by each
// method-handler struct.
type handlerSet struct {
	store  store.Store
	logger *slog.Logger
	clk    clock.Clock
}

// stateString renders a JMAP state counter into the wire form. The
// dispatcher treats the value as opaque per RFC 8620 §3.2; we use the
// integer's decimal representation for byte-cheap monotonicity.
func stateString(seq int64) string { return strconv.FormatInt(seq, 10) }

// currentState returns the principal's current Sieve state.
func (h *handlerSet) currentState(ctx context.Context, pid store.PrincipalID) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return stateString(st.Sieve), nil
}

// loadScript fetches the principal's active script text. An empty
// string means no script on file; the JMAP /get response is then an
// empty list per RFC 9007 §2.2.
func (h *handlerSet) loadScript(ctx context.Context, pid store.PrincipalID) (string, error) {
	return h.store.Meta().GetSieveScript(ctx, pid)
}

// requirePrincipal pulls the authenticated principal from ctx. The
// dispatcher's requireAuth middleware guarantees this in production;
// the helper re-checks defensively so a future dispatcher rewrite
// cannot silently leak privileges. principalFromTestCtx supports the
// in-package tests' contextWithTestPrincipal seam.
func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := principalFromTestCtx(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

// requireAccount validates the inbound accountId against the
// authenticated principal. An absent accountId is rejected with
// "invalidArguments" per RFC 8620 §5.1; a mismatched one returns
// "accountNotFound".
func requireAccount(req jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if req == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if req != protojmap.AccountIDForPrincipal(pid) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// serverFail wraps an internal Go error into a JMAP method-error
// envelope.
func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}

// -- Sieve/get --------------------------------------------------------

type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties []string  `json:"properties,omitempty"`
}

type getResponse struct {
	AccountID jmapID            `json:"accountId"`
	State     string            `json:"state"`
	List      []jmapSieveScript `json:"list"`
	NotFound  []jmapID          `json:"notFound"`
}

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "Sieve/get" }

func (g getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := g.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	scriptText, err := g.h.loadScript(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := getResponse{
		AccountID: protojmap.AccountIDForPrincipal(pid),
		State:     state,
		List:      []jmapSieveScript{},
		NotFound:  []jmapID{},
	}
	if scriptText == "" {
		// No script — empty list. If the client asked for specific ids,
		// surface them as notFound.
		if req.IDs != nil {
			resp.NotFound = append(resp.NotFound, (*req.IDs)...)
		}
		return resp, nil
	}
	row := jmapSieveScript{
		ID:        idForPrincipal(pid),
		Name:      "active",
		BlobID:    blobIDForScript(scriptText),
		IsActive:  true,
		CreatedAt: g.h.clk.Now().UTC(),
	}
	if req.IDs == nil {
		resp.List = append(resp.List, row)
		return resp, nil
	}
	for _, id := range *req.IDs {
		if id == row.ID {
			resp.List = append(resp.List, row)
			continue
		}
		resp.NotFound = append(resp.NotFound, id)
	}
	return resp, nil
}

// -- Sieve/set --------------------------------------------------------

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

type setResponse struct {
	AccountID    jmapID                      `json:"accountId"`
	OldState     string                      `json:"oldState,omitempty"`
	NewState     string                      `json:"newState"`
	Created      map[string]jmapSieveScript  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapSieveScript `json:"updated,omitempty"`
	Destroyed    []jmapID                    `json:"destroyed,omitempty"`
	NotCreated   map[string]sieveSetError    `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]sieveSetError    `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]sieveSetError    `json:"notDestroyed,omitempty"`
}

// sieveSetError is the per-key error envelope. Type "sieveValidationError"
// (RFC 9007 §2.5) carries an "errors" array; other types fall back to
// the standard SetError shape (RFC 8620 §5.3 — type, description,
// optional properties).
type sieveSetError struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Properties  []string               `json:"properties,omitempty"`
	Errors      []sieveValidationError `json:"errors,omitempty"`
}

type sieveCreateInput struct {
	Name   string `json:"name"`
	BlobID string `json:"blobId"`
}

type sieveUpdateInput struct {
	Name     *string `json:"name,omitempty"`
	BlobID   *string `json:"blobId,omitempty"`
	IsActive *bool   `json:"isActive,omitempty"`
}

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "Sieve/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req setRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	oldState, err := s.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: protojmap.AccountIDForPrincipal(pid),
		OldState:  oldState,
	}
	mutated := false

	// Per RFC 9007 §2.3, at most one active script may exist per
	// account. We model that as one-row-per-principal: any create
	// replaces the prior text. The id surfaced is always the
	// principal id, regardless of which "create key" the client
	// supplied.
	for clientID, raw := range req.Create {
		var in sieveCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.notCreated()[clientID] = sieveSetError{
					Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		if in.BlobID == "" {
			resp.notCreated()[clientID] = sieveSetError{
				Type:        "invalidProperties",
				Properties:  []string{"blobId"},
				Description: "blobId is required",
			}
			continue
		}
		body, rerr := s.h.readBlob(ctx, in.BlobID)
		if rerr != nil {
			resp.notCreated()[clientID] = sieveSetError{
				Type: "blobNotFound", Description: rerr.Error(),
				Properties: []string{"blobId"},
			}
			continue
		}
		if errs := validateScriptBody(body); len(errs) > 0 {
			resp.notCreated()[clientID] = sieveSetError{
				Type: "sieveValidationError", Errors: errs,
			}
			continue
		}
		if err := s.h.store.Meta().SetSieveScript(ctx, pid, string(body)); err != nil {
			return nil, serverFail(fmt.Errorf("sieve: persist: %w", err))
		}
		row := jmapSieveScript{
			ID:        idForPrincipal(pid),
			Name:      defaultName(in.Name),
			BlobID:    blobIDForScript(string(body)),
			IsActive:  true,
			CreatedAt: s.h.clk.Now().UTC(),
		}
		if resp.Created == nil {
			resp.Created = map[string]jmapSieveScript{}
		}
		resp.Created[clientID] = row
		mutated = true
	}

	for id, raw := range req.Update {
		_, ok := principalFromID(id)
		if !ok || id != idForPrincipal(pid) {
			resp.notUpdated()[id] = sieveSetError{Type: "notFound"}
			continue
		}
		var patch sieveUpdateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &patch); err != nil {
				resp.notUpdated()[id] = sieveSetError{
					Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		if patch.IsActive != nil && !*patch.IsActive {
			// JMAP allows setting isActive=false to deactivate a script,
			// but Phase 1 is single-script-per-principal so deactivation
			// would orphan the row; reject as invalid.
			resp.notUpdated()[id] = sieveSetError{
				Type:        "invalidProperties",
				Properties:  []string{"isActive"},
				Description: "deactivating the singleton sieve script is not supported",
			}
			continue
		}
		if patch.BlobID == nil {
			// Name-only updates are accepted but no-op (Phase 1 has no
			// per-script name persistence).
			if resp.Updated == nil {
				resp.Updated = map[jmapID]*jmapSieveScript{}
			}
			resp.Updated[id] = nil
			mutated = true
			continue
		}
		body, rerr := s.h.readBlob(ctx, *patch.BlobID)
		if rerr != nil {
			resp.notUpdated()[id] = sieveSetError{
				Type: "blobNotFound", Description: rerr.Error(),
				Properties: []string{"blobId"},
			}
			continue
		}
		if errs := validateScriptBody(body); len(errs) > 0 {
			resp.notUpdated()[id] = sieveSetError{
				Type: "sieveValidationError", Errors: errs,
			}
			continue
		}
		if err := s.h.store.Meta().SetSieveScript(ctx, pid, string(body)); err != nil {
			return nil, serverFail(fmt.Errorf("sieve: persist: %w", err))
		}
		row := jmapSieveScript{
			ID:        idForPrincipal(pid),
			Name:      defaultName(deref(patch.Name)),
			BlobID:    blobIDForScript(string(body)),
			IsActive:  true,
			CreatedAt: s.h.clk.Now().UTC(),
		}
		if resp.Updated == nil {
			resp.Updated = map[jmapID]*jmapSieveScript{}
		}
		resp.Updated[id] = &row
		mutated = true
	}

	for _, id := range req.Destroy {
		if id != idForPrincipal(pid) {
			resp.notDestroyed()[id] = sieveSetError{Type: "notFound"}
			continue
		}
		if err := s.h.store.Meta().SetSieveScript(ctx, pid, ""); err != nil {
			return nil, serverFail(fmt.Errorf("sieve: clear: %w", err))
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
	}

	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, pid,
			store.JMAPStateKindSieve); err != nil {
			return nil, serverFail(fmt.Errorf("sieve: bump state: %w", err))
		}
	}
	newState, err := s.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// notCreated / notUpdated / notDestroyed lazily allocate the maps so
// the response only carries them when a failure actually occurred.
func (r *setResponse) notCreated() map[string]sieveSetError {
	if r.NotCreated == nil {
		r.NotCreated = map[string]sieveSetError{}
	}
	return r.NotCreated
}
func (r *setResponse) notUpdated() map[jmapID]sieveSetError {
	if r.NotUpdated == nil {
		r.NotUpdated = map[jmapID]sieveSetError{}
	}
	return r.NotUpdated
}
func (r *setResponse) notDestroyed() map[jmapID]sieveSetError {
	if r.NotDestroyed == nil {
		r.NotDestroyed = map[jmapID]sieveSetError{}
	}
	return r.NotDestroyed
}

// -- Sieve/validate ---------------------------------------------------

type validateRequest struct {
	AccountID jmapID `json:"accountId"`
	BlobID    string `json:"blobId"`
}

type validateResponse struct {
	AccountID jmapID                 `json:"accountId"`
	IsValid   bool                   `json:"isValid"`
	Errors    []sieveValidationError `json:"errors"`
}

type validateHandler struct{ h *handlerSet }

func (validateHandler) Method() string { return "Sieve/validate" }

func (v validateHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req validateRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	if req.BlobID == "" {
		return nil, protojmap.NewMethodError("invalidArguments",
			"blobId is required")
	}
	body, rerr := v.h.readBlob(ctx, req.BlobID)
	if rerr != nil {
		merr := protojmap.NewMethodError("blobNotFound", rerr.Error())
		merr.Properties = []string{"blobId"}
		return nil, merr
	}
	errs := validateScriptBody(body)
	resp := validateResponse{
		AccountID: protojmap.AccountIDForPrincipal(pid),
		IsValid:   len(errs) == 0,
		Errors:    errs,
	}
	if resp.Errors == nil {
		resp.Errors = []sieveValidationError{}
	}
	return resp, nil
}

// -- helpers ----------------------------------------------------------

// readBlob fetches the bytes of blobID from the store's blob surface.
// Returns an error wrapping the message "blob: not found" when the
// blob is missing or unreadable; callers map that to "blobNotFound"
// per RFC 9007 §2.4.
func (h *handlerSet) readBlob(ctx context.Context, blobID string) ([]byte, error) {
	rc, err := h.store.Blobs().Get(ctx, blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("blob %q not found", blobID)
		}
		return nil, fmt.Errorf("blob %q read: %w", blobID, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, maxScriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("blob %q read: %w", blobID, err)
	}
	if int64(len(body)) > maxScriptBytes {
		return nil, fmt.Errorf("blob %q exceeds %d bytes", blobID, maxScriptBytes)
	}
	return body, nil
}

// validateScriptBody runs the existing parse + validate passes from
// internal/sieve and collects every error it can. Returns nil when the
// script is well-formed and semantically valid.
func validateScriptBody(body []byte) []sieveValidationError {
	script, perr := sieve.Parse(body)
	if perr != nil {
		return errorsFromSieveErr(perr)
	}
	if verr := sieve.Validate(script); verr != nil {
		return errorsFromSieveErr(verr)
	}
	return nil
}

// errorsFromSieveErr projects a sieve.{Parse,Validation}Error into the
// JMAP wire shape. Other error types (I/O, programming bugs) collapse
// to a single line=1 column=1 entry so /set still has something to
// surface.
func errorsFromSieveErr(err error) []sieveValidationError {
	var p *sieve.ParseError
	if errors.As(err, &p) {
		return []sieveValidationError{{Line: p.Line, Column: p.Column, Message: p.Message}}
	}
	var v *sieve.ValidationError
	if errors.As(err, &v) {
		return []sieveValidationError{{Line: v.Line, Column: v.Column, Message: v.Message}}
	}
	return []sieveValidationError{{Line: 1, Column: 1, Message: err.Error()}}
}

// blobIDForScript returns a stable opaque token identifying the
// current active script body. The JMAP downloadUrl is tied to the
// blob hash; when no separate blob is on file (Phase 1 stores the
// script text in a metadata column, not as a blob), we synthesise
// "active-<len>" so clients have something to round-trip on /get
// without paying a blob upload roundtrip. Sieve/set always supplies
// the real blob hash via the upload path.
func blobIDForScript(text string) string {
	return fmt.Sprintf("active-%d", len(text))
}

// defaultName returns name when non-empty, otherwise the canonical
// "active" placeholder so the wire-form Name field is never empty.
func defaultName(name string) string {
	if name == "" {
		return "active"
	}
	return name
}

// deref returns *p when p is non-nil, else "".
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
