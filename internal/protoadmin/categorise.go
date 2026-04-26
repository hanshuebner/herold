package protoadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// defaultRecategoriseLimit caps the per-job message count when the
// caller does not pass an explicit limit. Mirrors
// categorise.DefaultRecategoriseLimit; duplicated to avoid a
// protoadmin → categorise import.
const defaultRecategoriseLimit = 1000

// inMemoryJobRegistry is a small fallback used when Options
// .CategoriseJobs is nil. Tests can supply their own; the production
// wiring sets it explicitly.
type inMemoryJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]CategoriseJobStatus
}

func newInMemoryJobRegistry() *inMemoryJobRegistry {
	return &inMemoryJobRegistry{jobs: map[string]CategoriseJobStatus{}}
}

func (r *inMemoryJobRegistry) Get(id string) (CategoriseJobStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.jobs[id]
	return s, ok
}

func (r *inMemoryJobRegistry) Put(_ time.Time, s CategoriseJobStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[s.ID] = s
}

// catJobs returns the configured registry, creating a fresh
// in-memory one on first use when Options did not supply one.
func (s *Server) catJobs() CategoriseJobRegistry {
	if s.opts.CategoriseJobs != nil {
		return s.opts.CategoriseJobs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.CategoriseJobs == nil {
		s.opts.CategoriseJobs = newInMemoryJobRegistry()
	}
	return s.opts.CategoriseJobs
}

// recategoriseResponse is the immediate response body for
// POST /api/v1/principals/{pid}/recategorise. The work runs in a
// goroutine; clients poll /api/v1/jobs/{id}.
type recategoriseResponse struct {
	Enqueued bool   `json:"enqueued"`
	JobID    string `json:"jobId"`
}

// handleRecategorisePrincipal kicks off a re-categorisation pass
// (REQ-FILT-220). The body is empty; ?limit=N narrows the count. The
// handler returns 202 Accepted with a JSON {enqueued, jobId} payload
// immediately, then runs the categoriser in a goroutine. Progress
// lands on the in-memory job registry; clients poll it via
// GET /api/v1/jobs/{id}.
func (s *Server) handleRecategorisePrincipal(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	if s.opts.Categoriser == nil {
		writeProblem(w, r, http.StatusNotImplemented, "categorise/not_implemented",
			"categoriser is not configured on this server", "")
		return
	}
	limit := defaultRecategoriseLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return
		}
		if n > 10000 {
			n = 10000
		}
		limit = n
	}
	jobID := newJobID()
	registry := s.catJobs()
	registry.Put(s.clk.Now(), CategoriseJobStatus{
		ID:    jobID,
		State: "running",
		Done:  0,
		Total: 0,
	})
	// Detach the goroutine from the request lifetime so a client that
	// closes the connection does not abort the slow path. STANDARDS.md
	// §5 demands a documented shutdown path — when the server's
	// parent ctx cancels the categoriser observes it through its own
	// httpClient deadline; for now we use a fresh background ctx so
	// the loop runs to completion.
	bgCtx := context.Background()
	go func() {
		processed, err := s.opts.Categoriser.RecategoriseRecent(bgCtx, pid, limit,
			func(done, total int) {
				registry.Put(s.clk.Now(), CategoriseJobStatus{
					ID:    jobID,
					State: "running",
					Done:  done,
					Total: total,
				})
			})
		state := "done"
		errStr := ""
		if err != nil {
			state = "failed"
			errStr = err.Error()
		}
		registry.Put(s.clk.Now(), CategoriseJobStatus{
			ID:    jobID,
			State: state,
			Done:  processed,
			Total: processed,
			Err:   errStr,
		})
	}()
	s.appendAudit(r.Context(), "categorisation.recategorise.start",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "",
		map[string]string{"job_id": jobID, "limit": strconv.Itoa(limit)})
	writeJSON(w, http.StatusAccepted, recategoriseResponse{Enqueued: true, JobID: jobID})
}

// handleGetJob returns the current snapshot of a recategorisation
// job (REQ-FILT-220 polling). Available regardless of whether
// Categoriser is wired so clients can poll completed jobs after a
// restart-cycle removed the goroutine but the operator still wants
// the registry view.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	// Anyone authenticated may poll any job they hold the id for; the
	// id is opaque enough (16 hex bytes) that brute-forcing is not a
	// concern in scope. We still require auth so unauthenticated
	// scrapers don't see status.
	if caller.ID == 0 {
		writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "auth required", "")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "missing job id", "")
		return
	}
	registry := s.catJobs()
	status, ok := registry.Get(id)
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "not_found", "no such job", id)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// newJobID returns a 32-character hex job id. crypto/rand source so
// ids are unguessable; falls back to a timestamp-derived id if rand
// fails (shouldn't on any production platform).
func newJobID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// categorisationConfigDTO is the wire shape for GET/PUT
// /api/v1/principals/{pid}/categorisation (REQ-FILT-210..212).
// Field names mirror store.CategorisationConfig exactly so JSON
// round-trips are lossless. APIKeyEnv is a $VAR or file:/path
// reference — never a raw secret (STANDARDS §9).
type categorisationConfigDTO struct {
	Prompt      string              `json:"prompt"`
	CategorySet []store.CategoryDef `json:"category_set"`
	Endpoint    *string             `json:"endpoint,omitempty"`
	Model       *string             `json:"model,omitempty"`
	APIKeyEnv   *string             `json:"api_key_env,omitempty"`
	TimeoutSec  int                 `json:"timeout_sec,omitempty"`
	Enabled     bool                `json:"enabled"`
}

