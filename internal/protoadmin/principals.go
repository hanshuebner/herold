package protoadmin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// parsePID reads the {pid} path parameter and returns it as a
// PrincipalID. On failure the caller returns immediately after the
// 400 problem is written.
func parsePID(w http.ResponseWriter, r *http.Request) (store.PrincipalID, bool) {
	raw := r.PathValue("pid")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"invalid principal id", raw)
		return 0, false
	}
	return store.PrincipalID(n), true
}

// createPrincipalRequest is the POST /principals body.
type createPrincipalRequest struct {
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	DisplayName string   `json:"display_name,omitempty"`
	QuotaBytes  int64    `json:"quota_bytes,omitempty"`
	Flags       []string `json:"flags,omitempty"`
}

func (s *Server) handleListPrincipals(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	after := store.PrincipalID(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"cursor is not a principal id", raw)
			return
		}
		after = store.PrincipalID(n)
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return
		}
		if n > 1000 {
			n = 1000
		}
		limit = n
	}
	rows, err := s.store.Meta().ListPrincipals(r.Context(), after, limit)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]principalDTO, 0, len(rows))
	for _, p := range rows {
		items = append(items, toPrincipalDTO(p))
	}
	var next *string
	if len(rows) == limit && len(rows) > 0 {
		tok := strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
		next = &tok
	}
	writeJSON(w, http.StatusOK, pageDTO[principalDTO]{Items: items, Next: next})
}

func (s *Server) handleCreatePrincipal(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createPrincipalRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"email and password are required", "")
		return
	}
	flags, ok := principalFlagsFromStrings(req.Flags)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"unknown flag", "")
		return
	}
	pid, err := s.dir.CreatePrincipal(r.Context(), req.Email, req.Password)
	if err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}
	// Apply optional fields (quota, display name, flags) via a follow-up
	// UpdatePrincipal so the directory layer stays narrow.
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	p.DisplayName = req.DisplayName
	p.QuotaBytes = req.QuotaBytes
	p.Flags |= flags
	if err := s.store.Meta().UpdatePrincipal(r.Context(), p); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Mailbox provisioning lives in directory.CreatePrincipal so every
	// principal-creation entry point (admin REST, bootstrap CLI, future
	// OIDC autoprovision) gets the same default set without duplicating
	// the logic. REQ-ADM-MAILBOX-INIT.
	s.appendAudit(r.Context(), "principal.create",
		fmt.Sprintf("principal:%d", p.ID),
		store.OutcomeSuccess, "", map[string]string{"email": p.CanonicalEmail})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/principals/%d", p.ID))
	writeJSON(w, http.StatusCreated, toPrincipalDTO(p))
}

func (s *Server) handleGetPrincipal(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPrincipalDTO(p))
}

// patchPrincipalRequest is the partial-update body. Pointer-typed
// fields let a caller distinguish "not present" from "set to zero".
type patchPrincipalRequest struct {
	DisplayName *string   `json:"display_name,omitempty"`
	QuotaBytes  *int64    `json:"quota_bytes,omitempty"`
	Flags       *[]string `json:"flags,omitempty"`
}

func (s *Server) handlePatchPrincipal(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	var req patchPrincipalRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if req.DisplayName != nil {
		p.DisplayName = *req.DisplayName
	}
	if req.QuotaBytes != nil {
		// Only admin may change quota.
		if !caller.Flags.Has(store.PrincipalFlagAdmin) {
			writeProblem(w, r, http.StatusForbidden, "forbidden",
				"only admins may change quota", "")
			return
		}
		p.QuotaBytes = *req.QuotaBytes
	}
	if req.Flags != nil {
		if !caller.Flags.Has(store.PrincipalFlagAdmin) {
			writeProblem(w, r, http.StatusForbidden, "forbidden",
				"only admins may change flags", "")
			return
		}
		flags, okFlags := principalFlagsFromStrings(*req.Flags)
		if !okFlags {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"unknown flag", "")
			return
		}
		// Preserve TOTPEnabled; clients cannot flip it through PATCH.
		preserved := p.Flags & store.PrincipalFlagTOTPEnabled
		p.Flags = flags | preserved
	}
	if err := s.store.Meta().UpdatePrincipal(r.Context(), p); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "principal.update",
		fmt.Sprintf("principal:%d", p.ID),
		store.OutcomeSuccess, "", nil)
	writeJSON(w, http.StatusOK, toPrincipalDTO(p))
}

