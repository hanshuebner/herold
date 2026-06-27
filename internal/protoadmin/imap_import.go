package protoadmin

// imap_import.go — admin REST surface for per-principal IMAP import accounts.
//
// Endpoints (all authAdmin):
//
//   GET    /api/v1/principals/{pid}/imap-imports         list accounts (no credential)
//   POST   /api/v1/principals/{pid}/imap-imports         create
//   PATCH  /api/v1/principals/{pid}/imap-imports/{aid}   update
//   DELETE /api/v1/principals/{pid}/imap-imports/{aid}   delete
//   GET    /api/v1/imap-imports/status                   live worker snapshot
//
// REQ-IMAP-IMP-60, REQ-IMAP-IMP-65, REQ-ADM-300.

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// -- wire DTOs ----------------------------------------------------------------

// imapImportAccountDTO is the read-only wire shape for an IMAP import
// account. The sealed credential is NEVER returned (REQ-IMAP-IMP-70).
type imapImportAccountDTO struct {
	ID               string     `json:"id"`
	IdentityID       string     `json:"identity_id,omitempty"`
	PrincipalID      uint64     `json:"principal_id"`
	AccountName      string     `json:"account_name"`
	Host             string     `json:"host"`
	Port             int        `json:"port"`
	TLSMode          string     `json:"tls_mode"`
	Username         string     `json:"username"`
	AuthMethod       string     `json:"auth_method"`
	BackfillHorizon  string     `json:"backfill_horizon"`
	State            string     `json:"state"`
	LastSuccessAt    *time.Time `json:"last_success_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	DeletePropagates bool       `json:"delete_propagates"`
	HasCredential    bool       `json:"has_credential"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// imapImportFolderMapEntryDTO is one folder-mapping row.
type imapImportFolderMapEntryDTO struct {
	UpstreamFolder    string `json:"upstream_folder"`
	HeroldMailboxName string `json:"herold_mailbox_name"`
}

// toImapImportDTO converts a store row to the read-only wire form.
// Credential fields are stripped; HasCredential reflects presence.
func toImapImportDTO(a store.IMAPImportAccount) imapImportAccountDTO {
	dto := imapImportAccountDTO{
		ID:               a.ID,
		IdentityID:       a.IdentityID,
		PrincipalID:      uint64(a.PrincipalID),
		AccountName:      a.AccountName,
		Host:             a.Host,
		Port:             a.Port,
		TLSMode:          string(a.TLSMode),
		Username:         a.Username,
		AuthMethod:       string(a.AuthMethod),
		State:            string(a.State),
		LastError:        a.LastError,
		DeletePropagates: a.DeletePropagates,
		HasCredential:    len(a.CredentialCT) > 0,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}
	if a.BackfillFloorDate == nil {
		dto.BackfillHorizon = "all"
	} else {
		dto.BackfillHorizon = a.BackfillFloorDate.UTC().Format("2006-01-02")
	}
	if a.LastSuccessAt != nil {
		t := *a.LastSuccessAt
		dto.LastSuccessAt = &t
	}
	return dto
}

// createIMAPImportRequest is the body for POST .../imap-imports.
type createIMAPImportRequest struct {
	IdentityID       string                        `json:"identity_id,omitempty"`
	AccountName      string                        `json:"account_name"`
	Host             string                        `json:"host"`
	Port             int                           `json:"port"`
	TLSMode          string                        `json:"tls_mode"`
	Username         string                        `json:"username"`
	AuthMethod       string                        `json:"auth_method"`
	BackfillHorizon  string                        `json:"backfill_horizon"`
	Credential       string                        `json:"credential"`
	State            string                        `json:"state,omitempty"`
	DeletePropagates *bool                         `json:"delete_propagates,omitempty"`
	FolderMap        []imapImportFolderMapEntryDTO `json:"folder_map,omitempty"`
}

// patchIMAPImportRequest is the body for PATCH .../imap-imports/{aid}.
// All fields are optional; absent fields preserve the existing value.
type patchIMAPImportRequest struct {
	AccountName     *string `json:"account_name,omitempty"`
	Host            *string `json:"host,omitempty"`
	Port            *int    `json:"port,omitempty"`
	TLSMode         *string `json:"tls_mode,omitempty"`
	Username        *string `json:"username,omitempty"`
	AuthMethod      *string `json:"auth_method,omitempty"`
	BackfillHorizon *string `json:"backfill_horizon,omitempty"`
	// Credential, when non-empty, replaces the sealed credential.
	// When absent or empty the stored credential is preserved
	// (REQ-IMAP-IMP-70).
	Credential       *string                       `json:"credential,omitempty"`
	State            *string                       `json:"state,omitempty"`
	DeletePropagates *bool                         `json:"delete_propagates,omitempty"`
	FolderMap        []imapImportFolderMapEntryDTO `json:"folder_map,omitempty"`
}

// -- validation helpers -------------------------------------------------------

func validateIMAPTLSMode(m string) error {
	switch store.IMAPImportTLSMode(m) {
	case store.IMAPImportTLSModeSTARTTLS, store.IMAPImportTLSModeImplicit:
		return nil
	default:
		return fmt.Errorf("tls_mode must be %q or %q; got %q (tls_mode=none is rejected per REQ-IMAP-IMP-06)",
			store.IMAPImportTLSModeSTARTTLS, store.IMAPImportTLSModeImplicit, m)
	}
}

func validateIMAPAuthMethod(m string) error {
	switch store.IMAPImportAuthMethod(m) {
	case store.IMAPImportAuthMethodPassword,
		store.IMAPImportAuthMethodAppPassword,
		store.IMAPImportAuthMethodXOAuth2:
		return nil
	default:
		return fmt.Errorf("auth_method must be one of %q, %q, %q; got %q",
			store.IMAPImportAuthMethodPassword,
			store.IMAPImportAuthMethodAppPassword,
			store.IMAPImportAuthMethodXOAuth2, m)
	}
}

func validateIMAPPort(p int) error {
	if p <= 0 || p > 65535 {
		return fmt.Errorf("port must be in range 1..65535; got %d", p)
	}
	return nil
}

func validateIMAPState(s string) error {
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

// validateIMAPStateTransition enforces the cutover lifecycle rules for an
// admin PATCH state change (REQ-IMAP-IMP-90/95): migrating only from enabled
// (or idempotently migrating); migrated is worker-only and cannot be set
// directly; enabled / disabled are allowed from any state.
func validateIMAPStateTransition(prior, next store.IMAPImportAccountState) error {
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

// -- handlers -----------------------------------------------------------------

// handleListIMAPImports handles GET /api/v1/principals/{pid}/imap-imports.
// Returns all accounts for the principal without credential material.
// REQ-IMAP-IMP-60, REQ-ADM-300.
func (s *Server) handleListIMAPImports(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	// Verify the principal exists.
	if _, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	rows, err := s.store.Meta().ListIMAPImportAccountsByPrincipal(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]imapImportAccountDTO, 0, len(rows))
	for _, a := range rows {
		items = append(items, toImapImportDTO(a))
	}
	writeJSON(w, http.StatusOK, pageDTO[imapImportAccountDTO]{Items: items, Next: nil})
}

// handleCreateIMAPImport handles POST /api/v1/principals/{pid}/imap-imports.
// Seals the credential before storing; never returns it.
// REQ-IMAP-IMP-60, REQ-IMAP-IMP-70, REQ-ADM-300.
func (s *Server) handleCreateIMAPImport(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	// Verify the principal exists.
	if _, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	dataKey := s.opts.IMAPImportDataKey
	if len(dataKey) == 0 {
		writeProblem(w, r, http.StatusServiceUnavailable, "not_configured",
			"IMAP import credential sealing not configured on this server; set [server.secrets].data_key_ref", "")
		return
	}

	var req createIMAPImportRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	// Validate required fields.
	if req.AccountName == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "account_name is required", "")
		return
	}
	if req.Host == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "host is required", "")
		return
	}
	if err := validateIMAPPort(req.Port); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	if err := validateIMAPTLSMode(req.TLSMode); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	if req.Username == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "username is required", "")
		return
	}
	if err := validateIMAPAuthMethod(req.AuthMethod); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	if req.BackfillHorizon == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"backfill_horizon is required (use \"all\", \"30d\", \"90d\", \"1y\", or \"YYYY-MM-DD\")", "")
		return
	}
	if req.Credential == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "credential is required", "")
		return
	}

	// Validate the owning Identity (decision 10, REQ-IMAP-IMP-01/02). When
	// supplied it must reference an Identity owned by the same principal.
	if req.IdentityID != "" {
		ident, ierr := s.store.Meta().GetJMAPIdentity(r.Context(), req.IdentityID)
		if ierr != nil || ident.PrincipalID != pid {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"identity_id does not reference an Identity owned by this principal", req.IdentityID)
			return
		}
	}

	// Resolve the backfill horizon (REQ-IMAP-IMP-15, -16).
	floor, err := store.ParseBackfillHorizon(req.BackfillHorizon, s.clk.Now())
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}

	// Seal the credential (REQ-IMAP-IMP-70).
	ct, serr := secrets.Seal(dataKey, []byte(req.Credential))
	if serr != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.imap_import.seal_failed",
			"activity", observe.ActivityInternal, "err", serr)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal credential", "")
		return
	}
	if err := store.ValidateIMAPImportCredentialCT(ct); err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.imap_import.sealed_ct_invalid",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "credential seal produced invalid ciphertext", "")
		return
	}

	state := store.IMAPImportAccountStateEnabled
	if req.State != "" {
		if err := validateIMAPState(req.State); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		// A new account may only be created enabled or disabled; the cutover
		// states are reached by transitioning an existing account
		// (REQ-IMAP-IMP-90).
		if s := store.IMAPImportAccountState(req.State); s != store.IMAPImportAccountStateEnabled &&
			s != store.IMAPImportAccountStateDisabled {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				fmt.Sprintf("a new account may only be created %q or %q",
					store.IMAPImportAccountStateEnabled, store.IMAPImportAccountStateDisabled), "")
			return
		}
		state = store.IMAPImportAccountState(req.State)
	}
	deletePropagates := true
	if req.DeletePropagates != nil {
		deletePropagates = *req.DeletePropagates
	}

	create := store.IMAPImportAccountCreate{
		IdentityID:        req.IdentityID,
		PrincipalID:       pid,
		AccountName:       req.AccountName,
		Host:              req.Host,
		Port:              req.Port,
		TLSMode:           store.IMAPImportTLSMode(req.TLSMode),
		Username:          req.Username,
		AuthMethod:        store.IMAPImportAuthMethod(req.AuthMethod),
		BackfillFloorDate: floor,
		CredentialCT:      ct,
		State:             state,
		DeletePropagates:  deletePropagates,
	}
	created, cerr := s.store.Meta().CreateIMAPImportAccount(r.Context(), create)
	if cerr != nil {
		s.writeStoreError(w, r, cerr)
		return
	}

	// Apply folder map if supplied.
	if len(req.FolderMap) > 0 {
		entries := make([]store.IMAPImportFolderMapEntry, 0, len(req.FolderMap))
		for _, e := range req.FolderMap {
			entries = append(entries, store.IMAPImportFolderMapEntry{
				AccountID:         created.ID,
				UpstreamFolder:    e.UpstreamFolder,
				HeroldMailboxName: e.HeroldMailboxName,
			})
		}
		if serr := s.store.Meta().SetIMAPImportFolderMap(r.Context(), created.ID, entries); serr != nil {
			s.writeStoreError(w, r, serr)
			return
		}
	}

	s.appendAudit(r.Context(), "imap_import.create",
		fmt.Sprintf("imap_import:%s", created.ID),
		store.OutcomeSuccess, "",
		map[string]string{
			"principal_id": fmt.Sprintf("%d", pid),
			"account_name": created.AccountName,
			"host":         created.Host,
		})

	w.Header().Set("Location", fmt.Sprintf("/api/v1/principals/%d/imap-imports/%s", pid, created.ID))
	writeJSON(w, http.StatusCreated, toImapImportDTO(created))
}

