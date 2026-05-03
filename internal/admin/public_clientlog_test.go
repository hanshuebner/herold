package admin

// public_clientlog_test.go is the regression test for the bug where the
// clientlog ingest routes (and the per-user telemetry self-service endpoint)
// were not mounted on the public listener at all, so the Suite SPA -- which
// is served from the public origin -- got 405 on every event it tried to
// emit. The fix added three publicMux.Handle entries for /api/v1/clientlog,
// /api/v1/clientlog/, and /api/v1/me/clientlog/telemetry_enabled and
// introduced an Options.ListenerTag string + withListenerTag middleware so
// the enriched ring-buffer payload records the originating listener.
//
// Scenarios covered:
//
//  1. POST /api/v1/clientlog/public on the public listener accepts a valid
//     event (200) -- not 405. The enriched payload's listener field is
//     "public" -- not "admin".
//  2. OPTIONS /api/v1/clientlog/public preflight on the public listener
//     returns 204 with same-origin Access-Control headers -- not 405.
//  3. POST /api/v1/clientlog (auth) with a Bearer key on the public
//     listener accepts the event (200). The enriched payload's listener
//     field is "public".
//  4. Posting the same event via the admin listener tags the payload with
//     listener=admin, confirming the tag is per-listener.
//  5. PUT /api/v1/me/clientlog/telemetry_enabled on the public listener
//     without auth returns 401 -- not 405. (Confirms the route is mounted
//     even though we cannot easily exercise the success path without a
//     full session-cookie login flow.)

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestPublicListener_ClientlogIngest is the regression for the missing
// clientlog mount on publicMux + the listener-tag plumbing.
func TestPublicListener_ClientlogIngest(t *testing.T) {
	addrs, done, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap an admin and capture an API key so we can drive both
	// the auth ingest endpoint AND the admin REST surface that lets us
	// read events back to inspect the enriched payload.
	_, adminAPIKey, _, _ := bootstrapAndGetAPIKey(t, adminAddr)

	// --- Scenario 1: anonymous POST on public listener succeeds with
	// listener=public in the enriched payload. --------------------------------
	postBody := map[string]any{
		"events": []map[string]any{{
			"v": 1, "kind": "error", "level": "error",
			"msg":       "regression: anonymous public-listener event",
			"client_ts": "2026-05-03T11:00:00.000Z",
			"seq":       1,
			"page_id":   "00000000-0000-0000-0000-00000000a000",
			"app":       "suite",
			"build_sha": "test",
			"route":     "/login",
			"ua":        "test/1",
		}},
	}
	postPublic := func(t *testing.T, addr, path, bearer string, body any) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req, err := http.NewRequest("POST", "http://"+addr+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("new POST: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		return res
	}

	res := postPublic(t, publicAddr, "/api/v1/clientlog/public", "", postBody)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("anonymous POST on public listener: want 200, got %d body=%s",
			res.StatusCode, body)
	}

	// --- Scenario 2: OPTIONS preflight on public listener returns 204
	// with same-origin Access-Control headers. -------------------------------
	preflight := func(addr, path, origin string) *http.Response {
		req, err := http.NewRequest("OPTIONS", "http://"+addr+path, nil)
		if err != nil {
			t.Fatalf("OPTIONS req: %v", err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Content-Type")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("OPTIONS do: %v", err)
		}
		return res
	}
	res = preflight(publicAddr, "/api/v1/clientlog/public", "http://"+publicAddr)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight on public listener: want 204, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got == "" {
		t.Fatalf("OPTIONS preflight: missing Access-Control-Allow-Origin")
	}

	// --- Scenario 3: authenticated POST on public listener succeeds with
	// listener=public in the enriched payload. ------------------------------
	authBody := map[string]any{
		"events": []map[string]any{{
			"v": 1, "kind": "log", "level": "warn",
			"msg":        "regression: auth public-listener event",
			"client_ts":  "2026-05-03T11:01:00.000Z",
			"seq":        2,
			"page_id":    "00000000-0000-0000-0000-00000000a001",
			"session_id": "regression-session-public",
			"app":        "suite",
			"build_sha":  "test",
			"route":      "/mail/inbox",
			"ua":         "test/1",
		}},
	}
	res = postPublic(t, publicAddr, "/api/v1/clientlog", adminAPIKey, authBody)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auth POST on public listener: want 200, got %d body=%s",
			res.StatusCode, body)
	}

	// Read the rows back via the admin REST surface. The pipeline is async
	// (worker pool fan-out), so poll up to ~2 s.
	type ringRow struct {
		ID      int64  `json:"id"`
		Slice   string `json:"slice"`
		Msg     string `json:"msg"`
		Payload struct {
			Listener string `json:"listener"`
			Endpoint string `json:"endpoint"`
		} `json:"payload"`
	}
	type listResp struct {
		Rows []ringRow `json:"rows"`
	}
	listRows := func(t *testing.T, slice string) []ringRow {
		t.Helper()
		req, err := http.NewRequest("GET",
			"http://"+adminAddr+"/api/v1/admin/clientlog?limit=20&slice="+slice, nil)
		if err != nil {
			t.Fatalf("GET req: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminAPIKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET do: %v", err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("admin clientlog list (%s): %d body=%s", slice, res.StatusCode, raw)
		}
		var out listResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("admin clientlog decode: %v body=%s", err, raw)
		}
		return out.Rows
	}

	findByMsg := func(rows []ringRow, contains string) *ringRow {
		for i := range rows {
			if strings.Contains(rows[i].Msg, contains) {
				return &rows[i]
			}
		}
		return nil
	}

	deadline := time.Now().Add(3 * time.Second)
	var publicSliceRow, authSliceRow *ringRow
	for time.Now().Before(deadline) {
		publicSliceRow = findByMsg(listRows(t, "public"), "anonymous public-listener event")
		authSliceRow = findByMsg(listRows(t, "auth"), "auth public-listener event")
		if publicSliceRow != nil && authSliceRow != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if publicSliceRow == nil {
		t.Fatalf("anonymous event did not land in the public-slice ring buffer")
	}
	if authSliceRow == nil {
		t.Fatalf("auth event did not land in the auth-slice ring buffer")
	}

	// Both rows MUST carry listener="public" -- the regression: prior to the
	// withListenerTag middleware, the tag fell through to the "admin" default
	// even when the request arrived on the public listener.
	if publicSliceRow.Payload.Listener != "public" {
		t.Fatalf("public-slice row: listener=%q want %q (regression: listener tag not stamped on public listener)",
			publicSliceRow.Payload.Listener, "public")
	}
	if authSliceRow.Payload.Listener != "public" {
		t.Fatalf("auth-slice row from public listener: listener=%q want %q",
			authSliceRow.Payload.Listener, "public")
	}

	// --- Scenario 4: posting via the admin listener tags listener=admin. -----
	adminPostBody := map[string]any{
		"events": []map[string]any{{
			"v": 1, "kind": "log", "level": "warn",
			"msg":       "regression: admin-listener event",
			"client_ts": "2026-05-03T11:02:00.000Z",
			"seq":       3,
			"page_id":   "00000000-0000-0000-0000-00000000a002",
			"app":       "admin",
			"build_sha": "test",
			"route":     "/dashboard",
			"ua":        "test/1",
		}},
	}
	res = postPublic(t, adminAddr, "/api/v1/clientlog", adminAPIKey, adminPostBody)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auth POST on admin listener: want 200, got %d", res.StatusCode)
	}

	deadline = time.Now().Add(3 * time.Second)
	var adminSliceRow *ringRow
	for time.Now().Before(deadline) {
		adminSliceRow = findByMsg(listRows(t, "auth"), "admin-listener event")
		if adminSliceRow != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if adminSliceRow == nil {
		t.Fatalf("admin-listener event did not land in the ring buffer")
	}
	if adminSliceRow.Payload.Listener != "admin" {
		t.Fatalf("admin-listener row: listener=%q want %q",
			adminSliceRow.Payload.Listener, "admin")
	}

	// --- Scenario 5: /api/v1/me/clientlog/telemetry_enabled is mounted on
	// the public listener (returns 401, not 405). Cookie-based success path
	// is exercised by the dedicated self-service tests; this just confirms
	// the route survived the mount fix. ---------------------------------------
	teReq, err := http.NewRequest("PUT",
		"http://"+publicAddr+"/api/v1/me/clientlog/telemetry_enabled",
		strings.NewReader(`{"enabled":true}`))
	if err != nil {
		t.Fatalf("PUT req: %v", err)
	}
	teReq.Header.Set("Content-Type", "application/json")
	teRes, err := http.DefaultClient.Do(teReq)
	if err != nil {
		t.Fatalf("PUT do: %v", err)
	}
	teRes.Body.Close()
	if teRes.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/v1/me/clientlog/telemetry_enabled on public listener: 405 (route not mounted -- regression)")
	}
	if teRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PUT /api/v1/me/clientlog/telemetry_enabled (no auth): want 401, got %d",
			teRes.StatusCode)
	}

	_ = context.Background() // keep stdlib imports stable when callers prune
}
