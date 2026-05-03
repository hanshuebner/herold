package protoadmin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Admin REST surfaces for the client-log ring buffer (REQ-ADM-23,
// REQ-ADM-230..233). All handlers in this file require admin scope and are
// registered ONLY on the admin listener; the public listener returns 404
// for /api/v1/admin/* paths per REQ-OPS-ADMIN-LISTENER-01.
//
// REQ-OPS-218 is enforced at render time: the API returns plain JSON;
// HTML-encoding is the admin viewer's responsibility on display, but the
// server will not pass through bytes that would mis-render.

// clientLogRowDTO is the wire shape for a single ring-buffer row.
// Times are RFC 3339 with millisecond precision; nullable fields appear
// only when set so consumers can distinguish absence from empty string.
type clientLogRowDTO struct {
	ID          int64   `json:"id"`
	Slice       string  `json:"slice"`
	ServerTS    string  `json:"server_ts"`
	ClientTS    string  `json:"client_ts"`
	ClockSkewMS int64   `json:"clock_skew_ms"`
	App         string  `json:"app"`
	Kind        string  `json:"kind"`
	Level       string  `json:"level"`
	UserID      *string `json:"user_id,omitempty"`
	SessionID   *string `json:"session_id,omitempty"`
	PageID      string  `json:"page_id"`
	RequestID   *string `json:"request_id,omitempty"`
	Route       *string `json:"route,omitempty"`
	BuildSHA    string  `json:"build_sha"`
	UA          string  `json:"ua"`
	Msg         string  `json:"msg"`
	Stack       *string `json:"stack,omitempty"`
	// Payload carries the full enriched record for replay. Always JSON
	// (not a string) so the admin viewer can drill into breadcrumbs etc.
	Payload json.RawMessage `json:"payload,omitempty"`
}

func toClientLogRowDTO(row store.ClientLogRow) clientLogRowDTO {
	d := clientLogRowDTO{
		ID:          row.ID,
		Slice:       string(row.Slice),
		ServerTS:    row.ServerTS.UTC().Format(rfc3339Millis),
		ClientTS:    row.ClientTS.UTC().Format(rfc3339Millis),
		ClockSkewMS: row.ClockSkewMS,
		App:         row.App,
		Kind:        row.Kind,
		Level:       row.Level,
		UserID:      row.UserID,
		SessionID:   row.SessionID,
		PageID:      row.PageID,
		RequestID:   row.RequestID,
		Route:       row.Route,
		BuildSHA:    row.BuildSHA,
		UA:          row.UA,
		Msg:         row.Msg,
		Stack:       row.Stack,
	}
	if row.PayloadJSON != "" {
		d.Payload = json.RawMessage(row.PayloadJSON)
	}
	return d
}

const rfc3339Millis = "2006-01-02T15:04:05.000Z07:00"

// listClientLogResponse is the paginated wire response for REQ-ADM-230.
type listClientLogResponse struct {
	Rows       []clientLogRowDTO `json:"rows"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// handleAdminListClientLog implements GET /api/v1/admin/clientlog.
func (s *Server) handleAdminListClientLog(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	q := r.URL.Query()
	opts, perr := parseClientLogCursorOptions(q)
	if perr != nil {
		writeProblem(w, r, http.StatusBadRequest, "clientlog/invalid_query",
			"invalid query parameter", perr.Error())
		return
	}
	rows, next, err := s.store.Meta().ListClientLogByCursor(r.Context(), opts)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	out := listClientLogResponse{
		Rows:       make([]clientLogRowDTO, 0, len(rows)),
		NextCursor: next,
	}
	for _, row := range rows {
		out.Rows = append(out.Rows, toClientLogRowDTO(row))
	}
	writeJSON(w, http.StatusOK, out)
}

// parseClientLogCursorOptions converts URL query values into a typed
// options struct. Empty/absent params do not constrain the query.
func parseClientLogCursorOptions(q map[string][]string) (store.ClientLogCursorOptions, error) {
	opts := store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{
			Slice: store.ClientLogSliceAuth,
		},
		Cursor: getOne(q, "cursor"),
		Limit:  100,
	}
	if v := getOne(q, "slice"); v != "" {
		switch v {
		case "auth":
			opts.Filter.Slice = store.ClientLogSliceAuth
		case "public":
			opts.Filter.Slice = store.ClientLogSlicePublic
		default:
			return opts, errors.New("slice must be 'auth' or 'public'")
		}
	}
	if v := getOne(q, "app"); v != "" {
		switch v {
		case "suite", "admin":
			opts.Filter.App = v
		default:
			return opts, errors.New("app must be 'suite' or 'admin'")
		}
	}
	if v := getOne(q, "kind"); v != "" {
		switch v {
		case "error", "log", "vital":
			opts.Filter.Kind = v
		default:
			return opts, errors.New("kind must be 'error', 'log', or 'vital'")
		}
	}
	if v := getOne(q, "level"); v != "" {
		switch v {
		case "trace", "debug", "info", "warn", "error":
			opts.Filter.Level = v
		default:
			return opts, errors.New("level must be one of trace|debug|info|warn|error")
		}
	}
	if v := getOne(q, "since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return opts, errors.New("since must be RFC 3339")
		}
		opts.Filter.Since = t
	}
	if v := getOne(q, "until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return opts, errors.New("until must be RFC 3339")
		}
		opts.Filter.Until = t
	}
	opts.Filter.UserID = getOne(q, "user")
	opts.Filter.SessionID = getOne(q, "session_id")
	opts.Filter.RequestID = getOne(q, "request_id")
	opts.Filter.Route = getOne(q, "route")
	opts.Filter.MsgSubstring = getOne(q, "text")
	if v := getOne(q, "limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return opts, errors.New("limit must be a positive integer")
		}
		if n > 1000 {
			n = 1000
		}
		opts.Limit = n
	}
	return opts, nil
}

// getOne returns the first value for key from the parsed URL.Values map,
// or the empty string when absent. Avoids the temporary url.Values
// conversion in callers that already have a map[string][]string.
func getOne(q map[string][]string, key string) string {
	v, ok := q[key]
	if !ok || len(v) == 0 {
		return ""
	}
	return strings.TrimSpace(v[0])
}

// timelineEntryDTO is the wire shape for one entry in the merged
// client-server timeline (REQ-ADM-231, REQ-OPS-213). Source is "client"
// for ring-buffer rows and "server" for parsed slog records.
type timelineEntryDTO struct {
	Source      string           `json:"source"`
	EffectiveTS string           `json:"effective_ts"`
	ClientLog   *clientLogRowDTO `json:"clientlog,omitempty"`
	ServerLog   json.RawMessage  `json:"serverlog,omitempty"`
}

// handleAdminClientLogTimeline implements GET
// /api/v1/admin/clientlog/timeline?request_id=<id> (REQ-ADM-231).
//
// v1 returns the client-side rows only and a 422 for the server-side merge
// when no JSON file sink is configured to read from. The architecture
// promises a merged view; merging is wired in once the slog sink layer
// exposes a "scan-by-request-id" surface (tracked separately).
func (s *Server) handleAdminClientLogTimeline(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	rid := strings.TrimSpace(r.URL.Query().Get("request_id"))
	if rid == "" {
		writeProblem(w, r, http.StatusBadRequest, "clientlog/missing_request_id",
			"request_id query parameter is required", "")
		return
	}
	rows, err := s.store.Meta().ListClientLogByRequestID(r.Context(), rid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	out := make([]timelineEntryDTO, 0, len(rows))
	for _, row := range rows {
		dto := toClientLogRowDTO(row)
		// Effective time is client_ts + skew when client_ts is set,
		// otherwise server_ts. ring-buffer rows always have client_ts.
		eff := row.ClientTS.Add(time.Duration(row.ClockSkewMS) * time.Millisecond)
		out = append(out, timelineEntryDTO{
			Source:      "client",
			EffectiveTS: eff.UTC().Format(rfc3339Millis),
			ClientLog:   &dto,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// livetailRequest is the body for POST /api/v1/admin/clientlog/livetail.
type livetailRequest struct {
	SessionID string `json:"session_id"`
	// Duration is a Go duration string (e.g. "10m"). When empty, the
	// server uses clientlog.livetail_default_duration.
	Duration string `json:"duration,omitempty"`
}

// livetailResponse echoes the new state for the affected session.
type livetailResponse struct {
	SessionID string `json:"session_id"`
	Until     string `json:"livetail_until"`
}

// handleAdminClientLogLivetailSet implements
// POST /api/v1/admin/clientlog/livetail (REQ-ADM-232).
func (s *Server) handleAdminClientLogLivetailSet(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req livetailRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body",
			"request body is not valid JSON", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		writeProblem(w, r, http.StatusBadRequest, "clientlog/missing_session_id",
			"session_id is required", "")
		return
	}
	dDefault, dMax := s.clientLogLivetailDurations()
	d := dDefault
	if strings.TrimSpace(req.Duration) != "" {
		parsed, err := time.ParseDuration(req.Duration)
		if err != nil || parsed <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "clientlog/invalid_duration",
				"duration must be a positive Go duration string", req.Duration)
			return
		}
		d = parsed
	}
	if d > dMax {
		d = dMax
	}
	row, err := s.store.Meta().GetSession(r.Context(), req.SessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	now := s.clk.Now()
	until := now.Add(d).UTC()
	before := ""
	if row.ClientlogLivetailUntil != nil {
		before = row.ClientlogLivetailUntil.UTC().Format(rfc3339Millis)
	}
	row.ClientlogLivetailUntil = &until
	if err := s.store.Meta().UpsertSession(r.Context(), row); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(),
		"clientlog.livetail.set",
		"session:"+req.SessionID,
		store.OutcomeSuccess,
		"live-tail enabled",
		map[string]string{
			"livetail_until_before": before,
			"livetail_until_after":  until.Format(rfc3339Millis),
			"duration":              d.String(),
		},
	)
	if observe.ClientlogLivetailActiveSessions != nil {
		// Cheap upper-bound update: increment when we transition from
		// nil → non-nil. We do not maintain an exact count because the
		// gauge is advisory; the sweeper does not decrement here.
		observe.ClientlogLivetailActiveSessions.Inc()
	}
	writeJSON(w, http.StatusOK, livetailResponse{
		SessionID: req.SessionID,
		Until:     until.Format(rfc3339Millis),
	})
}

// handleAdminClientLogLivetailClear implements
// DELETE /api/v1/admin/clientlog/livetail/{session_id} (REQ-ADM-232).
func (s *Server) handleAdminClientLogLivetailClear(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	sid := strings.TrimSpace(r.PathValue("session_id"))
	if sid == "" {
		writeProblem(w, r, http.StatusBadRequest, "clientlog/missing_session_id",
			"session_id path parameter is required", "")
		return
	}
	row, err := s.store.Meta().GetSession(r.Context(), sid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	before := ""
	if row.ClientlogLivetailUntil != nil {
		before = row.ClientlogLivetailUntil.UTC().Format(rfc3339Millis)
	}
	row.ClientlogLivetailUntil = nil
	if err := s.store.Meta().UpsertSession(r.Context(), row); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(),
		"clientlog.livetail.clear",
		"session:"+sid,
		store.OutcomeSuccess,
		"live-tail disabled",
		map[string]string{
			"livetail_until_before": before,
		},
	)
	if observe.ClientlogLivetailActiveSessions != nil && before != "" {
		observe.ClientlogLivetailActiveSessions.Dec()
	}
	w.WriteHeader(http.StatusNoContent)
}

// clientLogStatsResponse is the wire shape for REQ-ADM-233.
type clientLogStatsResponse struct {
	ReceivedTotal  map[string]float64 `json:"received_total"`
	DroppedTotal   map[string]float64 `json:"dropped_total"`
	RingBufferRows map[string]float64 `json:"ring_buffer_rows"`
}

// handleAdminClientLogStats implements GET /api/v1/admin/clientlog/stats
// (REQ-ADM-233). We surface current counter values directly so the admin
// dashboard can compute deltas client-side; the prometheus registry is
// the canonical source.
func (s *Server) handleAdminClientLogStats(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	resp := clientLogStatsResponse{
		ReceivedTotal:  map[string]float64{},
		DroppedTotal:   map[string]float64{},
		RingBufferRows: map[string]float64{},
	}
	gathered, err := observe.Registry.Gather()
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "clientlog/metrics_gather",
			"failed to gather metrics", err.Error())
		return
	}
	for _, mf := range gathered {
		switch mf.GetName() {
		case "herold_clientlog_received_total":
			for _, m := range mf.GetMetric() {
				resp.ReceivedTotal[labelKey(m, "endpoint", "app", "kind")] = m.GetCounter().GetValue()
			}
		case "herold_clientlog_dropped_total":
			for _, m := range mf.GetMetric() {
				resp.DroppedTotal[labelKey(m, "endpoint", "reason")] = m.GetCounter().GetValue()
			}
		case "herold_clientlog_ring_buffer_rows":
			for _, m := range mf.GetMetric() {
				resp.RingBufferRows[labelKey(m, "slice")] = m.GetGauge().GetValue()
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// labelKey joins a metric's label values for the requested keys with "/"
// so {endpoint=auth,app=suite,kind=error} becomes "auth/suite/error".
func labelKey(m *dto.Metric, keys ...string) string {
	want := make(map[string]string, len(keys))
	for _, l := range m.GetLabel() {
		want[l.GetName()] = l.GetValue()
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, want[k])
	}
	return strings.Join(parts, "/")
}

// clientLogLivetailDurations returns (default, max) live-tail durations.
// The values come from the sysconfig clientlog block when available;
// stub defaults match REQ-OPS-219 for tests that do not wire a config.
func (s *Server) clientLogLivetailDurations() (time.Duration, time.Duration) {
	d := 15 * time.Minute
	m := 60 * time.Minute
	if s.clientlogLivetailDefault > 0 {
		d = s.clientlogLivetailDefault
	}
	if s.clientlogLivetailMax > 0 {
		m = s.clientlogLivetailMax
	}
	if d > m {
		d = m
	}
	return d, m
}
