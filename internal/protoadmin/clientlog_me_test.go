package protoadmin_test

// clientlog_me_test.go covers GET /api/v1/me/clientlog (re #83).
//
// Test matrix:
//   - unauthenticated caller: 401
//   - caller receives only their own rows (not another principal's rows)
//   - public-slice rows are never returned
//   - no admin scope required: non-admin principal can call the endpoint
//   - no elevation required: non-elevated principal can call the endpoint
//   - limit is capped at meClientLogLimitCap (500)
//   - session_id filter narrows to matching rows
//   - since filter excludes rows before the threshold
//   - cursor pagination works

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

const meClientlogPath = "/api/v1/me/clientlog"

// createUserAndKey creates a non-admin principal via the admin API and returns
// (principalID, apiKey) for use in self-service tests.
func (h *clientlogHarness) createUserAndKey(adminKey, email string) (uint64, string) {
	h.t.Helper()

	// Create principal.
	body, _ := json.Marshal(map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	req, _ := http.NewRequest("POST", h.baseURL+"/api/v1/principals",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminKey)
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("createUserAndKey POST principals: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("createUserAndKey: status=%d body=%s", res.StatusCode, raw)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		h.t.Fatalf("createUserAndKey decode principal: %v", err)
	}

	// Mint an API key for this principal.
	keyBody, _ := json.Marshal(map[string]any{"label": "test"})
	req2, _ := http.NewRequest("POST",
		h.baseURL+fmt.Sprintf("/api/v1/principals/%d/api-keys", p.ID),
		bytes.NewReader(keyBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+adminKey)
	res2, err := h.client.Do(req2)
	if err != nil {
		h.t.Fatalf("createUserAndKey POST api-keys: %v", err)
	}
	defer res2.Body.Close()
	raw2, _ := io.ReadAll(res2.Body)
	if res2.StatusCode != http.StatusCreated {
		h.t.Fatalf("createUserAndKey mint key: status=%d body=%s", res2.StatusCode, raw2)
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw2, &k); err != nil {
		h.t.Fatalf("createUserAndKey decode key: %v", err)
	}
	return p.ID, k.Key
}

// getClientlog issues GET /api/v1/me/clientlog with optional query params and Bearer key.
func (h *clientlogHarness) getClientlog(key, query string) (*http.Response, []byte) {
	h.t.Helper()
	path := meClientlogPath
	if query != "" {
		path += "?" + query
	}
	req, err := http.NewRequest("GET", h.baseURL+path, nil)
	if err != nil {
		h.t.Fatalf("getClientlog new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("getClientlog do: %v", err)
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	return res, buf
}

// seedAuthRow inserts one auth-slice ring-buffer row for userID (decimal string).
func (h *clientlogHarness) seedAuthRow(userID string, sessionID string, offset time.Duration, msg string) {
	h.t.Helper()
	var sidPtr *string
	if sessionID != "" {
		sidPtr = &sessionID
	}
	uidPtr := userID
	row := store.ClientLogRow{
		Slice:       store.ClientLogSliceAuth,
		ServerTS:    h.clk.Now().UTC().Add(offset),
		ClientTS:    h.clk.Now().UTC().Add(offset),
		ClockSkewMS: 0,
		App:         "suite",
		Kind:        "log",
		Level:       "info",
		UserID:      &uidPtr,
		SessionID:   sidPtr,
		PageID:      "page-1",
		BuildSHA:    "abc",
		UA:          "Mozilla/5.0",
		Msg:         msg,
		PayloadJSON: `{"v":1}`,
	}
	if err := h.fs.Meta().AppendClientLog(context.Background(), row); err != nil {
		h.t.Fatalf("seedAuthRow: %v", err)
	}
}

// seedPublicRow inserts one public-slice ring-buffer row (no user_id).
func (h *clientlogHarness) seedPublicRow(msg string) {
	h.t.Helper()
	row := store.ClientLogRow{
		Slice:       store.ClientLogSlicePublic,
		ServerTS:    h.clk.Now().UTC(),
		ClientTS:    h.clk.Now().UTC(),
		App:         "suite",
		Kind:        "error",
		Level:       "error",
		PageID:      "page-pub",
		BuildSHA:    "abc",
		UA:          "Mozilla/5.0",
		Msg:         msg,
		PayloadJSON: `{"v":1}`,
	}
	if err := h.fs.Meta().AppendClientLog(context.Background(), row); err != nil {
		h.t.Fatalf("seedPublicRow: %v", err)
	}
}

// decodeListClientlogResponse decodes the standard {rows, next_cursor} envelope.
func decodeListClientlogResponse(t *testing.T, body []byte) ([]map[string]any, string) {
	t.Helper()
	var out struct {
		Rows       []map[string]any `json:"rows"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode clientlog response: %v body=%s", err, body)
	}
	return out.Rows, out.NextCursor
}

// TestMeClientlog_Unauthenticated_Returns401 verifies that an unauthenticated
// request receives 401.
func TestMeClientlog_Unauthenticated_Returns401(t *testing.T) {
	t.Parallel()
	h, _ := newClientlogHarness(t)

	res, _ := h.getClientlog("", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET: status=%d, want 401", res.StatusCode)
	}
}

// TestMeClientlog_OwnRowsOnly verifies that the endpoint returns only the
// caller's own rows; another principal's rows are never included.
func TestMeClientlog_OwnRowsOnly(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	// Create two non-admin principals.
	userAID, userAKey := h.createUserAndKey(adminKey, "me-clientlog-a@example.com")
	userBID, userBKey := h.createUserAndKey(adminKey, "me-clientlog-b@example.com")

	uidA := fmt.Sprintf("%d", userAID)
	uidB := fmt.Sprintf("%d", userBID)

	// Seed two rows for A, three for B, and one public row (no owner).
	h.seedAuthRow(uidA, "", 0, "A row 1")
	h.seedAuthRow(uidA, "", time.Second, "A row 2")
	h.seedAuthRow(uidB, "", 0, "B row 1")
	h.seedAuthRow(uidB, "", time.Second, "B row 2")
	h.seedAuthRow(uidB, "", 2*time.Second, "B row 3")
	h.seedPublicRow("anonymous row")

	// User A: expects exactly 2 rows, both owned by A.
	resA, bodyA := h.getClientlog(userAKey, "")
	if resA.StatusCode != http.StatusOK {
		t.Fatalf("user A GET: status=%d body=%s", resA.StatusCode, bodyA)
	}
	rowsA, _ := decodeListClientlogResponse(t, bodyA)
	if len(rowsA) != 2 {
		t.Fatalf("user A: want 2 rows, got %d", len(rowsA))
	}
	for i, row := range rowsA {
		if row["user_id"] != uidA {
			t.Errorf("user A row[%d].user_id = %v, want %q", i, row["user_id"], uidA)
		}
		if row["slice"] != "auth" {
			t.Errorf("user A row[%d].slice = %v, want auth", i, row["slice"])
		}
	}

	// User B: expects exactly 3 rows, all owned by B.
	resB, bodyB := h.getClientlog(userBKey, "")
	if resB.StatusCode != http.StatusOK {
		t.Fatalf("user B GET: status=%d body=%s", resB.StatusCode, bodyB)
	}
	rowsB, _ := decodeListClientlogResponse(t, bodyB)
	if len(rowsB) != 3 {
		t.Fatalf("user B: want 3 rows, got %d", len(rowsB))
	}
	for i, row := range rowsB {
		if row["user_id"] != uidB {
			t.Errorf("user B row[%d].user_id = %v, want %q", i, row["user_id"], uidB)
		}
	}

	// Verify that A's response never leaks B's rows and vice-versa.
	for i, row := range rowsA {
		if row["user_id"] == uidB {
			t.Errorf("user A response contains B's row at index %d: %v", i, row)
		}
	}
	for i, row := range rowsB {
		if row["user_id"] == uidA {
			t.Errorf("user B response contains A's row at index %d: %v", i, row)
		}
	}
}

// TestMeClientlog_PublicSliceNeverReturned verifies that rows from the public
// slice are excluded regardless of how many exist.
func TestMeClientlog_PublicSliceNeverReturned(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	_, userKey := h.createUserAndKey(adminKey, "me-clientlog-pub@example.com")

	// Seed only public rows (no auth rows for this user).
	h.seedPublicRow("pub 1")
	h.seedPublicRow("pub 2")

	res, body := h.getClientlog(userKey, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: status=%d body=%s", res.StatusCode, body)
	}
	rows, _ := decodeListClientlogResponse(t, body)
	if len(rows) != 0 {
		t.Fatalf("want 0 rows (public rows must not leak), got %d: %v", len(rows), rows)
	}
}

// TestMeClientlog_NoAdminScopeRequired verifies that a non-admin principal
// with only the default user scope can call the endpoint successfully.
func TestMeClientlog_NoAdminScopeRequired(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	userID, userKey := h.createUserAndKey(adminKey, "me-clientlog-nonadmin@example.com")
	uidStr := fmt.Sprintf("%d", userID)

	// Seed one auth row for this user.
	h.seedAuthRow(uidStr, "", 0, "non-admin row")

	res, body := h.getClientlog(userKey, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("non-admin GET: status=%d body=%s, want 200", res.StatusCode, body)
	}
	rows, _ := decodeListClientlogResponse(t, body)
	if len(rows) != 1 {
		t.Fatalf("non-admin: want 1 row, got %d", len(rows))
	}
}

// TestMeClientlog_LimitCap verifies that requesting more than meClientLogLimitCap
// rows is silently clamped to the cap rather than rejected.
func TestMeClientlog_LimitCap(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	userID, userKey := h.createUserAndKey(adminKey, "me-clientlog-limitcap@example.com")
	uidStr := fmt.Sprintf("%d", userID)

	// Seed 3 rows (well below any cap) — we are testing the cap parameter
	// clamp, not actually returning 500 rows in a test.
	for i := 0; i < 3; i++ {
		h.seedAuthRow(uidStr, "", time.Duration(i)*time.Second, fmt.Sprintf("row %d", i))
	}

	// Request limit=9999 (above the 500 cap). Should return all 3 rows
	// without error (cap is applied silently).
	res, body := h.getClientlog(userKey, "limit=9999")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("limit=9999 GET: status=%d body=%s", res.StatusCode, body)
	}
	rows, _ := decodeListClientlogResponse(t, body)
	if len(rows) != 3 {
		t.Fatalf("limit cap: want 3 rows (all seeded), got %d", len(rows))
	}

	// Request invalid limit (negative) must return 400.
	resBad, bodyBad := h.getClientlog(userKey, "limit=-1")
	if resBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative limit: want 400, got %d body=%s", resBad.StatusCode, bodyBad)
	}
}

// TestMeClientlog_SessionIDFilter verifies that the session_id query parameter
// narrows the result to rows with the matching session.
func TestMeClientlog_SessionIDFilter(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	userID, userKey := h.createUserAndKey(adminKey, "me-clientlog-session@example.com")
	uidStr := fmt.Sprintf("%d", userID)

	h.seedAuthRow(uidStr, "session-alpha", 0, "alpha row 1")
	h.seedAuthRow(uidStr, "session-alpha", time.Second, "alpha row 2")
	h.seedAuthRow(uidStr, "session-beta", 0, "beta row")

	// Filter to session-alpha: expect 2 rows.
	res, body := h.getClientlog(userKey, "session_id=session-alpha")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session_id filter GET: status=%d body=%s", res.StatusCode, body)
	}
	rows, _ := decodeListClientlogResponse(t, body)
	if len(rows) != 2 {
		t.Fatalf("session_id=session-alpha: want 2, got %d rows=%v", len(rows), rows)
	}
	for i, row := range rows {
		if row["session_id"] != "session-alpha" {
			t.Errorf("row[%d].session_id = %v, want session-alpha", i, row["session_id"])
		}
	}
}

// TestMeClientlog_SinceFilter verifies that the since parameter excludes rows
// with server_ts before the threshold.
func TestMeClientlog_SinceFilter(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	userID, userKey := h.createUserAndKey(adminKey, "me-clientlog-since@example.com")
	uidStr := fmt.Sprintf("%d", userID)

	base := h.clk.Now().UTC()
	h.seedAuthRow(uidStr, "", -2*time.Minute, "old row") // before threshold
	h.seedAuthRow(uidStr, "", 0, "new row 1")            // at base
	h.seedAuthRow(uidStr, "", time.Minute, "new row 2")  // after base

	// Filter to rows at or after base: expect 2 (excluding "old row").
	since := base.Format(time.RFC3339)
	res, body := h.getClientlog(userKey, "since="+since)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("since filter GET: status=%d body=%s", res.StatusCode, body)
	}
	rows, _ := decodeListClientlogResponse(t, body)
	if len(rows) != 2 {
		t.Fatalf("since filter: want 2 rows, got %d rows=%v", len(rows), rows)
	}
}

// TestMeClientlog_CursorPagination verifies that the cursor-based pagination
// returns all rows across pages and terminates correctly.
func TestMeClientlog_CursorPagination(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	userID, userKey := h.createUserAndKey(adminKey, "me-clientlog-cursor@example.com")
	uidStr := fmt.Sprintf("%d", userID)

	for i := 0; i < 5; i++ {
		h.seedAuthRow(uidStr, "", time.Duration(i)*time.Second, fmt.Sprintf("row %d", i))
	}

	// Page 1: limit=2.
	res1, body1 := h.getClientlog(userKey, "limit=2")
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("page1 GET: status=%d body=%s", res1.StatusCode, body1)
	}
	rows1, cursor1 := decodeListClientlogResponse(t, body1)
	if len(rows1) != 2 {
		t.Fatalf("page1: want 2 rows, got %d", len(rows1))
	}
	if cursor1 == "" {
		t.Fatal("page1: expected non-empty next_cursor")
	}

	// Page 2: use cursor from page 1.
	res2, body2 := h.getClientlog(userKey, "limit=2&cursor="+cursor1)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("page2 GET: status=%d body=%s", res2.StatusCode, body2)
	}
	rows2, cursor2 := decodeListClientlogResponse(t, body2)
	if len(rows2) != 2 {
		t.Fatalf("page2: want 2 rows, got %d", len(rows2))
	}
	if cursor2 == "" {
		t.Fatal("page2: expected non-empty next_cursor")
	}

	// Page 3: last page with 1 row.
	res3, body3 := h.getClientlog(userKey, "limit=2&cursor="+cursor2)
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("page3 GET: status=%d body=%s", res3.StatusCode, body3)
	}
	rows3, cursor3 := decodeListClientlogResponse(t, body3)
	if len(rows3) != 1 {
		t.Fatalf("page3: want 1 row, got %d", len(rows3))
	}
	if cursor3 != "" {
		t.Fatalf("page3: expected empty next_cursor, got %q", cursor3)
	}

	// All returned rows belong to the caller.
	for _, set := range [][]map[string]any{rows1, rows2, rows3} {
		for _, row := range set {
			if row["user_id"] != uidStr {
				t.Errorf("cross-page row has wrong user_id: %v", row["user_id"])
			}
		}
	}
}

// TestMeClientlog_SinceInvalidFormat verifies that a malformed since parameter
// returns 400.
func TestMeClientlog_SinceInvalidFormat(t *testing.T) {
	t.Parallel()
	h, adminKey := newClientlogHarness(t)

	_, userKey := h.createUserAndKey(adminKey, "me-clientlog-sinceformat@example.com")

	res, body := h.getClientlog(userKey, "since=not-a-date")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid since: want 400, got %d body=%s", res.StatusCode, body)
	}
}