// handlePatchIMAPImport handles PATCH /api/v1/principals/{pid}/imap-imports/{aid}.
// Absent fields preserve existing values; credential is re-sealed when supplied.
// REQ-IMAP-IMP-60, REQ-IMAP-IMP-70, REQ-ADM-300.
func (s *Server) handlePatchIMAPImport(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	aid := r.PathValue("aid")
	if aid == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "account id is required", "")
		return
	}

	// Verify the principal exists.
	if _, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// Load the existing account; also verifies aid belongs to pid.
	existing, err := s.store.Meta().GetIMAPImportAccount(r.Context(), aid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if existing.PrincipalID != pid {
		// The account exists but belongs to a different principal.
		writeProblem(w, r, http.StatusNotFound, "not_found", "imap import account not found", aid)
		return
	}

	var req patchIMAPImportRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	// Build the update from the existing record (patch semantics).
	upd := store.IMAPImportAccountUpdate{
		ID:                existing.ID,
		PrincipalID:       pid,
		IdentityID:        existing.IdentityID, // owning Identity preserved
		AccountName:       existing.AccountName,
		Host:              existing.Host,
		Port:              existing.Port,
		TLSMode:           existing.TLSMode,
		Username:          existing.Username,
		AuthMethod:        existing.AuthMethod,
		BackfillFloorDate: existing.BackfillFloorDate,
		State:             existing.State,
		DeletePropagates:  existing.DeletePropagates,
		// CredentialCT nil means "keep existing"
	}

	if req.AccountName != nil {
		upd.AccountName = *req.AccountName
	}
	if req.Host != nil {
		upd.Host = *req.Host
	}
	if req.Port != nil {
		if err := validateIMAPPort(*req.Port); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		upd.Port = *req.Port
	}
	if req.TLSMode != nil {
		if err := validateIMAPTLSMode(*req.TLSMode); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		upd.TLSMode = store.IMAPImportTLSMode(*req.TLSMode)
	}
	if req.Username != nil {
		upd.Username = *req.Username
	}
	if req.AuthMethod != nil {
		if err := validateIMAPAuthMethod(*req.AuthMethod); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		upd.AuthMethod = store.IMAPImportAuthMethod(*req.AuthMethod)
	}
	if req.BackfillHorizon != nil {
		floor, err := store.ParseBackfillHorizon(*req.BackfillHorizon, s.clk.Now())
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		upd.BackfillFloorDate = floor
	}
	if req.State != nil {
		if err := validateIMAPState(*req.State); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		next := store.IMAPImportAccountState(*req.State)
		if err := validateIMAPStateTransition(existing.State, next); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		upd.State = next
		// Entering migrating forces the backfill floor to "all" so the
		// one-shot complete backfill reaches the earliest UID
		// (REQ-IMAP-IMP-91).
		if next == store.IMAPImportAccountStateMigrating {
			upd.BackfillFloorDate = nil
		}
	}
	if req.DeletePropagates != nil {
		upd.DeletePropagates = *req.DeletePropagates
	}

	// Reseal credential if supplied (REQ-IMAP-IMP-70).
	if req.Credential != nil && *req.Credential != "" {
		dataKey := s.opts.IMAPImportDataKey
		if len(dataKey) == 0 {
			writeProblem(w, r, http.StatusServiceUnavailable, "not_configured",
				"IMAP import credential sealing not configured on this server; set [server.secrets].data_key_ref", "")
			return
		}
		ct, serr := secrets.Seal(dataKey, []byte(*req.Credential))
		if serr != nil {
			s.loggerFrom(r.Context()).Error("protoadmin.imap_import.seal_failed",
				"activity", observe.ActivityInternal, "err", serr)
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal credential", "")
			return
		}
		if err := store.ValidateIMAPImportCredentialCT(ct); err != nil {
			s.loggerFrom(r.Context()).Error("protoadmin.imap_import.sealed_ct_invalid",
				"activity", observe.ActivityInternal, "err", err)
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "credential seal produced invalid ciphertext", "")
			return
		}
		upd.CredentialCT = ct
	}

	updated, uerr := s.store.Meta().UpdateIMAPImportAccount(r.Context(), upd)
	if uerr != nil {
		s.writeStoreError(w, r, uerr)
		return
	}

	// Apply folder map if supplied.
	if req.FolderMap != nil {
		entries := make([]store.IMAPImportFolderMapEntry, 0, len(req.FolderMap))
		for _, e := range req.FolderMap {
			entries = append(entries, store.IMAPImportFolderMapEntry{
				AccountID:         updated.ID,
				UpstreamFolder:    e.UpstreamFolder,
				HeroldMailboxName: e.HeroldMailboxName,
			})
		}
		if serr := s.store.Meta().SetIMAPImportFolderMap(r.Context(), updated.ID, entries); serr != nil {
			s.writeStoreError(w, r, serr)
			return
		}
	}

	s.appendAudit(r.Context(), "imap_import.update",
		fmt.Sprintf("imap_import:%s", updated.ID),
		store.OutcomeSuccess, "",
		map[string]string{
			"principal_id": fmt.Sprintf("%d", pid),
			"account_id":   updated.ID,
		})

	writeJSON(w, http.StatusOK, toImapImportDTO(updated))
}

