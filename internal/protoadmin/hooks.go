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
	ID           uint64 `json:"id"`
	OwnerKind    string `json:"owner_kind"`
	OwnerID      string `json:"owner_id"`
	TargetKind   string `json:"target_kind,omitempty"`
	TargetURL    string `json:"target_url"`
	DeliveryMode string `json:"delivery_mode"`
	BodyMode     string `json:"body_mode,omitempty"`
	Active       bool   `json:"active"`
	HMACSecret   string `json:"hmac_secret,omitempty"`
	// Phase 3 Wave 3.5c (REQ-HOOK-EXTRACTED-01..03).  Only meaningful
	// when BodyMode == "extracted"; emitted regardless so receivers can
	// inspect the full row state.
	ExtractedTextMaxBytes int64     `json:"extracted_text_max_bytes,omitempty"`
	TextRequired          bool      `json:"text_required,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func toWebhookDTO(w store.Webhook, includeSecret bool) webhookDTO {
	d := webhookDTO{
		ID:                    uint64(w.ID),
		OwnerKind:             w.OwnerKind.String(),
		OwnerID:               w.OwnerID,
		TargetURL:             w.TargetURL,
		DeliveryMode:          w.DeliveryMode.String(),
		Active:                w.Active,
		ExtractedTextMaxBytes: w.ExtractedTextMaxBytes,
		TextRequired:          w.TextRequired,
		CreatedAt:             w.CreatedAt,
		UpdatedAt:             w.UpdatedAt,
	}
	if w.TargetKind != store.WebhookTargetUnspecified {
		d.TargetKind = w.TargetKind.String()
	}
	if w.BodyMode != store.WebhookBodyModeUnspecified {
		d.BodyMode = w.BodyMode.String()
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

// targetKindFromString parses the REQ-HOOK-02 target.kind vocabulary.
// The legacy {domain, principal} pair maps onto the existing
// owner_kind column for backwards compat; address and synthetic are
// new in Phase 3 Wave 3.5c.
func targetKindFromString(s string) (store.WebhookTargetKind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return store.WebhookTargetUnspecified, true
	case "address":
		return store.WebhookTargetAddress, true
	case "domain":
		return store.WebhookTargetDomain, true
	case "principal":
		return store.WebhookTargetPrincipal, true
	case "synthetic":
		return store.WebhookTargetSynthetic, true
	default:
		return store.WebhookTargetUnspecified, false
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

// bodyModeFromString parses the REQ-HOOK-EXTRACTED-01 body_mode
// vocabulary.  inline / url match the Phase-2 DeliveryMode values;
// extracted is new in Phase 3 Wave 3.5c.
func bodyModeFromString(s string) (store.WebhookBodyMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return store.WebhookBodyModeUnspecified, true
	case "inline":
		return store.WebhookBodyModeInline, true
	case "url", "fetch_url":
		return store.WebhookBodyModeURL, true
	case "extracted":
		return store.WebhookBodyModeExtracted, true
	default:
		return store.WebhookBodyModeUnspecified, false
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
	OwnerKind string `json:"owner_kind"`
	OwnerID   string `json:"owner_id"`
	// TargetKind / TargetValue are the REQ-HOOK-02 surface; either
	// (target_kind, target_value) OR (owner_kind, owner_id) must be
	// supplied.  When TargetKind is "synthetic" the row is stored with
	// owner_kind=domain (best legacy fallback) so existing list paths
	// continue to surface it; the dispatcher consults TargetKind when
	// matching.
	TargetKind   string `json:"target_kind,omitempty"`
	TargetValue  string `json:"target_value,omitempty"`
	TargetURL    string `json:"target_url"`
	HMACSecret   string `json:"hmac_secret,omitempty"`
	DeliveryMode string `json:"delivery_mode,omitempty"`
	BodyMode     string `json:"body_mode,omitempty"`
	// REQ-HOOK-EXTRACTED-01..03 fields.  ExtractedTextMaxBytes is
	// clamped at MaxExtractedTextMaxBytes; values <= 0 yield the
	// package default at read time.
	ExtractedTextMaxBytes int64              `json:"extracted_text_max_bytes,omitempty"`
	TextRequired          bool               `json:"text_required,omitempty"`
	RetryPolicy           *store.RetryPolicy `json:"retry_policy,omitempty"`
	Active                *bool              `json:"active,omitempty"`
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
	// Resolve the target.{kind,value} surface (REQ-HOOK-02) into the
	// row's owner_kind / owner_id / target_kind columns.  Callers may
	// pass either (target_kind, target_value) or the legacy
	// (owner_kind, owner_id) pair; the former takes precedence.
	var (
		ownerKind  store.WebhookOwnerKind
		ownerID    string
		targetKind store.WebhookTargetKind
	)
	if req.TargetKind != "" || req.TargetValue != "" {
		tk, ok := targetKindFromString(req.TargetKind)
		if !ok || tk == store.WebhookTargetUnspecified {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"target_kind must be one of 'address' | 'domain' | 'principal' | 'synthetic'", req.TargetKind)
			return
		}
		if req.TargetValue == "" {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"target_value is required when target_kind is set", "")
			return
		}
		targetKind = tk
		ownerID = req.TargetValue
		switch tk {
		case store.WebhookTargetDomain, store.WebhookTargetAddress, store.WebhookTargetSynthetic:
			ownerKind = store.WebhookOwnerDomain
		case store.WebhookTargetPrincipal:
			ownerKind = store.WebhookOwnerPrincipal
		}
	} else {
		ok, valid := ownerKindFromString(req.OwnerKind)
		if !valid || ok == store.WebhookOwnerUnknown {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"owner_kind must be 'domain' or 'principal'", req.OwnerKind)
			return
		}
		if req.OwnerID == "" {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"owner_id is required", "")
			return
		}
		ownerKind = ok
		ownerID = req.OwnerID
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
	bodyMode, ok := bodyModeFromString(req.BodyMode)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"body_mode must be one of 'inline' | 'url' | 'extracted'", req.BodyMode)
		return
	}
	if req.ExtractedTextMaxBytes < 0 {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"extracted_text_max_bytes must be non-negative", "")
		return
	}
	if req.ExtractedTextMaxBytes > store.MaxExtractedTextMaxBytes {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"extracted_text_max_bytes exceeds the operator-set ceiling", "")
		return
	}
	if req.TextRequired && bodyMode != store.WebhookBodyModeExtracted {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"text_required only applies when body_mode='extracted'", "")
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
		OwnerKind:             ownerKind,
		OwnerID:               ownerID,
		TargetKind:            targetKind,
		TargetURL:             req.TargetURL,
		HMACSecret:            secretBytes,
		DeliveryMode:          mode,
		BodyMode:              bodyMode,
		ExtractedTextMaxBytes: req.ExtractedTextMaxBytes,
		TextRequired:          req.TextRequired,
		Active:                active,
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
	TargetURL             *string            `json:"target_url,omitempty"`
	DeliveryMode          *string            `json:"delivery_mode,omitempty"`
	BodyMode              *string            `json:"body_mode,omitempty"`
	ExtractedTextMaxBytes *int64             `json:"extracted_text_max_bytes,omitempty"`
	TextRequired          *bool              `json:"text_required,omitempty"`
	RetryPolicy           *store.RetryPolicy `json:"retry_policy,omitempty"`
	Active                *bool              `json:"active,omitempty"`
	RotateSecret          bool               `json:"rotate_secret,omitempty"`
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
	if req.BodyMode != nil {
		bm, ok := bodyModeFromString(*req.BodyMode)
		if !ok {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"body_mode must be one of 'inline' | 'url' | 'extracted'", *req.BodyMode)
			return
		}
		hook.BodyMode = bm
	}
	if req.ExtractedTextMaxBytes != nil {
		v := *req.ExtractedTextMaxBytes
		if v < 0 {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"extracted_text_max_bytes must be non-negative", "")
			return
		}
		if v > store.MaxExtractedTextMaxBytes {
			writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
				"extracted_text_max_bytes exceeds the operator-set ceiling", "")
			return
		}
		hook.ExtractedTextMaxBytes = v
	}
	if req.TextRequired != nil {
		hook.TextRequired = *req.TextRequired
	}
	// REQ-HOOK-EXTRACTED-03 says text_required only applies in
	// extracted mode; reject combinations that no longer hold after
	// the patch is applied.
	if hook.TextRequired && hook.EffectiveBodyMode() != store.WebhookBodyModeExtracted {
		writeProblem(w, r, http.StatusBadRequest, "hooks/validation_failed",
			"text_required only applies when body_mode='extracted'", "")
		return
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
