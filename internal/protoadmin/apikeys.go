package protoadmin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

type createAPIKeyRequest struct {
	Label string `json:"label"`
}

// createAPIKeyResponse is returned exactly once on creation. Future GETs
// against this key do NOT expose the plaintext.
type createAPIKeyResponse struct {
	ID          uint64 `json:"id"`
	PrincipalID uint64 `json:"principal_id"`
	Label       string `json:"label"`
	Key         string `json:"key"`
	CreatedAt   string `json:"created_at"`
}

// generateAPIKey returns a new plaintext key and its SHA-256 hash.
// The plaintext has the "hk_" prefix so operators can identify leaked
// tokens by substring grep.
func generateAPIKey() (plaintext, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("rand: %w", err)
	}
	plaintext = APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return plaintext, HashAPIKey(plaintext), nil
}

func (s *Server) handleListPrincipalAPIKeys(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	// Admin required per spec (GET /principals/{pid}/api-keys is the
	// admin path). Self uses GET /api-keys.
	if !requireAdmin(w, r, caller) {
		return
	}
	rows, err := s.store.Meta().ListAPIKeysByPrincipal(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]apiKeyDTO, 0, len(rows))
	for _, k := range rows {
		items = append(items, toAPIKeyDTO(k))
	}
	writeJSON(w, http.StatusOK, pageDTO[apiKeyDTO]{Items: items, Next: nil})
}

func (s *Server) handleListOwnAPIKeys(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	rows, err := s.store.Meta().ListAPIKeysByPrincipal(r.Context(), caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]apiKeyDTO, 0, len(rows))
	for _, k := range rows {
		items = append(items, toAPIKeyDTO(k))
	}
	writeJSON(w, http.StatusOK, pageDTO[apiKeyDTO]{Items: items, Next: nil})
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	var req createAPIKeyRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Label == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"label is required", "")
		return
	}
	plaintext, hash, err := generateAPIKey()
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.api_key.generate", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"failed to generate key", "")
		return
	}
	inserted, err := s.store.Meta().InsertAPIKey(r.Context(), store.APIKey{
		PrincipalID: pid,
		Hash:        hash,
		Name:        req.Label,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "apikey.create",
		fmt.Sprintf("apikey:%d", inserted.ID),
		store.OutcomeSuccess, "", map[string]string{
			"label":        req.Label,
			"principal_id": strconv.FormatUint(uint64(pid), 10),
		})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/api-keys/%d", inserted.ID))
	writeJSON(w, http.StatusCreated, createAPIKeyResponse{
		ID:          uint64(inserted.ID),
		PrincipalID: uint64(pid),
		Label:       req.Label,
		Key:         plaintext,
		CreatedAt:   inserted.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"api key id must be a positive integer", raw)
		return
	}
	// Load the key to resolve its owner so we can apply self-or-admin.
	keys, err := s.store.Meta().ListAPIKeysByPrincipal(r.Context(), caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	owned := false
	for _, k := range keys {
		if uint64(k.ID) == n {
			owned = true
			break
		}
	}
	if !owned && !caller.Flags.Has(store.PrincipalFlagAdmin) {
		// 404 rather than 403 so non-admins cannot enumerate key ids
		// by probing for 403s.
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"api key not found", "")
		return
	}
	if err := s.store.Meta().DeleteAPIKey(r.Context(), store.APIKeyID(n)); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "apikey.delete",
		fmt.Sprintf("apikey:%d", n),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}