// handleDeleteIMAPImport handles DELETE /api/v1/principals/{pid}/imap-imports/{aid}.
// Removes the account row (cascades to cursor/state tables). Owner-scoped.
// REQ-IMAP-IMP-60, REQ-ADM-300.
func (s *Server) handleDeleteIMAPImport(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	aid := r.PathValue("aid")
	if aid == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "account id is required", "")
		return
	}

	// Verify the principal exists.
	if _, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// delete_imported_mail selects keep (default) vs purge (REQ-IMAP-IMP-102):
	// keep leaves the imported mail and the provenance label in place; purge
	// also deletes the mail imported through this account, dedup-safe.
	deleteImportedMail := r.URL.Query().Get("delete_imported_mail") == "true"

	// RemoveAccount is owner-scoped: it returns ErrNotFound when the account
	// does not exist or does not belong to pid. It performs the dedup-safe
	// purge (when requested) before dropping the account row.
	result, err := store.RemoveIMAPImportAccount(r.Context(), s.store, pid, aid, deleteImportedMail)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "imap import account not found", aid)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	s.appendAudit(r.Context(), "imap_import.delete",
		fmt.Sprintf("imap_import:%s", aid),
		store.OutcomeSuccess, "",
		map[string]string{
			"principal_id":         fmt.Sprintf("%d", pid),
			"account_id":           aid,
			"delete_imported_mail": fmt.Sprintf("%t", deleteImportedMail),
			"messages_destroyed":   fmt.Sprintf("%d", result.MessagesDestroyed),
			"labels_detached":      fmt.Sprintf("%d", result.LabelsDetached),
		})

	w.WriteHeader(http.StatusNoContent)
}

// handleIMAPImportStatus handles GET /api/v1/imap-imports/status.
// Returns the live point-in-time snapshot of every IMAP import worker.
// When no pool has been wired (nil provider) returns an empty list.
// REQ-IMAP-IMP-65.
func (s *Server) handleIMAPImportStatus(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}

	provider := s.opts.IMAPImportStatus
	if provider == nil {
		writeJSON(w, http.StatusOK, pageDTO[IMAPImportWorkerStatus]{
			Items: []IMAPImportWorkerStatus{},
		})
		return
	}

	snap := provider.Snapshot()
	if snap == nil {
		snap = []IMAPImportWorkerStatus{}
	}
	writeJSON(w, http.StatusOK, pageDTO[IMAPImportWorkerStatus]{Items: snap})
}
