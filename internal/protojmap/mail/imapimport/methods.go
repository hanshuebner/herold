package imapimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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

type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
}

type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
	// DeleteImportedMail selects the keep-or-purge behaviour for every id in
	// Destroy (REQ-IMAP-IMP-102): false / absent keeps the imported mail (the
	// provenance label survives as an ordinary user label); true also deletes
	// the mail imported through the account, dedup-safe (REQ-IMAP-IMP-103).
	DeleteImportedMail *bool `json:"deleteImportedMail,omitempty"`
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

// currentState returns the opaque JMAP state string for IMAPImport/get,
// IMAPImport/changes, and the OldState/NewState envelope of IMAPImport/set.
// It reads the per-principal IMAPImport state counter
// (jmap_states.imap_import_state, migration 0065) which is bumped on every
// IMAPImport/set create, update, or destroy (REQ-IMAP-IMP-61).
func (h *handlerSet) currentState(ctx context.Context, pid store.PrincipalID) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return stateString(st.IMAPImport), nil
}

// stateString stringifies a JMAP state counter.
func stateString(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// wireWithFolderMap builds the wire form for an account and attaches its
// folder-map rows (REQ-IMAP-IMP-10/11) so a self-service client sees the
// current mappings on read.
func (h *handlerSet) wireWithFolderMap(ctx context.Context, a store.IMAPImportAccount) (jmapIMAPImportAccount, error) {
	wire := recordToJMAP(a)
	entries, err := h.store.Meta().GetIMAPImportFolderMap(ctx, a.ID)
	if err != nil {
		return jmapIMAPImportAccount{}, err
	}
	wire.FolderMap = folderMapToJMAP(entries)
	return wire, nil
}

// folderMapEntryIn is the wire form of one inbound folder-mapping row for
// IMAPImport/set create / update (REQ-IMAP-IMP-10).
type folderMapEntryIn struct {
	UpstreamFolder    string `json:"upstreamFolder"`
	HeroldMailboxName string `json:"heroldMailboxName"`
}

// toStoreFolderMap converts inbound wire rows to store rows for accountID,
// validating that neither name is empty. Mirrors the admin REST mapping logic
// (internal/protoadmin/imap_import.go) so the two surfaces behave identically.
func toStoreFolderMap(accountID string, in []folderMapEntryIn) ([]store.IMAPImportFolderMapEntry, error) {
	entries := make([]store.IMAPImportFolderMapEntry, 0, len(in))
	for _, e := range in {
		if e.UpstreamFolder == "" || e.HeroldMailboxName == "" {
			return nil, fmt.Errorf("folderMap entries must have non-empty upstreamFolder and heroldMailboxName")
		}
		entries = append(entries, store.IMAPImportFolderMapEntry{
			AccountID:         accountID,
			UpstreamFolder:    e.UpstreamFolder,
			HeroldMailboxName: e.HeroldMailboxName,
		})
	}
	return entries, nil
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
			wire, werr := g.h.wireWithFolderMap(ctx, a)
			if werr != nil {
				return nil, protojmap.NewMethodError("serverFail", werr.Error())
			}
			resp.List = append(resp.List, wire)
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
		wire, werr := g.h.wireWithFolderMap(ctx, rec)
		if werr != nil {
			return nil, protojmap.NewMethodError("serverFail", werr.Error())
		}
		resp.List = append(resp.List, wire)
	}
	return resp, nil
}

// -- IMAPImport/changes ----------------------------------------------

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "IMAPImport/changes" }

