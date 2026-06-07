package imapimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles dependencies shared by every method handler.
type handlerSet struct {
	store   store.Store
	clk     clock.Clock
	logger  *slog.Logger
	dataKey []byte
}

// -- shared JMAP envelope shapes -------------------------------------

type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties []string  `json:"properties,omitempty"`
}

type getResponse struct {
	AccountID string                  `json:"accountId"`
	State     string                  `json:"state"`
	List      []jmapIMAPImportAccount `json:"list"`
	NotFound  []jmapID                `json:"notFound"`
}

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type setResponse struct {
	AccountID    string                            `json:"accountId"`
	OldState     string                            `json:"oldState,omitempty"`
	NewState     string                            `json:"newState"`
	Created      map[string]jmapIMAPImportAccount  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapIMAPImportAccount `json:"updated,omitempty"`
	Destroyed    []jmapID                          `json:"destroyed,omitempty"`
	NotCreated   map[string]setError               `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError               `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError               `json:"notDestroyed,omitempty"`
}

// currentState returns the opaque JMAP state string for IMAPImport/get
// and the OldState/NewState envelope of IMAPImport/set.
//
// IMAPImport/changes is not implemented in v1 (no dedicated jmap_states
// column; migration 0057 is frozen), so there is no per-principal
// IMAPImport state counter to report. Return a fixed placeholder rather
// than borrowing an unrelated datatype's counter (e.g. FileShare), which
// would couple the two and make this state change for reasons unrelated
// to IMAPImport. A dedicated counter + /changes can be added in a later,
// unfrozen migration.
func (h *handlerSet) currentState(_ context.Context, _ store.PrincipalID) (string, error) {
	return "0", nil
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

// -- IMAPImport/get --------------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "IMAPImport/get" }

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
	state, err := g.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(p.ID)),
		State:     state,
		List:      []jmapIMAPImportAccount{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// Return all accounts for this principal.
		rows, lerr := g.h.store.Meta().ListIMAPImportAccountsByPrincipal(ctx, p.ID)
		if lerr != nil {
			return nil, protojmap.NewMethodError("serverFail", lerr.Error())
		}
		for _, a := range rows {
			resp.List = append(resp.List, recordToJMAP(a))
		}
		return resp, nil
	}
	// Fetch by IDs: load all and index for O(1) lookup.
	all, lerr := g.h.store.Meta().ListIMAPImportAccountsByPrincipal(ctx, p.ID)
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	byID := make(map[string]store.IMAPImportAccount, len(all))
	for _, a := range all {
		byID[a.ID] = a
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

// -- IMAPImport/set --------------------------------------------------

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "IMAPImport/set" }

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
	oldState, err := s.h.currentState(ctx, p.ID)
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

	// -- creates -------------------------------------------------------

	// createIn is the wire form for IMAPImport/set create. credential is
	// write-only and sealed server-side before storage (REQ-IMAP-IMP-70).
	type createIn struct {
		AccountName      string `json:"accountName"`
		Host             string `json:"host"`
		Port             int    `json:"port"`
		TLSMode          string `json:"tlsMode"`
		Username         string `json:"username"`
		AuthMethod       string `json:"authMethod"`
		BackfillHorizon  string `json:"backfillHorizon"`
		Credential       string `json:"credential"`
		State            string `json:"state,omitempty"`
		DeletePropagates *bool  `json:"deletePropagates,omitempty"`
	}

	for clientID, raw := range req.Create {
		var in createIn
		if err := json.Unmarshal(raw, &in); err != nil {
			setNotCreated(&resp, clientID, "invalidProperties", nil, err.Error())
			continue
		}

		// Validate required fields.
		var missing []string
		if in.AccountName == "" {
			missing = append(missing, "accountName")
		}
		if in.Host == "" {
			missing = append(missing, "host")
		}
		if in.Port == 0 {
			missing = append(missing, "port")
		}
		if in.TLSMode == "" {
			missing = append(missing, "tlsMode")
		}
		if in.Username == "" {
			missing = append(missing, "username")
		}
		if in.AuthMethod == "" {
			missing = append(missing, "authMethod")
		}
		if in.BackfillHorizon == "" {
			missing = append(missing, "backfillHorizon")
		}
		if in.Credential == "" {
			missing = append(missing, "credential")
		}
		if len(missing) > 0 {
			setNotCreated(&resp, clientID, "invalidProperties", missing,
				"required fields missing: "+strings.Join(missing, ", "))
			continue
		}

		// Validate tlsMode.
		if err := validateTLSMode(in.TLSMode); err != nil {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"tlsMode"}, err.Error())
			continue
		}

		// Validate authMethod.
		if err := validateAuthMethod(in.AuthMethod); err != nil {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"authMethod"}, err.Error())
			continue
		}

		// Validate port.
		if err := validatePort(in.Port); err != nil {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"port"}, err.Error())
			continue
		}

		// Resolve backfill horizon to absolute floor date
		// (REQ-IMAP-IMP-15, -16). Relative values are resolved at
		// create time using the injected clock so the horizon does not
		// drift silently.
		floor, err := resolveHorizon(in.BackfillHorizon, s.h.clk.Now())
		if err != nil {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"backfillHorizon"}, err.Error())
			continue
		}

		// Seal the credential with the AEAD data key
		// (REQ-IMAP-IMP-70, decision 2).
		ct, serr := secrets.Seal(s.h.dataKey, []byte(in.Credential))
		if serr != nil {
			return nil, protojmap.NewMethodError("serverFail",
				"credential seal failed: "+serr.Error())
		}

		// Validate that the sealed blob carries the v1: prefix before
		// handing it to the store (mirrors ValidateIMAPImportCredentialCT).
		if err := store.ValidateIMAPImportCredentialCT(ct); err != nil {
			return nil, protojmap.NewMethodError("serverFail",
				"credential seal produced invalid ciphertext: "+err.Error())
		}

		state := store.IMAPImportAccountStateEnabled
		if in.State != "" {
			if err := validateState(in.State); err != nil {
				setNotCreated(&resp, clientID, "invalidProperties",
					[]string{"state"}, err.Error())
				continue
			}
			state = store.IMAPImportAccountState(in.State)
		}
		deletePropagates := true
		if in.DeletePropagates != nil {
			deletePropagates = *in.DeletePropagates
		}

		create := store.IMAPImportAccountCreate{
			PrincipalID:       p.ID,
			AccountName:       in.AccountName,
			Host:              in.Host,
			Port:              in.Port,
			TLSMode:           store.IMAPImportTLSMode(in.TLSMode),
			Username:          in.Username,
			AuthMethod:        store.IMAPImportAuthMethod(in.AuthMethod),
			BackfillFloorDate: floor,
			CredentialCT:      ct,
			State:             state,
			DeletePropagates:  deletePropagates,
		}
		created, cerr := s.h.store.Meta().CreateIMAPImportAccount(ctx, create)
		if cerr != nil {
			if errors.Is(cerr, store.ErrInvalidArgument) {
				setNotCreated(&resp, clientID, "invalidProperties",
					nil, cerr.Error())
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", cerr.Error())
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapIMAPImportAccount)
		}
		resp.Created[clientID] = recordToJMAP(created)
	}

	// -- updates -------------------------------------------------------

	// updateIn is the wire form for IMAPImport/set update. Only mutable
	// non-secret fields may appear in a patch. credential, when present,
	// is re-sealed; when absent the stored credential is preserved.
	type updateIn struct {
		AccountName      *string `json:"accountName,omitempty"`
		Host             *string `json:"host,omitempty"`
		Port             *int    `json:"port,omitempty"`
		TLSMode          *string `json:"tlsMode,omitempty"`
		Username         *string `json:"username,omitempty"`
		AuthMethod       *string `json:"authMethod,omitempty"`
		BackfillHorizon  *string `json:"backfillHorizon,omitempty"`
		Credential       *string `json:"credential,omitempty"`
		State            *string `json:"state,omitempty"`
		DeletePropagates *bool   `json:"deletePropagates,omitempty"`
	}

	// Load current accounts for update validation.
	existing, lerr := s.h.store.Meta().ListIMAPImportAccountsByPrincipal(ctx, p.ID)
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	byID := make(map[string]store.IMAPImportAccount, len(existing))
	for _, a := range existing {
		byID[a.ID] = a
	}

	for id, raw := range req.Update {
		prior, exists := byID[id]
		if !exists {
			setNotUpdated(&resp, id, "notFound", nil, "")
			continue
		}

		// Reject attempts to set immutable fields.
		var rawMap map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawMap); err != nil {
			setNotUpdated(&resp, id, "invalidProperties", nil, err.Error())
			continue
		}
		if badProp := immutableUpdateField(rawMap); badProp != "" {
			setNotUpdated(&resp, id, "invalidProperties",
				[]string{badProp}, badProp+" is immutable")
			continue
		}

		var in updateIn
		if err := json.Unmarshal(raw, &in); err != nil {
			setNotUpdated(&resp, id, "invalidProperties", nil, err.Error())
			continue
		}

		// Apply patch onto a copy of the existing record.
		upd := store.IMAPImportAccountUpdate{
			ID:                prior.ID,
			PrincipalID:       p.ID,
			AccountName:       prior.AccountName,
			Host:              prior.Host,
			Port:              prior.Port,
			TLSMode:           prior.TLSMode,
			Username:          prior.Username,
			AuthMethod:        prior.AuthMethod,
			BackfillFloorDate: prior.BackfillFloorDate,
			State:             prior.State,
			DeletePropagates:  prior.DeletePropagates,
			// CredentialCT: nil means "keep existing"; set below if
			// credential is present in the patch.
		}

		if in.AccountName != nil {
			upd.AccountName = *in.AccountName
		}
		if in.Host != nil {
			upd.Host = *in.Host
		}
		if in.Port != nil {
			if err := validatePort(*in.Port); err != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"port"}, err.Error())
				continue
			}
			upd.Port = *in.Port
		}
		if in.TLSMode != nil {
			if err := validateTLSMode(*in.TLSMode); err != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"tlsMode"}, err.Error())
				continue
			}
			upd.TLSMode = store.IMAPImportTLSMode(*in.TLSMode)
		}
		if in.Username != nil {
			upd.Username = *in.Username
		}
		if in.AuthMethod != nil {
			if err := validateAuthMethod(*in.AuthMethod); err != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"authMethod"}, err.Error())
				continue
			}
			upd.AuthMethod = store.IMAPImportAuthMethod(*in.AuthMethod)
		}
		if in.BackfillHorizon != nil {
			// Lowering the horizon is allowed (REQ-IMAP-IMP-19).
			// Raising is accepted too — the worker treats it as a
			// no-op (REQ-IMAP-IMP-19). Resolve relative values to
			// absolute dates at update time (REQ-IMAP-IMP-16).
			floor, err := resolveHorizon(*in.BackfillHorizon, s.h.clk.Now())
			if err != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"backfillHorizon"}, err.Error())
				continue
			}
			upd.BackfillFloorDate = floor
		}
		if in.State != nil {
			if err := validateState(*in.State); err != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"state"}, err.Error())
				continue
			}
			upd.State = store.IMAPImportAccountState(*in.State)
		}
		if in.DeletePropagates != nil {
			upd.DeletePropagates = *in.DeletePropagates
		}

		// If a new credential is supplied, re-seal it. Otherwise leave
		// CredentialCT nil so the store keeps the existing value.
		if in.Credential != nil && *in.Credential != "" {
			ct, serr := secrets.Seal(s.h.dataKey, []byte(*in.Credential))
			if serr != nil {
				return nil, protojmap.NewMethodError("serverFail",
					"credential seal failed: "+serr.Error())
			}
			if err := store.ValidateIMAPImportCredentialCT(ct); err != nil {
				return nil, protojmap.NewMethodError("serverFail",
					"credential seal produced invalid ciphertext: "+err.Error())
			}
			upd.CredentialCT = ct
		}

		updated, uerr := s.h.store.Meta().UpdateIMAPImportAccount(ctx, upd)
		if uerr != nil {
			if errors.Is(uerr, store.ErrNotFound) {
				setNotUpdated(&resp, id, "notFound", nil, "")
				continue
			}
			if errors.Is(uerr, store.ErrInvalidArgument) {
				setNotUpdated(&resp, id, "invalidProperties", nil, uerr.Error())
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", uerr.Error())
		}
		wire := recordToJMAP(updated)
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapIMAPImportAccount)
		}
		resp.Updated[id] = &wire
	}

	// -- destroys ------------------------------------------------------

	for _, id := range req.Destroy {
		derr := s.h.store.Meta().DeleteIMAPImportAccount(ctx, p.ID, id)
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
	}

	// State: wave 1 does not have a dedicated IMAPImport state counter
	// (no jmap_states column in migration 0057). The state string does
	// not advance on mutations; wave 2 will add IncrementJMAPState calls
	// once JMAPStateKindIMAPImport and the column exist.
	newState, err := s.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = newState
	return resp, nil
}

