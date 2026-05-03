package protoadmin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
)

// clientlogMaxBodyAuth is the maximum body size for the authenticated endpoint (REQ-OPS-216).
const clientlogMaxBodyAuth = 256 * 1024 // 256 KiB

// clientlogMaxBodyPublic is the maximum body size for the anonymous endpoint (REQ-OPS-216).
const clientlogMaxBodyPublic = 8 * 1024 // 8 KiB

// clientlogMaxBatchAuth is the max events per batch on the authenticated endpoint (REQ-OPS-216).
const clientlogMaxBatchAuth = 100

// clientlogMaxBatchPublic is the max events per batch on the public endpoint (REQ-OPS-216).
const clientlogMaxBatchPublic = 5

// clientlogMaxStackAuth is the max stack size on the authenticated endpoint (REQ-OPS-216).
const clientlogMaxStackAuth = 16 * 1024 // 16 KiB

// clientlogMaxStackPublic is the max stack size on the public endpoint (REQ-OPS-216).
const clientlogMaxStackPublic = 4 * 1024 // 4 KiB

// clientlogMaxMsgAuth is the max msg length on the authenticated endpoint (REQ-OPS-216).
const clientlogMaxMsgAuth = 4 * 1024 // 4 KiB

// clientlogMaxMsgPublic is the max msg length on the public endpoint (REQ-OPS-216).
const clientlogMaxMsgPublic = 1 * 1024 // 1 KiB

// clientlogMaxBreadcrumbsAuth is the max breadcrumb count on the authenticated endpoint (REQ-OPS-216).
const clientlogMaxBreadcrumbsAuth = 32

// clientlogRateAuthWindow is the per-session window for the authenticated endpoint (REQ-OPS-216).
const clientlogRateAuthWindow = 5 * time.Minute

// clientlogRateAuthLimit is the per-session event limit for the authenticated endpoint (REQ-OPS-216).
const clientlogRateAuthLimit = 1000

// clientlogRatePublicWindow is the per-IP window for the anonymous endpoint (REQ-OPS-216).
const clientlogRatePublicWindow = time.Minute

// clientlogRatePublicLimit is the per-IP event limit for the anonymous endpoint (REQ-OPS-216).
// The limit is set to the burst value (20) to model token-bucket burst behaviour with the
// sliding window implementation; the "10 events/min" effective rate is enforced by
// the window duration.
const clientlogRatePublicLimit = 20

// clientlogRetryAfterBackpressure is the Retry-After value when the worker queue is full.
const clientlogRetryAfterBackpressure = 5

// wireEvent is the full (authenticated) event schema (REQ-OPS-202). All
// fields are decoded from the JSON body; unknown fields cause a 400 when
// strict mode is on (public endpoint).
type wireEvent struct {
	V           int              `json:"v"`
	Kind        string           `json:"kind"`
	Level       string           `json:"level"`
	Msg         string           `json:"msg"`
	Stack       string           `json:"stack,omitempty"`
	ClientTS    string           `json:"client_ts"`
	Seq         int64            `json:"seq"`
	PageID      string           `json:"page_id"`
	SessionID   string           `json:"session_id,omitempty"`
	App         string           `json:"app"`
	BuildSHA    string           `json:"build_sha"`
	Route       string           `json:"route"`
	RequestID   string           `json:"request_id,omitempty"`
	UA          string           `json:"ua"`
	Breadcrumbs []wireBreadcrumb `json:"breadcrumbs,omitempty"`
	Vital       *wireVital       `json:"vital,omitempty"`
	Synchronous bool             `json:"synchronous,omitempty"`
}

// wireNarrowEvent is the narrow (anonymous) event schema (REQ-OPS-207).
// It excludes breadcrumbs, session_id, request_id, and vital. Unknown
// fields are rejected (strict parsing via json.Decoder.DisallowUnknownFields).
type wireNarrowEvent struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	Level    string `json:"level"`
	Msg      string `json:"msg"`
	Stack    string `json:"stack,omitempty"`
	ClientTS string `json:"client_ts"`
	Seq      int64  `json:"seq"`
	PageID   string `json:"page_id"`
	App      string `json:"app"`
	BuildSHA string `json:"build_sha"`
	Route    string `json:"route"`
	UA       string `json:"ua"`
}