// Execute implements IMAPImport/changes (RFC 8620 §5.2, REQ-IMAP-IMP-61).
// The per-principal imap_import_state counter advances on every
// IMAPImport/set mutation but herold does not keep a per-id change ledger
// for this datatype, so when the client's sinceState differs from the
// current state every account is reported as "updated" — a drifted client
// re-fetches the full set via IMAPImport/get. This mirrors the
// FileShare/changes and Identity/changes behaviour.
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
	if req.SinceState == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "sinceState is required")
	}
	now, err := c.h.currentState(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	accountID := string(protojmap.AccountIDForPrincipal(p.ID))
	resp := changesResponse{
		AccountID: accountID,
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if req.SinceState == now {
		// No drift: nothing changed.
		return resp, nil
	}
	rows, lerr := c.h.store.Meta().ListIMAPImportAccountsByPrincipal(ctx, p.ID)
	if lerr != nil {
		return nil, protojmap.NewMethodError("serverFail", lerr.Error())
	}
	for _, a := range rows {
		resp.Updated = append(resp.Updated, a.ID)
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
		IdentityID       string             `json:"identityId"`
		AccountName      string             `json:"accountName"`
		Host             string             `json:"host"`
		Port             int                `json:"port"`
		TLSMode          string             `json:"tlsMode"`
		Username         string             `json:"username"`
		AuthMethod       string             `json:"authMethod"`
		BackfillHorizon  string             `json:"backfillHorizon"`
		Credential       string             `json:"credential"`
		State            string             `json:"state,omitempty"`
		DeletePropagates *bool              `json:"deletePropagates,omitempty"`
		FolderMap        []folderMapEntryIn `json:"folderMap,omitempty"`
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

		// Validate the owning Identity (decision 10, REQ-IMAP-IMP-01/02).
		// When supplied it must reference an Identity owned by the caller;
		// the store enforces 0-or-1 account per identity at insert time.
		if in.IdentityID != "" {
			ident, ierr := s.h.store.Meta().GetJMAPIdentity(ctx, in.IdentityID)
			if ierr != nil || ident.PrincipalID != p.ID {
				setNotCreated(&resp, clientID, "invalidProperties",
					[]string{"identityId"},
					"identityId does not reference an Identity owned by the caller")
				continue
			}
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
			// A new account may only be created enabled or disabled; the
			// cutover states are reached by transitioning an existing account
			// (REQ-IMAP-IMP-90).
			if s := store.IMAPImportAccountState(in.State); s != store.IMAPImportAccountStateEnabled &&
				s != store.IMAPImportAccountStateDisabled {
				setNotCreated(&resp, clientID, "invalidProperties", []string{"state"},
					fmt.Sprintf("a new account may only be created %q or %q",
						store.IMAPImportAccountStateEnabled, store.IMAPImportAccountStateDisabled))
				continue
			}
			state = store.IMAPImportAccountState(in.State)
		}
		deletePropagates := true
		if in.DeletePropagates != nil {
			deletePropagates = *in.DeletePropagates
		}

		// Validate the optional folder map before creating the account so a
		// malformed entry fails the create rather than leaving an account
		// without its mappings (REQ-IMAP-IMP-10/11). The accountID is stamped
		// after the row exists.
		if _, ferr := toStoreFolderMap("", in.FolderMap); ferr != nil {
			setNotCreated(&resp, clientID, "invalidProperties",
				[]string{"folderMap"}, ferr.Error())
			continue
		}

		create := store.IMAPImportAccountCreate{
			IdentityID:        in.IdentityID,
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
			if errors.Is(cerr, store.ErrConflict) {
				// 0-or-1 import account per identity (decision 10).
				setNotCreated(&resp, clientID, "invalidProperties",
					[]string{"identityId"},
					"this Identity already has an IMAP import account")
				continue
			}
			return nil, protojmap.NewMethodError("serverFail", cerr.Error())
		}

		// Persist the folder map for the new account (REQ-IMAP-IMP-10/11).
		// Validated above, so the conversion cannot fail here.
		if len(in.FolderMap) > 0 {
			entries, _ := toStoreFolderMap(created.ID, in.FolderMap)
			if serr := s.h.store.Meta().SetIMAPImportFolderMap(ctx, created.ID, entries); serr != nil {
				return nil, protojmap.NewMethodError("serverFail", serr.Error())
			}
		}

		if resp.Created == nil {
			resp.Created = make(map[string]jmapIMAPImportAccount)
		}
		wire, werr := s.h.wireWithFolderMap(ctx, created)
		if werr != nil {
			return nil, protojmap.NewMethodError("serverFail", werr.Error())
		}
		resp.Created[clientID] = wire
	}

	// -- updates -------------------------------------------------------

	// updateIn is the wire form for IMAPImport/set update. Only mutable
	// non-secret fields may appear in a patch. credential, when present,
	// is re-sealed; when absent the stored credential is preserved.
	type updateIn struct {
		AccountName      *string             `json:"accountName,omitempty"`
		Host             *string             `json:"host,omitempty"`
		Port             *int                `json:"port,omitempty"`
		TLSMode          *string             `json:"tlsMode,omitempty"`
		Username         *string             `json:"username,omitempty"`
		AuthMethod       *string             `json:"authMethod,omitempty"`
		BackfillHorizon  *string             `json:"backfillHorizon,omitempty"`
		Credential       *string             `json:"credential,omitempty"`
		State            *string             `json:"state,omitempty"`
		DeletePropagates *bool               `json:"deletePropagates,omitempty"`
		FolderMap        *[]folderMapEntryIn `json:"folderMap,omitempty"`
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
			IdentityID:        prior.IdentityID, // owning Identity is immutable here
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
			next := store.IMAPImportAccountState(*in.State)
			if err := validateStateTransition(prior.State, next); err != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"state"}, err.Error())
				continue
			}
			upd.State = next
			// Entering migrating forces the backfill floor to "all" so the
			// one-shot complete backfill reaches the earliest UID
			// (REQ-IMAP-IMP-91). Done here (and re-forced in the worker) so
			// IMAPImport/get reflects it and a restart resumes with no floor.
			if next == store.IMAPImportAccountStateMigrating {
				upd.BackfillFloorDate = nil
			}
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

		// Validate the folder map (when present in the patch) before mutating
		// the account, so a malformed entry does not leave a half-applied
		// update (REQ-IMAP-IMP-10/11).
		var folderEntries []store.IMAPImportFolderMapEntry
		if in.FolderMap != nil {
			fe, ferr := toStoreFolderMap(prior.ID, *in.FolderMap)
			if ferr != nil {
				setNotUpdated(&resp, id, "invalidProperties",
					[]string{"folderMap"}, ferr.Error())
				continue
			}
			folderEntries = fe
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

		// Replace the folder map when it was present in the patch (a present
		// empty array clears it). Absent leaves the existing map untouched
		// (REQ-IMAP-IMP-10/11). Mirrors the admin REST "req.FolderMap != nil"
		// semantics.
		if in.FolderMap != nil {
			if serr := s.h.store.Meta().SetIMAPImportFolderMap(ctx, updated.ID, folderEntries); serr != nil {
				return nil, protojmap.NewMethodError("serverFail", serr.Error())
			}
		}

		wire, werr := s.h.wireWithFolderMap(ctx, updated)
		if werr != nil {
			return nil, protojmap.NewMethodError("serverFail", werr.Error())
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapIMAPImportAccount)
		}
		resp.Updated[id] = &wire
	}

	// -- destroys ------------------------------------------------------

	deleteImportedMail := req.DeleteImportedMail != nil && *req.DeleteImportedMail
	for _, id := range req.Destroy {
		_, derr := store.RemoveIMAPImportAccount(ctx, s.h.store, p.ID, id, deleteImportedMail)
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

	// Advance the per-principal IMAPImport state counter once if this call
	// committed any mutation, so a self-service client tracking
	// IMAPImport/changes observes the drift (REQ-IMAP-IMP-61, migration 0065).
	// A call that only produced not-created/not-updated/not-destroyed errors
	// leaves the state unchanged.
	mutated := len(resp.Created) > 0 || len(resp.Updated) > 0 || len(resp.Destroyed) > 0
	if mutated {
		if _, ierr := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindIMAPImport); ierr != nil {
			return nil, protojmap.NewMethodError("serverFail", ierr.Error())
		}
	}
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
		store.IMAPImportAccountStateErrored,
		store.IMAPImportAccountStateMigrating,
		store.IMAPImportAccountStateMigrated:
		return nil
	default:
		return fmt.Errorf("state must be one of %q, %q, %q, %q, %q; got %q",
			store.IMAPImportAccountStateEnabled,
			store.IMAPImportAccountStateDisabled,
			store.IMAPImportAccountStateErrored,
			store.IMAPImportAccountStateMigrating,
			store.IMAPImportAccountStateMigrated, s)
	}
}

// validateStateTransition enforces the lifecycle rules for a state change
// requested through the JMAP / admin surfaces (REQ-IMAP-IMP-90/95):
//
//   - migrating may only be requested from enabled (or idempotently from
//     migrating) — it starts the complete-migration cutover.
//   - migrated is reached automatically by the worker when the cutover
//     completes; it cannot be set directly.
//   - enabled (re-open / re-enable) and disabled are allowed from any state.
func validateStateTransition(prior, next store.IMAPImportAccountState) error {
	switch next {
	case store.IMAPImportAccountStateMigrating:
		if prior != store.IMAPImportAccountStateEnabled &&
			prior != store.IMAPImportAccountStateMigrating {
			return fmt.Errorf("complete migration can only be started from the %q state (current: %q)",
				store.IMAPImportAccountStateEnabled, prior)
		}
	case store.IMAPImportAccountStateMigrated:
		return fmt.Errorf("%q is reached automatically when the cutover completes; it cannot be set directly",
			store.IMAPImportAccountStateMigrated)
	}
	return nil
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
			"state", "deletePropagates", "folderMap":
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