// -- validation helpers -----------------------------------------------

func validateTLSMode(m string) error {
	switch store.IMAPImportTLSMode(m) {
	case store.IMAPImportTLSModeSTARTTLS, store.IMAPImportTLSModeImplicit:
		return nil
	default:
		return fmt.Errorf("tlsMode must be %q or %q; got %q (tls_mode=none is rejected per REQ-IMAP-IMP-06)",
			store.IMAPImportTLSModeSTARTTLS, store.IMAPImportTLSModeImplicit, m)
	}
}

func validateAuthMethod(m string) error {
	switch store.IMAPImportAuthMethod(m) {
	case store.IMAPImportAuthMethodPassword,
		store.IMAPImportAuthMethodAppPassword,
		store.IMAPImportAuthMethodXOAuth2:
		return nil
	default:
		return fmt.Errorf("authMethod must be one of %q, %q, %q; got %q",
			store.IMAPImportAuthMethodPassword,
			store.IMAPImportAuthMethodAppPassword,
			store.IMAPImportAuthMethodXOAuth2, m)
	}
}

func validatePort(p int) error {
	if p <= 0 || p > 65535 {
		return fmt.Errorf("port must be in range 1..65535; got %d", p)
	}
	return nil
}

func validateState(s string) error {
	switch store.IMAPImportAccountState(s) {
	case store.IMAPImportAccountStateEnabled,
		store.IMAPImportAccountStateDisabled,
		store.IMAPImportAccountStateErrored:
		return nil
	default:
		return fmt.Errorf("state must be one of %q, %q, %q; got %q",
			store.IMAPImportAccountStateEnabled,
			store.IMAPImportAccountStateDisabled,
			store.IMAPImportAccountStateErrored, s)
	}
}

