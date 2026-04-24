package protoadmin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// randRead and encodeBase64URL are thin wrappers so we can swap in a
// deterministic source for tests without widening the production API.
var (
	randRead        = rand.Read
	encodeBase64URL = base64.RawURLEncoding.EncodeToString
)

// handleHealthLive always reports 200. No dependencies.
func (s *Server) handleHealthLive(w http.ResponseWriter, r *http.Request) {
	s.opts.Health.LivenessHandler().ServeHTTP(w, r)
}

// handleHealthReady reports 200 once the server is ready, 503 otherwise.
func (s *Server) handleHealthReady(w http.ResponseWriter, r *http.Request) {
	s.opts.Health.ReadinessHandler().ServeHTTP(w, r)
}

type bootstrapRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

type bootstrapResponse struct {
	PrincipalID     uint64 `json:"principal_id"`
	Email           string `json:"email"`
	InitialPassword string `json:"initial_password"`
	InitialAPIKey   string `json:"initial_api_key"`
	InitialAPIKeyID uint64 `json:"initial_api_key_id"`
}

// handleBootstrap creates the first admin principal along with an
// initial API key and password. Subsequent calls return 409 Conflict
// because the store already contains at least one principal.
//
// Because the endpoint is unauthenticated we rate-limit it aggressively
// per remote IP (one request per 5 minutes by default) to prevent an
// attacker from repeatedly probing while racing an operator who is
// mid-setup.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	// Per-remote rate limit. We key on the remote address portion of
	// r.RemoteAddr (host, not port) so a high-ephemeral-port attacker
	// cannot multiplex.
	remote := remoteHost(r.RemoteAddr)
	if ok, retry := s.bootstrapRL.allow("bootstrap:" + remote); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeProblem(w, r, http.StatusTooManyRequests,
			"rate_limited", "bootstrap rate limit exceeded", "")
		return
	}
	// Precondition: the store must contain zero principals.
	existing, err := s.store.Meta().ListPrincipals(r.Context(), 0, 1)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if len(existing) > 0 {
		writeProblem(w, r, http.StatusConflict, "already_initialised",
			"server already has a principal", "")
		return
	}
	var req bootstrapRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Email == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"email is required", "")
		return
	}
	// Generate a secure random password for the admin.
	password, err := generateBootstrapPassword()
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.bootstrap.password_gen", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"failed to generate password", "")
		return
	}
	pid, err := s.dir.CreatePrincipal(r.Context(), req.Email, password)
	if err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}
	// Promote to admin.
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	p.Flags |= store.PrincipalFlagAdmin
	if req.DisplayName != "" {
		p.DisplayName = req.DisplayName
	}
	if err := s.store.Meta().UpdatePrincipal(r.Context(), p); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Mint the initial API key.
	plaintext, hash, err := generateAPIKey()
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.bootstrap.apikey_gen", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"failed to generate api key", "")
		return
	}
	key, err := s.store.Meta().InsertAPIKey(r.Context(), store.APIKey{
		PrincipalID: pid,
		Hash:        hash,
		Name:        "bootstrap",
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Audit with ActorSystem since the bootstrap endpoint is
	// pre-authentication.
	entry := store.AuditLogEntry{
		At:         s.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    "system",
		Action:     "bootstrap",
		Subject:    fmt.Sprintf("principal:%d", pid),
		RemoteAddr: remote,
		Outcome:    store.OutcomeSuccess,
		Message:    "initial admin created",
	}
	_ = s.store.Meta().AppendAuditLog(r.Context(), entry)
	writeJSON(w, http.StatusCreated, bootstrapResponse{
		PrincipalID:     uint64(pid),
		Email:           p.CanonicalEmail,
		InitialPassword: password,
		InitialAPIKey:   plaintext,
		InitialAPIKeyID: uint64(key.ID),
	})
}

func (s *Server) handleServerStatus(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	uptime := int64(s.clk.Now().Sub(s.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.opts.ServerVersion,
		"uptime_seconds": uptime,
		"ready":          s.opts.Health.Ready(),
		"listeners":      []string{},
		"plugins":        []string{},
		"store_backend":  storeBackendName(s.store),
	})
}

func (s *Server) handleServerConfigCheck(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	// Minimal Phase 1 check: the store is reachable and returns a
	// principal list. A deeper config validator lands with the
	// sysconfig reload path in a later wave.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.store.Meta().ListLocalDomains(ctx); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"checked": []string{"store"},
	})
}

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	q := r.URL.Query()
	filter := store.AuditLogFilter{
		Action: q.Get("action"),
	}
	if raw := q.Get("principal_id"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"principal_id must be a positive integer", raw)
			return
		}
		filter.PrincipalID = store.PrincipalID(n)
	}
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"since must be RFC3339", raw)
			return
		}
		filter.Since = t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"until must be RFC3339", raw)
			return
		}
		filter.Until = t
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return
		}
		filter.Limit = n
	}
	if raw := q.Get("after_id"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"after_id must be a positive integer", raw)
			return
		}
		filter.AfterID = store.AuditLogID(n)
	}
	rows, err := s.store.Meta().ListAuditLog(r.Context(), filter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, auditEntryToMap(e))
	}
	var next *string
	if filter.Limit > 0 && len(rows) == filter.Limit && len(rows) > 0 {
		tok := strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
		next = &tok
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"next":  next,
	})
}

func auditEntryToMap(e store.AuditLogEntry) map[string]any {
	return map[string]any{
		"id":          uint64(e.ID),
		"at":          e.At,
		"actor_kind":  actorKindString(e.ActorKind),
		"actor_id":    e.ActorID,
		"action":      e.Action,
		"subject":     e.Subject,
		"remote_addr": e.RemoteAddr,
		"outcome":     outcomeString(e.Outcome),
		"message":     e.Message,
		"metadata":    e.Metadata,
	}
}

func actorKindString(k store.ActorKind) string {
	switch k {
	case store.ActorPrincipal:
		return "principal"
	case store.ActorAPIKey:
		return "api_key"
	case store.ActorSystem:
		return "system"
	default:
		return "unknown"
	}
}

func outcomeString(o store.AuditOutcome) string {
	switch o {
	case store.OutcomeSuccess:
		return "success"
	case store.OutcomeFailure:
		return "failure"
	default:
		return "unknown"
	}
}

func storeBackendName(st store.Store) string {
	// The store type is an opaque interface; we identify the backend by
	// its Go package name to keep the status endpoint informative
	// without adding an explicit BackendName() method to every Store.
	typeName := fmt.Sprintf("%T", st)
	switch {
	case strContains(typeName, "fakestore"):
		return "fakestore"
	case strContains(typeName, "storesqlite"):
		return "sqlite"
	case strContains(typeName, "storepg"):
		return "postgres"
	}
	return "unknown"
}

// strContains is a tiny substring check kept local to avoid importing
// strings just for one call site.
func strContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// remoteHost strips the port from a RemoteAddr (host:port or [ipv6]:port).
func remoteHost(addr string) string {
	if len(addr) > 0 && addr[0] == '[' {
		for i := 1; i < len(addr); i++ {
			if addr[i] == ']' {
				return addr[1:i]
			}
		}
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// generateBootstrapPassword returns a 24-character urlsafe-base64
// random string, which exceeds MinPasswordLength (12).
func generateBootstrapPassword() (string, error) {
	var b [18]byte
	if _, err := randRead(b[:]); err != nil {
		return "", err
	}
	return encodeBase64URL(b[:]), nil
}
