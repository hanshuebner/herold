package protoadmin

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// webhookDTO is the wire representation of a Webhook row. The HMAC
// secret is intentionally redacted on every read; the plaintext is
// returned ONCE on POST and after a rotate-secret update.
type webhookDTO struct {
	ID           uint64    `json:"id"`
	OwnerKind    string    `json:"owner_kind"`
	OwnerID      string    `json:"owner_id"`
	TargetURL    string    `json:"target_url"`
	DeliveryMode string    `json:"delivery_mode"`
	Active       bool      `json:"active"`
	HMACSecret   string    `json:"hmac_secret,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toWebhookDTO(w store.Webhook, includeSecret bool) webhookDTO {
	d := webhookDTO{
		ID:           uint64(w.ID),
		OwnerKind:    w.OwnerKind.String(),
		OwnerID:      w.OwnerID,
		TargetURL:    w.TargetURL,
		DeliveryMode: w.DeliveryMode.String(),
		Active:       w.Active,
		CreatedAt:    w.CreatedAt,
		UpdatedAt:    w.UpdatedAt,
	}
	if includeSecret {
		d.HMACSecret = base64.RawStdEncoding.EncodeToString(w.HMACSecret)
	}
	return d
}

func ownerKindFromString(s string) (store.WebhookOwnerKind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return store.WebhookOwnerUnknown, true
	case "domain":
		return store.WebhookOwnerDomain, true
	case "principal":
		return store.WebhookOwnerPrincipal, true
	default:
		return store.WebhookOwnerUnknown, false
	}
}

func deliveryModeFromString(s string) (store.DeliveryMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "inline":
		return store.DeliveryModeInline, true
	case "fetch_url":
		return store.DeliveryModeFetchURL, true
	default:
		return store.DeliveryModeUnknown, false
	}
}

func parseWebhookID(w http.ResponseWriter, r *http.Request) (store.WebhookID, bool) {
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "hooks/invalid_id",
			"webhook id must be a positive integer", raw)
		return 0, false
	}
	return store.WebhookID(n), true
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	q := r.URL.Query()
	kind, ok := ownerKindFromString(q.Get("owner_kind"))
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "hooks/invalid_owner_kind",
			"owner_kind must be 'domain' or 'principal'", q.Get("owner_kind"))
		return
	}
	rows, err := s.store.Meta().ListWebhooks(r.Context(), kind, q.Get("owner_id"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]webhookDTO, 0, len(rows))
	for _, h := range rows {
		items = append(items, toWebhookDTO(h, false))
	}
	writeJSON(w, http.StatusOK, pageDTO[webhookDTO]{Items: items, Next: nil})
}

func (s *Server) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	hook, err := s.store.Meta().GetWebhook(r.Context(), id)
	if err != nil {
		s.writeHookError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookDTO(hook, false))
}

type createWebhookRequest struct {
	OwnerKind    string             `json:"owner_kind"`
	OwnerID      string             `json:"owner_id"`
	TargetURL    string             `json:"target_url"`
	HMACSecret   string             `json:"hmac_secret,omitempty"`
	DeliveryMode string             `json:"delivery_mode,omitempty"`
	RetryPolicy  *store.RetryPolicy `json:"retry_policy,omitempty"`
	Active       *bool              `json:"active,omitempty"`
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createWebhookRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	kind, ok := ownerKindFromString(req.OwnerKind)
	if !ok || kind == store.WebhookOwnerUnknown {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"owner_kind must be 'domain' or 'principal'", req.OwnerKind)
		return
	}
	if req.OwnerID == "" {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"owner_id is required", "")
		return
	}
	if !strings.HasPrefix(req.TargetURL, "https://") && !strings.HasPrefix(req.TargetURL, "http://") {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"target_url must be an http(s) URL", req.TargetURL)
		return
	}
	mode, ok := deliveryModeFromString(req.DeliveryMode)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"delivery_mode must be 'inline' or 'fetch_url'", req.DeliveryMode)
		return
	}
	var secretBytes []byte
	if req.HMACSecret != "" {
		decoded, err := base64.RawStdEncoding.DecodeString(req.HMACSecret)
		if err != nil {
			// Be lenient: accept raw text too.
			secretBytes = []byte(req.HMACSecret)
		} else {
			secretBytes = decoded
		}
	} else {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "hooks/secret_generation_failed",
				err.Error(), "")
			return
		}
		secretBytes = b[:]
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	hook := store.Webhook{
		OwnerKind:    kind,
		OwnerID:      req.OwnerID,
		TargetURL:    req.TargetURL,
		HMACSecret:   secretBytes,
		DeliveryMode: mode,
		Active:       active,
	}
	if req.RetryPolicy != nil {
		hook.RetryPolicy = *req.RetryPolicy
	}
	inserted, err := s.store.Meta().InsertWebhook(r.Context(), hook)
	if err != nil {
		s.writeHookError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "hook.create",
		fmt.Sprintf("webhook:%d", inserted.ID),
		store.OutcomeSuccess, "", map[string]string{
			"owner_kind": inserted.OwnerKind.String(),
			"owner_id":   inserted.OwnerID,
		})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/webhooks/%d", inserted.ID))
	// Include the plaintext secret ONCE in the response so the operator can
	// configure the receiver. Subsequent reads omit it.
	writeJSON(w, http.StatusCreated, toWebhookDTO(inserted, true))
}

type patchWebhookRequest struct {
	TargetURL    *string            `json:"target_url,omitempty"`
	DeliveryMode *string            `json:"delivery_mode,omitempty"`
	RetryPolicy  *store.RetryPolicy `json:"retry_policy,omitempty"`
	Active       *bool              `json:"active,omitempty"`
	RotateSecret bool               `json:"rotate_secret,omitempty"`
}

func (s *Server) handlePatchWebhook(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	hook, err := s.store.Meta().GetWebhook(r.Context(), id)
	if err != nil {
		s.writeHookError(w, r, err)
		return
	}
	var req patchWebhookRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.TargetURL != nil {
		if !strings.HasPrefix(*req.TargetURL, "https://") && !strings.HasPrefix(*req.TargetURL, "http://") {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"target_url must be an http(s) URL", *req.TargetURL)
			return
		}
		hook.TargetURL = *req.TargetURL
	}
	if req.DeliveryMode != nil {
		mode, ok := deliveryModeFromString(*req.DeliveryMode)
		if !ok {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"delivery_mode must be 'inline' or 'fetch_url'", *req.DeliveryMode)
			return
		}
		hook.DeliveryMode = mode
	}
	if req.RetryPolicy != nil {
		hook.RetryPolicy = *req.RetryPolicy
	}
	if req.Active != nil {
		hook.Active = *req.Active
	}
	rotated := false
	if req.RotateSecret {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "hooks/secret_generation_failed",
				err.Error(), "")
			return
		}
		hook.HMACSecret = b[:]
		rotated = true
	}
	if err := s.store.Meta().UpdateWebhook(r.Context(), hook); err != nil {
		s.writeHookError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "hook.update",
		fmt.Sprintf("webhook:%d", hook.ID),
		store.OutcomeSuccess, "", nil)
	updated, err := s.store.Meta().GetWebhook(r.Context(), hook.ID)
	if err != nil {
		s.writeHookError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookDTO(updated, rotated))
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	if err := s.store.Meta().DeleteWebhook(r.Context(), id); err != nil {
		s.writeHookError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "hook.delete",
		fmt.Sprintf("webhook:%d", id), store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeHookError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "hooks/not_found", err.Error(), "")
	case errors.Is(err, store.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "hooks/conflict", err.Error(), "")
	default:
		s.writeStoreError(w, r, err)
	}
}