// resolveHorizon converts the backfillHorizon value to an absolute floor
// date pointer (nil = "all") using now as the reference time
// (REQ-IMAP-IMP-15, -16). Delegates to store.ParseBackfillHorizon so
// the admin REST and JMAP paths share one implementation.
func resolveHorizon(horizon string, now time.Time) (*time.Time, error) {
	return store.ParseBackfillHorizon(horizon, now)
}

// immutableUpdateField returns the first property name that is not
// mutable via IMAPImport/set update, or "" if all properties are valid.
func immutableUpdateField(rawMap map[string]json.RawMessage) string {
	for k := range rawMap {
		switch k {
		case "accountName", "host", "port", "tlsMode", "username",
			"authMethod", "backfillHorizon", "credential",
			"state", "deletePropagates":
			// mutable
		default:
			return k
		}
	}
	return ""
}

// setNotCreated is a convenience helper that initialises NotCreated
// on first use and appends one entry.
func setNotCreated(resp *setResponse, clientID, errType string, props []string, desc string) {
	if resp.NotCreated == nil {
		resp.NotCreated = make(map[string]setError)
	}
	resp.NotCreated[clientID] = setError{
		Type:        errType,
		Properties:  props,
		Description: desc,
	}
}

// setNotUpdated is a convenience helper that initialises NotUpdated
// on first use and appends one entry.
func setNotUpdated(resp *setResponse, id, errType string, props []string, desc string) {
	if resp.NotUpdated == nil {
		resp.NotUpdated = make(map[jmapID]setError)
	}
	resp.NotUpdated[id] = setError{
		Type:        errType,
		Properties:  props,
		Description: desc,
	}
}