func (s *Server) handleDeletePrincipal(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	// DELETE is admin-only, even for self-delete (we refuse self-delete
	// to avoid admins accidentally locking themselves out).
	if !requireAdmin(w, r, caller) {
		return
	}
	if err := s.dir.DeletePrincipal(r.Context(), pid); err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "principal.delete",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// setPasswordRequest is the PUT /password body.
type setPasswordRequest struct {
	NewPassword     string `json:"new_password"`
	CurrentPassword string `json:"current_password,omitempty"`
}

func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	var req setPasswordRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.NewPassword == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"new_password is required", "")
		return
	}
	isSelf := caller.ID == pid
	isAdmin := caller.Flags.Has(store.PrincipalFlagAdmin)
	if isSelf {
		if req.CurrentPassword == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"current_password is required for self change", "")
			return
		}
		if err := s.dir.UpdatePassword(r.Context(), pid, req.CurrentPassword, req.NewPassword); err != nil {
			s.writeDirectoryError(w, r, err)
			return
		}
	} else if isAdmin {
		// Admin override: hash and store without re-verifying current.
		if err := s.adminSetPassword(r, pid, req.NewPassword); err != nil {
			s.writeDirectoryError(w, r, err)
			return
		}
	} else {
		writeProblem(w, r, http.StatusForbidden, "forbidden",
			"insufficient privileges", "")
		return
	}
	s.appendAudit(r.Context(), "principal.password.change",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// adminSetPassword bypasses the current-password check. Argon2id
// parameters match the directory package's internal defaults
// (STANDARDS.md §9): time=2, memory=64 MiB, threads=4, keyLen=32,
// saltLen=16. Keeping a local hasher here avoids widening the
// directory surface for a single caller in scope of this ticket; when
// a third caller arrives the two should converge on an exported
// helper in directory.
func (s *Server) adminSetPassword(r *http.Request, pid store.PrincipalID, newPassword string) error {
	if len(newPassword) < directory.MinPasswordLength {
		return directory.ErrWeakPassword
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		return err
	}
	hash, err := hashPasswordArgon2id(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	p.PasswordHash = hash
	return s.store.Meta().UpdatePrincipal(r.Context(), p)
}

// writeStoreError maps a store error to an HTTP problem response.
func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", err.Error(), "")
	case errors.Is(err, store.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error(), "")
	case errors.Is(err, store.ErrQuotaExceeded):
		writeProblem(w, r, http.StatusInsufficientStorage, "quota_exceeded", err.Error(), "")
	default:
		s.loggerFrom(r.Context()).Error("protoadmin.store_error", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"internal server error", "")
	}
}

// writeDirectoryError maps a directory error (which may wrap a store
// error) to an HTTP problem response.
func (s *Server) writeDirectoryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, directory.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", err.Error(), "")
	case errors.Is(err, directory.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error(), "")
	case errors.Is(err, directory.ErrInvalidEmail):
		writeProblem(w, r, http.StatusBadRequest, "invalid_email", err.Error(), "")
	case errors.Is(err, directory.ErrWeakPassword):
		writeProblem(w, r, http.StatusBadRequest, "weak_password", err.Error(), "")
	case errors.Is(err, directory.ErrUnauthorized):
		writeProblem(w, r, http.StatusUnauthorized, "unauthorized", err.Error(), "")
	case errors.Is(err, directory.ErrRateLimited):
		writeProblem(w, r, http.StatusTooManyRequests, "rate_limited", err.Error(), "")
	case errors.Is(err, directory.ErrTOTPNotEnrolled):
		writeProblem(w, r, http.StatusBadRequest, "totp_not_enrolled", err.Error(), "")
	case errors.Is(err, directory.ErrTOTPAlreadyEnabled):
		writeProblem(w, r, http.StatusConflict, "totp_already_enabled", err.Error(), "")
	default:
		s.writeStoreError(w, r, err)
	}
}