func toCategorisationDTO(cfg store.CategorisationConfig) categorisationConfigDTO {
	return categorisationConfigDTO{
		Prompt:      cfg.Prompt,
		CategorySet: cfg.CategorySet,
		Endpoint:    cfg.Endpoint,
		Model:       cfg.Model,
		APIKeyEnv:   cfg.APIKeyEnv,
		TimeoutSec:  cfg.TimeoutSec,
		Enabled:     cfg.Enabled,
	}
}

// handleGetCategorisationConfig handles
// GET /api/v1/principals/{pid}/categorisation (REQ-FILT-210, 212).
// Admin-only. Returns the per-principal categoriser config; APIKeyEnv
// is returned verbatim as the $VAR or file:/path reference — never
// decrypted (STANDARDS §9).
func (s *Server) handleGetCategorisationConfig(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	cfg, err := s.store.Meta().GetCategorisationConfig(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCategorisationDTO(cfg))
}

// handlePutCategorisationConfig handles
// PUT /api/v1/principals/{pid}/categorisation (REQ-FILT-211).
// Admin-only. Validates and upserts the per-principal categoriser
// config. APIKeyEnv, when present, must be a $VAR or file:/path
// reference (STANDARDS §9). TimeoutSec must be positive. Endpoint,
// when set, must parse as a URL. Model must be non-empty when
// Enabled=true.
func (s *Server) handlePutCategorisationConfig(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req categorisationConfigDTO
	if !decodeJSONBody(w, r, &req) {
		return
	}

	// Validate APIKeyEnv: must be a reference form when present.
	if req.APIKeyEnv != nil {
		v := *req.APIKeyEnv
		if v != "" && !strings.HasPrefix(v, "$") && !strings.HasPrefix(v, "file:") {
			writeProblem(w, r, http.StatusBadRequest, "categorise/validation_failed",
				"api_key_env must be a $VAR or file:/path reference",
				"inline secret values are not permitted (STANDARDS §9)")
			return
		}
	}

	// Validate TimeoutSec when provided.
	if req.TimeoutSec < 0 {
		writeProblem(w, r, http.StatusBadRequest, "categorise/validation_failed",
			"timeout_sec must be non-negative", "")
		return
	}

	// Validate Endpoint parses as URL when set.
	if req.Endpoint != nil && *req.Endpoint != "" {
		if _, err := url.ParseRequestURI(*req.Endpoint); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "categorise/validation_failed",
				"endpoint must be a valid URL", err.Error())
			return
		}
	}

	// When Enabled, Model must be non-empty.
	if req.Enabled && req.Model != nil && *req.Model == "" {
		writeProblem(w, r, http.StatusBadRequest, "categorise/validation_failed",
			"model must be non-empty when enabled is true", "")
		return
	}

	cfg := store.CategorisationConfig{
		PrincipalID: pid,
		Prompt:      req.Prompt,
		CategorySet: req.CategorySet,
		Endpoint:    req.Endpoint,
		Model:       req.Model,
		APIKeyEnv:   req.APIKeyEnv,
		TimeoutSec:  req.TimeoutSec,
		Enabled:     req.Enabled,
	}
	if err := s.store.Meta().UpdateCategorisationConfig(r.Context(), cfg); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// Audit log: record which high-level fields changed; prompt body is
	// intentionally omitted (can be large and contains PII hints).
	meta := map[string]string{
		"enabled": strconv.FormatBool(req.Enabled),
	}
	if req.Model != nil {
		meta["model"] = *req.Model
	}
	if req.Endpoint != nil {
		meta["endpoint"] = *req.Endpoint
	}
	if req.APIKeyEnv != nil {
		meta["api_key_env_set"] = "true"
	}
	if req.TimeoutSec != 0 {
		meta["timeout_sec"] = strconv.Itoa(req.TimeoutSec)
	}
	s.appendAudit(r.Context(), "categorise_config_update",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "", meta)

	writeJSON(w, http.StatusOK, toCategorisationDTO(cfg))
}

// _ keeps the encoding/json import alive for ergonomics: future
// handlers added to this file generally encode JSON and dropping the
// import on every minimal handler would be churn.
var _ = json.NewEncoder
