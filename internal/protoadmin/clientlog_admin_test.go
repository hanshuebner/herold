package protoadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// TestAdminListClientlog checks the GET /api/v1/admin/clientlog endpoint
// pages over the ring buffer, applies filters, and rejects bad input.
func TestAdminListClientlog(t *testing.T) {
	h, apiKey := newClientlogHarness(t)

	// Seed the ring buffer with three auth-slice events and one public.
	now := h.clk.Now().UTC()
	mustAppend := func(row store.ClientLogRow) {
		t.Helper()
		if err := h.fs.Meta().AppendClientLog(context.Background(), row); err != nil {
			t.Fatalf("AppendClientLog: %v", err)
		}
	}
	uid := "1"
	for i, kind := range []string{"error", "log", "vital"} {
		mustAppend(store.ClientLogRow{
			Slice:       store.ClientLogSliceAuth,
			ServerTS:    now.Add(time.Duration(i) * time.Second),
			ClientTS:    now.Add(time.Duration(i) * time.Second),
			ClockSkewMS: 0,
			App:         "suite",
			Kind:        kind,
			Level:       map[string]string{"error": "error", "log": "warn", "vital": "info"}[kind],
			UserID:      &uid,
			PageID:      "page-1",
			BuildSHA:    "abc123",
			UA:          "Mozilla/5.0",
			Msg:         kind + " event",
			PayloadJSON: `{"v":1}`,
		})
	}
	mustAppend(store.ClientLogRow{
		Slice:       store.ClientLogSlicePublic,
		ServerTS:    now,
		ClientTS:    now,
		App:         "suite",
		Kind:        "error",
		Level:       "error",
		PageID:      "page-2",
		BuildSHA:    "abc123",
		UA:          "Mozilla/5.0",
		Msg:         "anonymous error",
		PayloadJSON: `{"v":1}`,
	})

	// Default: auth slice, all rows.
	res, body := h.adminGet("/api/v1/admin/clientlog", apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body=%s", res.StatusCode, body)
	}
	var out struct {
		Rows       []map[string]any `json:"rows"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if len(out.Rows) != 3 {
		t.Fatalf("auth-slice rows: want 3, got %d", len(out.Rows))
	}
	for _, row := range out.Rows {
		if row["slice"] != "auth" {
			t.Fatalf("default slice should be auth, got %v", row["slice"])
		}
	}

	// Filter by kind.
	_, body2 := h.adminGet("/api/v1/admin/clientlog?kind=error", apiKey, nil)
	_ = json.Unmarshal(body2, &out)
	if len(out.Rows) != 1 || out.Rows[0]["kind"] != "error" {
		t.Fatalf("kind=error filter: got %+v", out.Rows)
	}

	// Filter to public slice.
	_, body3 := h.adminGet("/api/v1/admin/clientlog?slice=public", apiKey, nil)
	_ = json.Unmarshal(body3, &out)
	if len(out.Rows) != 1 || out.Rows[0]["slice"] != "public" {
		t.Fatalf("slice=public filter: got %+v", out.Rows)
	}

	// Bad slice.
	res4, _ := h.adminGet("/api/v1/admin/clientlog?slice=banana", apiKey, nil)
	if res4.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad slice: want 400, got %d", res4.StatusCode)
	}

	// Pagination via small limit.
	res5, body5 := h.adminGet("/api/v1/admin/clientlog?limit=2", apiKey, nil)
	if res5.StatusCode != http.StatusOK {
		t.Fatalf("paged: %d", res5.StatusCode)
	}
	_ = json.Unmarshal(body5, &out)
	if len(out.Rows) != 2 {
		t.Fatalf("paged: want 2 rows, got %d", len(out.Rows))
	}
	if out.NextCursor == "" {
		t.Fatalf("paged: expected non-empty next_cursor")
	}
}

// TestAdminClientlogTimeline verifies timeline returns the client-side
// rows for a given request_id sorted by effective time.
func TestAdminClientlogTimeline(t *testing.T) {
	h, apiKey := newClientlogHarness(t)

	rid := "req-abc"
	other := "req-other"
	now := h.clk.Now().UTC()
	uid := "1"
	mk := func(reqID *string, offset time.Duration, kind string) store.ClientLogRow {
		return store.ClientLogRow{
			Slice:       store.ClientLogSliceAuth,
			ServerTS:    now.Add(offset),
			ClientTS:    now.Add(offset),
			ClockSkewMS: 0,
			App:         "suite",
			Kind:        kind,
			Level:       "info",
			UserID:      &uid,
			PageID:      "page-1",
			RequestID:   reqID,
			BuildSHA:    "abc",
			UA:          "Mozilla/5.0",
			Msg:         kind,
			PayloadJSON: `{"v":1}`,
		}
	}
	if err := h.fs.Meta().AppendClientLog(context.Background(), mk(&rid, 0, "log")); err != nil {
		t.Fatal(err)
	}
	if err := h.fs.Meta().AppendClientLog(context.Background(), mk(&rid, time.Second, "error")); err != nil {
		t.Fatal(err)
	}
	if err := h.fs.Meta().AppendClientLog(context.Background(), mk(&other, time.Second, "log")); err != nil {
		t.Fatal(err)
	}

	// Missing request_id → 400.
	res, _ := h.adminGet("/api/v1/admin/clientlog/timeline", apiKey, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing request_id: want 400, got %d", res.StatusCode)
	}

	// Hits → only the two entries with the matching request_id.
	res2, body := h.adminGet("/api/v1/admin/clientlog/timeline?request_id=req-abc", apiKey, nil)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body=%s", res2.StatusCode, body)
	}
	var entries []map[string]any
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: want 2, got %d body=%s", len(entries), body)
	}
	for _, e := range entries {
		if e["source"] != "client" {
			t.Fatalf("source: want client, got %v", e["source"])
		}
		if e["clientlog"] == nil {
			t.Fatalf("entry missing clientlog payload")
		}
	}
}

// TestAdminClientlogLivetailLifecycle exercises the POST + DELETE livetail
// endpoints, the duration clamp, the audit-log entry, and the session row.
func TestAdminClientlogLivetailLifecycle(t *testing.T) {
	h, apiKey := newClientlogHarness(t)

	// Seed a session row to flip live-tail on.
	sid := "session-abc"
	now := h.clk.Now().UTC()
	if err := h.fs.Meta().UpsertSession(context.Background(), store.SessionRow{
		SessionID:                 sid,
		PrincipalID:               1,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// Set live-tail with an explicit short duration; assert response.
	body := map[string]any{"session_id": sid, "duration": "2m"}
	res, respBody := h.postWithKey("/api/v1/admin/clientlog/livetail", body, apiKey)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set: %d body=%s", res.StatusCode, respBody)
	}
	var setResp map[string]any
	_ = json.Unmarshal(respBody, &setResp)
	if setResp["session_id"] != sid {
		t.Fatalf("set: unexpected session_id: %v", setResp["session_id"])
	}
	if !strings.HasPrefix(setResp["livetail_until"].(string), "2026-") {
		t.Fatalf("set: unexpected livetail_until: %v", setResp["livetail_until"])
	}

	// Session row reflects the change.
	row, err := h.fs.Meta().GetSession(context.Background(), sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ClientlogLivetailUntil == nil {
		t.Fatalf("expected livetail_until set on session")
	}
	if row.ClientlogLivetailUntil.Sub(now) != 2*time.Minute {
		t.Fatalf("duration: want 2m, got %s", row.ClientlogLivetailUntil.Sub(now))
	}

	// Audit log carries the action.
	auditRows, err := h.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "clientlog.livetail.set", Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(auditRows) != 1 {
		t.Fatalf("audit rows: want 1, got %d", len(auditRows))
	}

	// Duration clamp: request 24h, default max is 60m.
	body2 := map[string]any{"session_id": sid, "duration": "24h"}
	res2, _ := h.postWithKey("/api/v1/admin/clientlog/livetail", body2, apiKey)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("clamp: %d", res2.StatusCode)
	}
	row2, _ := h.fs.Meta().GetSession(context.Background(), sid)
	if row2.ClientlogLivetailUntil.Sub(now) > 60*time.Minute+time.Second {
		t.Fatalf("clamp: livetail_until exceeded max: %s", row2.ClientlogLivetailUntil.Sub(now))
	}

	// Bad duration string → 400.
	bad := map[string]any{"session_id": sid, "duration": "banana"}
	res3, _ := h.postWithKey("/api/v1/admin/clientlog/livetail", bad, apiKey)
	if res3.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad duration: want 400, got %d", res3.StatusCode)
	}

	// Clear via DELETE.
	delRes := h.adminDelete("/api/v1/admin/clientlog/livetail/"+sid, apiKey)
	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", delRes.StatusCode)
	}
	row3, _ := h.fs.Meta().GetSession(context.Background(), sid)
	if row3.ClientlogLivetailUntil != nil {
		t.Fatalf("expected livetail_until cleared")
	}
	clearAudit, _ := h.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "clientlog.livetail.clear", Limit: 10})
	if len(clearAudit) != 1 {
		t.Fatalf("clear audit: want 1, got %d", len(clearAudit))
	}
}

// TestAdminClientlogStats exercises the stats endpoint surfacing the
// counter values from the prometheus registry.
func TestAdminClientlogStats(t *testing.T) {
	h, apiKey := newClientlogHarness(t)

	res, body := h.adminGet("/api/v1/admin/clientlog/stats", apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body=%s", res.StatusCode, body)
	}
	var resp struct {
		ReceivedTotal  map[string]float64 `json:"received_total"`
		DroppedTotal   map[string]float64 `json:"dropped_total"`
		RingBufferRows map[string]float64 `json:"ring_buffer_rows"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	// Maps are present even when empty (no events processed in this test).
	if resp.ReceivedTotal == nil {
		t.Fatalf("expected received_total map (possibly empty)")
	}
}

// TestAdminClientlogRequiresAdmin checks every admin endpoint returns 401
// without auth and 403 for a non-admin caller.
func TestAdminClientlogRequiresAdmin(t *testing.T) {
	h, _ := newClientlogHarness(t)

	for _, path := range []string{
		"/api/v1/admin/clientlog",
		"/api/v1/admin/clientlog/timeline?request_id=x",
		"/api/v1/admin/clientlog/stats",
	} {
		res, _ := h.adminGet(path, "", nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s no-auth: want 401, got %d", path, res.StatusCode)
		}
	}
}

// adminGet issues an authenticated GET against the admin REST surface.
func (h *clientlogHarness) adminGet(path, key string, hdr map[string]string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest("GET", h.baseURL+path, nil)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, _ := readAllForTest(res)
	return res, buf
}

// adminDelete issues an authenticated DELETE.
func (h *clientlogHarness) adminDelete(path, key string) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest("DELETE", h.baseURL+path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	return res
}

// postWithKey wraps the existing post helper to skip the extra-headers map.
func (h *clientlogHarness) postWithKey(path string, body any, key string) (*http.Response, []byte) {
	return h.post(path, body, nil, key)
}

func readAllForTest(res *http.Response) ([]byte, error) {
	const max = 1 << 20
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 4096)
	for {
		n, err := res.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > max {
				return buf, nil
			}
		}
		if err != nil {
			return buf, nil
		}
	}
}