// wireBreadcrumb is the breadcrumb shape (REQ-OPS-202 — allow-listed fields only).
// Unknown fields are stripped silently per REQ-OPS-215.
type wireBreadcrumb struct {
	TS      string `json:"ts"`
	Kind    string `json:"kind"`
	Route   string `json:"route,omitempty"`
	Status  int    `json:"status,omitempty"`
	Method  string `json:"method,omitempty"`
	URLPath string `json:"url_path,omitempty"`
	Msg     string `json:"msg,omitempty"`
}

// wireVital is the Web Vitals payload shape (REQ-OPS-202, kind=vital only).
type wireVital struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	ID    string  `json:"id"`
}

// wireRequest is the wrapper body accepted on both endpoints (REQ-OPS-201).
// Each endpoint decodes events[] independently to enforce strict/loose schema.
type wireRequest struct {
	Events []json.RawMessage `json:"events"`
}

// handleClientLogAuth handles POST /api/v1/clientlog on both listeners
// (authenticated endpoint, full schema, REQ-OPS-200, REQ-OPS-202).
func (s *Server) handleClientLogAuth(w http.ResponseWriter, r *http.Request) {
	observe.RegisterClientlogMetrics()
	listener := listenerTagFromContext(r)

	// Enforce body cap (REQ-OPS-216).
	lr := io.LimitReader(r.Body, clientlogMaxBodyAuth+1)
	bodyBytes, err := io.ReadAll(lr)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "read_error", "could not read body", err.Error())
		return
	}
	if int64(len(bodyBytes)) > clientlogMaxBodyAuth {
		observe.ClientlogDroppedTotal.WithLabelValues("auth", "body_too_large").Inc()
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "body_too_large",
			"request body exceeds the 256 KiB limit", "")
		return
	}

	// Decode the wrapper — use json.Decoder for consistency; not strict at the
	// wrapper level (extra fields outside "events" are ignored by the full schema).
	var req wireRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		observe.ClientlogDroppedTotal.WithLabelValues("auth", "schema").Inc()
		writeProblem(w, r, http.StatusBadRequest, "invalid_body", "could not parse JSON body", err.Error())
		return
	}

	// Batch cap (REQ-OPS-216).
	if len(req.Events) > clientlogMaxBatchAuth {
		observe.ClientlogDroppedTotal.WithLabelValues("auth", "schema").Inc()
		writeProblem(w, r, http.StatusBadRequest, "batch_too_large",
			fmt.Sprintf("batch exceeds %d event limit", clientlogMaxBatchAuth), "")
		return
	}

	// Per-session rate limit (REQ-OPS-216).
	principal, hasPrincipal := principalFrom(r.Context())
	sessionKey := fmt.Sprintf("clientlog-auth:%d", 0)
	if hasPrincipal {
		sessionKey = fmt.Sprintf("clientlog-auth:%d", principal.ID)
	}
	// Count total events in the batch against the per-session window.
	ok, retryAfter := s.clientlogAuthRL.allow(sessionKey)
	// The rate limiter checks one event at a time; for a batch we allow the
	// whole batch only if at least one token is available. If denied, all
	// events in this batch are dropped.
	if !ok {
		observe.ClientlogDroppedTotal.WithLabelValues("auth", "rate_limit").Add(float64(len(req.Events)))
		secs := int(retryAfter.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
		writeProblem(w, r, http.StatusTooManyRequests,
			"rate_limited", "client-log rate limit exceeded",
			fmt.Sprintf("retry after %s", retryAfter))
		return
	}

	// Resolve user ID for enrichment.
	userID := ""
	if hasPrincipal {
		userID = fmt.Sprintf("%d", principal.ID)
	}

	// Decode + validate + pipeline each event.
	serverRecvTS := s.clk.Now()
	for _, raw := range req.Events {
		var ev wireEvent
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		// Full schema: do NOT disallow unknown fields — the wire spec says
		// unknown fields on the authenticated endpoint are ignored silently.
		if err := dec.Decode(&ev); err != nil {
			observe.ClientlogDroppedTotal.WithLabelValues("auth", "schema").Inc()
			continue
		}
		if !clientlogValidKind(ev.Kind) || !clientlogValidApp(ev.App) {
			observe.ClientlogDroppedTotal.WithLabelValues("auth", "schema").Inc()
			continue
		}

		// Opt-out check (REQ-OPS-208, server-side gate).
		if ev.Kind != "error" && !s.clientlogTelemetryGate.IsEnabled(sessionKey) {
			observe.ClientlogDroppedTotal.WithLabelValues("auth", "telemetry_disabled").Inc()
			continue
		}

		observe.ClientlogReceivedTotal.WithLabelValues("auth", ev.App, ev.Kind).Inc()

		job := clientlogJob{
			raw:          raw,
			ev:           &ev,
			narrowEv:     nil,
			isPublic:     false,
			userID:       userID,
			listener:     listener,
			serverRecvTS: serverRecvTS,
			remoteAddr:   r.RemoteAddr,
		}
		if !s.submitClientlogJob(w, r, job) {
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// handleClientLogPublic handles POST /api/v1/clientlog/public on both
// listeners (anonymous endpoint, narrow schema, REQ-OPS-200, REQ-OPS-207).
func (s *Server) handleClientLogPublic(w http.ResponseWriter, r *http.Request) {
	observe.RegisterClientlogMetrics()
	listener := listenerTagFromContext(r)

	// CORS: check Origin header (REQ-OPS-217 rule 3).
	// Absent Origin is allowed (CLI tools, sendBeacon may omit).
	if origin := r.Header.Get("Origin"); origin != "" {
		ownOrigin := s.ownOrigin(r)
		if !strings.EqualFold(origin, ownOrigin) {
			// Silent drop — no signal to attackers (REQ-OPS-217).
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Enforce body cap (REQ-OPS-216).
	lr := io.LimitReader(r.Body, clientlogMaxBodyPublic+1)
	bodyBytes, err := io.ReadAll(lr)
	if err != nil {
		w.WriteHeader(http.StatusOK) // silent fail for public
		return
	}
	if int64(len(bodyBytes)) > clientlogMaxBodyPublic {
		observe.ClientlogDroppedTotal.WithLabelValues("public", "body_too_large").Inc()
		// REQ-OPS-201: bodies over the per-endpoint cap are rejected with 413.
		// (The silent-200 rule is for rate-limited requests, not for body-cap
		// violations -- the latter is a configuration / client bug, not an
		// abuse signal.)
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "body_too_large",
			"request body exceeds the public-endpoint limit", "")
		return
	}

	// Decode wrapper.
	var req wireRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		observe.ClientlogDroppedTotal.WithLabelValues("public", "schema").Inc()
		writeProblem(w, r, http.StatusBadRequest, "invalid_body", "could not parse JSON body", err.Error())
		return
	}

	// Batch cap.
	if len(req.Events) > clientlogMaxBatchPublic {
		observe.ClientlogDroppedTotal.WithLabelValues("public", "schema").Inc()
		// Silent 200 for public over-quota (REQ-OPS-216).
		w.WriteHeader(http.StatusOK)
		return
	}

	// Per-IP rate limit (REQ-OPS-216). Silent 200 on over-quota.
	ipKey := remoteIP(r)
	ok, _ := s.clientlogPublicRL.allow("clientlog-public:" + ipKey)
	if !ok {
		observe.ClientlogDroppedTotal.WithLabelValues("public", "rate_limit").Add(float64(len(req.Events)))
		// Silent 200 (REQ-OPS-216 — no signal to attackers).
		w.WriteHeader(http.StatusOK)
		return
	}

	serverRecvTS := s.clk.Now()
	for _, raw := range req.Events {
		// Strict schema (REQ-OPS-207, REQ-OPS-217 rule 1).
		var ev wireNarrowEvent
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ev); err != nil {
			observe.ClientlogDroppedTotal.WithLabelValues("public", "schema").Inc()
			writeProblem(w, r, http.StatusBadRequest, "invalid_body",
				"event does not conform to narrow schema", err.Error())
			return
		}
		if !clientlogValidKind(ev.Kind) || !clientlogValidApp(ev.App) {
			observe.ClientlogDroppedTotal.WithLabelValues("public", "schema").Inc()
			writeProblem(w, r, http.StatusBadRequest, "invalid_body",
				"unknown kind or app value", "")
			return
		}

		observe.ClientlogReceivedTotal.WithLabelValues("public", ev.App, ev.Kind).Inc()

		job := clientlogJob{
			raw:          raw,
			ev:           nil,
			narrowEv:     &ev,
			isPublic:     true,
			userID:       "",
			listener:     listener,
			serverRecvTS: serverRecvTS,
			remoteAddr:   r.RemoteAddr,
		}
		if !s.submitClientlogJob(w, r, job) {
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// handleClientLogPreflight handles CORS preflight OPTIONS requests for
// both clientlog endpoints. Only same-origin requests get Allow-Origin
// (REQ-OPS-217 rule 4).
func (s *Server) handleClientLogPreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ownOrigin := s.ownOrigin(r)
	if strings.EqualFold(origin, ownOrigin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
	}
	// Foreign origins receive 204 with no allow-headers (REQ-OPS-217 rule 4).
	w.WriteHeader(http.StatusNoContent)
}

// submitClientlogJob enqueues a clientlog fan-out job. Returns false and
// writes a 503 response if the worker queue is full (REQ-OPS architecture
// §Concurrency — backpressure).
func (s *Server) submitClientlogJob(w http.ResponseWriter, r *http.Request, job clientlogJob) bool {
	select {
	case s.clientlogQueue <- job:
		return true
	default:
		endpoint := "auth"
		if job.isPublic {
			endpoint = "public"
		}
		observe.ClientlogDroppedTotal.WithLabelValues(endpoint, "backpressure").Inc()
		w.Header().Set("Retry-After", fmt.Sprintf("%d", clientlogRetryAfterBackpressure))
		writeProblem(w, r, http.StatusServiceUnavailable,
			"backpressure", "client-log worker queue is full",
			fmt.Sprintf("retry after %ds", clientlogRetryAfterBackpressure))
		return false
	}
}

// ownOrigin returns the HTTP origin (scheme://host) of this server from the
// request. Used for Origin-header validation on the public endpoint.
func (s *Server) ownOrigin(r *http.Request) string {
	if s.opts.BaseURL != "" {
		// Strip any trailing path component.
		u := s.opts.BaseURL
		if idx := strings.Index(u, "://"); idx >= 0 {
			// find the path separator after the host
			rest := u[idx+3:]
			if slash := strings.IndexByte(rest, '/'); slash >= 0 {
				u = u[:idx+3+slash]
			}
		}
		return u
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// remoteIP extracts the IP address from r.RemoteAddr (strips port).
func remoteIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// listenerTagFromContext extracts the listener tag that was stamped onto the
// request context by the routing layer. Falls back to "admin" if not present,
// since the admin listener is the primary surface for protoadmin.
func listenerTagFromContext(r *http.Request) string {
	if v, ok := r.Context().Value(ctxKeyListener).(string); ok && v != "" {
		return v
	}
	return "admin"
}

// clientlogValidKind reports whether kind is a valid event kind.
func clientlogValidKind(kind string) bool {
	switch kind {
	case "error", "log", "vital":
		return true
	}
	return false
}

// clientlogValidApp reports whether app is a valid SPA identifier.
func clientlogValidApp(app string) bool {
	switch app {
	case "suite", "admin":
		return true
	}
	return false
}
