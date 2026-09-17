// bugreports_test.go exercises the bug-reports REST surface (issue
// #416, REQ-ADM-320..324):
//
//   - POST /api/v1/bug-reports accepts a device-token-authenticated
//     end-user's multipart bundle (separate parts, or one zip part),
//     rejects an unexpected part name, an oversized screenshot, and a
//     body missing report.json, and refuses a bug-reports-scoped key
//     (it lacks ScopeEndUser).
//   - GET /api/v1/bug-reports (list) and GET .../{id} (zip download)
//     and DELETE .../{id} require ScopeBugReports or ScopeAdmin; an
//     end-user token is refused.
//   - A bug-reports-scoped key is refused on two unrelated admin
//     endpoints (REQ-AUTH-SCOPE-02), proving the scope is load-bearing
//     everywhere else on the admin surface.
//   - The full create/list/get/delete lifecycle, and its audit-log
//     entries, on both SQLite and (when HEROLD_PG_DSN is set) Postgres
//     via openSubmissionBackends.
package protoadmin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

const bugReportTestMeta = `{
  "protocol": "herold-bug-mail/1",
  "createdAt": "2026-09-17T09:00:00.000Z",
  "kind": "bug",
  "descriptionEntered": true,
  "sketch": "Thread list jumps after sync\nSecond line.",
  "app": {"id": "herold-android", "name": "Herold Android", "version": "0.6.1"},
  "principal": {"id": "p1", "label": "alice@example.local"},
  "context": {"route": "thread/t42"},
  "logs": [{"ts": 1758013200000, "level": "warn", "msg": "sync: outbox retry"}],
  "screenshotCount": 1
}`

var bugReportTestPNG = []byte("\x89PNG\r\n\x1a\nfake-png-bytes")

// createScopedAPIKey mints an API key with an arbitrary scope list,
// unlike harness.createAPIKey which is hardcoded to scope admin.
func (h *harness) createScopedAPIKey(callerKey string, pid uint64, scope []string, allowAdmin bool) (uint64, string) {
	h.t.Helper()
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), callerKey, map[string]any{
		"label":             "test-key",
		"scope":             scope,
		"allow_admin_scope": allowAdmin,
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("createScopedAPIKey: %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID  uint64 `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		h.t.Fatalf("createScopedAPIKey decode: %v: %s", err, buf)
	}
	return created.ID, created.Key
}

// deviceToken mints a Bearer device token for email/password, carrying
// auth.AllEndUserScopes (ScopeEndUser among them).
func (h *harness) deviceToken(email, password string) string {
	h.t.Helper()
	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":        email,
		"password":     password,
		"device_label": "test-device",
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("device-token: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		h.t.Fatalf("device-token decode: %v: %s", err, buf)
	}
	return out.Token
}

// doMultipart posts a multipart/form-data body: one file part per entry
// in parts, keyed by the target drop filename (which doubles as the
// form field name, matching what the server reads from
// r.MultipartForm.File).
func (h *harness) doMultipart(method, path, bearer string, parts map[string][]byte) (*http.Response, []byte) {
	h.t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	for name, data := range parts {
		fw, err := mw.CreateFormFile(name, name)
		if err != nil {
			h.t.Fatalf("CreateFormFile(%s): %v", name, err)
		}
		if _, err := fw.Write(data); err != nil {
			h.t.Fatalf("write part %s: %v", name, err)
		}
	}
	if err := mw.Close(); err != nil {
		h.t.Fatalf("multipart close: %v", err)
	}
	req, err := http.NewRequest(method, h.baseURL+path, buf)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return res, body
}

// zipBundle builds a zip archive in memory from name -> content.
func zipBundle(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// bugReportsHarness stands up a harness with BugReportsDir set to a
// fresh temp directory, plus an admin principal, an alice principal
// with a device token, and a bug-reports-scoped key.
type bugReportsHarness struct {
	h            *harness
	dir          string
	adminPID     uint64
	adminKey     string
	aliceEmail   string
	aliceToken   string
	bugReportKey string
}

func newBugReportsHarness(t *testing.T) *bugReportsHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return newBugReportsHarnessOnStore(t, sqlitetest.Open(t, clk), clk)
}

// newBugReportsHarnessOnStore builds a bugReportsHarness against a
// caller-supplied store/clock, so TestBugReports_Lifecycle_BothBackends
// can run the same scenario against both openSubmissionBackends()
// entries (SQLite always, Postgres when HEROLD_PG_DSN is set).
func newBugReportsHarnessOnStore(t *testing.T, fs store.Store, clk *clock.FakeClock) *bugReportsHarness {
	t.Helper()
	dir := t.TempDir()
	h := newHarnessWithStoreOpts(t, fs, clk, func(o *protoadmin.Options) {
		o.BugReportsDir = dir
	})
	adminPID, adminKey := h.bootstrap("bugreports-admin@example.com")
	const aliceEmail = "bugreports-alice@example.com"
	const alicePassword = "correct-horse-battery-staple"
	h.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    aliceEmail,
		"password": alicePassword,
	})
	aliceToken := h.deviceToken(aliceEmail, alicePassword)
	_, bugReportKey := h.createScopedAPIKey(adminKey, adminPID, []string{"bug-reports"}, false)
	return &bugReportsHarness{
		h: h, dir: dir, adminPID: adminPID, adminKey: adminKey,
		aliceEmail: aliceEmail, aliceToken: aliceToken, bugReportKey: bugReportKey,
	}
}

func TestBugReports_Create_SeparateParts(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h

	res, buf := h.doMultipart("POST", "/api/v1/bug-reports", br.aliceToken, map[string][]byte{
		"report.json":      []byte(bugReportTestMeta),
		"report.md":        []byte("# Bug: Thread list jumps\n"),
		"logs.txt":         []byte("2026-09-17T09:00:00Z WARN: outbox retry\n"),
		"screenshot-1.png": bugReportTestPNG,
		"private.json":     []byte(`{"session": "do-not-leak"}`),
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST bug-reports: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &out); err != nil || out.ID == "" {
		t.Fatalf("decode id: %v: %s", err, buf)
	}

	dropDir := filepath.Join(br.dir, out.ID)
	for _, name := range []string{"report.json", "report.md", "logs.txt", "screenshot-1.png", "private.json", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dropDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dropDir, "report.json"))
	if err != nil || string(got) != bugReportTestMeta {
		t.Errorf("report.json not verbatim: err=%v got=%s", err, got)
	}
	metaRaw, err := os.ReadFile(filepath.Join(dropDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta struct {
		Email string           `json:"email"`
		Sizes map[string]int64 `json:"sizes"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("decode meta.json: %v: %s", err, metaRaw)
	}
	if meta.Email != br.aliceEmail {
		t.Errorf("meta.json email = %q, want %q", meta.Email, br.aliceEmail)
	}
	if meta.Sizes["screenshot-1.png"] != int64(len(bugReportTestPNG)) {
		t.Errorf("meta.json sizes[screenshot-1.png] = %d, want %d", meta.Sizes["screenshot-1.png"], len(bugReportTestPNG))
	}
}

func TestBugReports_Create_ZipBundle(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h

	archive := zipBundle(t, map[string][]byte{
		"report.json":      []byte(bugReportTestMeta),
		"report.md":        []byte("# Bug: zipped\n"),
		"logs.txt":         []byte("zipped log\n"),
		"screenshot-1.png": bugReportTestPNG,
	})
	res, buf := h.doMultipart("POST", "/api/v1/bug-reports", br.aliceToken, map[string][]byte{
		"zip": archive,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST bug-reports (zip): %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &out); err != nil || out.ID == "" {
		t.Fatalf("decode id: %v: %s", err, buf)
	}
	dropDir := filepath.Join(br.dir, out.ID)
	for _, name := range []string{"report.json", "report.md", "logs.txt", "screenshot-1.png", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dropDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

func TestBugReports_Create_RequiresReportJSON(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	res, buf := h.doMultipart("POST", "/api/v1/bug-reports", br.aliceToken, map[string][]byte{
		"report.md": []byte("# no report.json\n"),
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing report.json: %d: %s", res.StatusCode, buf)
	}
}

func TestBugReports_Create_RejectsUnexpectedPart(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	res, buf := h.doMultipart("POST", "/api/v1/bug-reports", br.aliceToken, map[string][]byte{
		"report.json": []byte(bugReportTestMeta),
		"trace.har":   []byte("not part of the drop layout"),
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected part: %d: %s", res.StatusCode, buf)
	}
}

func TestBugReports_Create_RejectsOversizedScreenshot(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	oversized := bytes.Repeat([]byte{0xff}, 8<<20+1) // one byte over the 8 MiB cap
	res, buf := h.doMultipart("POST", "/api/v1/bug-reports", br.aliceToken, map[string][]byte{
		"report.json":      []byte(bugReportTestMeta),
		"screenshot-1.png": oversized,
	})
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized screenshot: %d: %s", res.StatusCode, buf)
	}
}

// TestBugReports_Create_RefusesBugReportsScopedKey proves POST requires
// ScopeEndUser: a bug-reports-only key (which the maintainer's
// bug-fetch uses to list/download/delete) cannot submit a report.
func TestBugReports_Create_RefusesBugReportsScopedKey(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	res, buf := h.doMultipart("POST", "/api/v1/bug-reports", br.bugReportKey, map[string][]byte{
		"report.json": []byte(bugReportTestMeta),
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("bug-reports key POST: %d: %s", res.StatusCode, buf)
	}
}

// createBugReport is a test helper that posts a minimal valid report and
// returns its id.
func (br *bugReportsHarness) createBugReport(t *testing.T) string {
	t.Helper()
	res, buf := br.h.doMultipart("POST", "/api/v1/bug-reports", br.aliceToken, map[string][]byte{
		"report.json":      []byte(bugReportTestMeta),
		"screenshot-1.png": bugReportTestPNG,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("createBugReport: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &out); err != nil || out.ID == "" {
		t.Fatalf("createBugReport decode: %v: %s", err, buf)
	}
	return out.ID
}

func TestBugReports_List_RequiresBugReportsOrAdminScope(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	id := br.createBugReport(t)

	// End-user token: refused.
	res, buf := h.doRequest("GET", "/api/v1/bug-reports", br.aliceToken, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("list with end-user token: %d: %s", res.StatusCode, buf)
	}

	// bug-reports-scoped key: allowed, and the row carries the fields
	// /bug-inbox and bug-fetch read.
	res, buf = h.doRequest("GET", "/api/v1/bug-reports", br.bugReportKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list with bug-reports key: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []struct {
			ID                 string `json:"id"`
			Email              string `json:"email"`
			Title              string `json:"title"`
			Route              string `json:"route"`
			DescriptionEntered bool   `json:"description_entered"`
			ScreenshotCount    int    `json:"screenshot_count"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode list: %v: %s", err, buf)
	}
	if len(out.Items) != 1 || out.Items[0].ID != id {
		t.Fatalf("list items = %+v, want one item with id %s", out.Items, id)
	}
	item := out.Items[0]
	if item.Title != "Thread list jumps after sync" {
		t.Errorf("title = %q", item.Title)
	}
	if item.Route != "thread/t42" {
		t.Errorf("route = %q", item.Route)
	}
	if !item.DescriptionEntered {
		t.Errorf("description_entered = false, want true")
	}
	if item.ScreenshotCount != 1 {
		t.Errorf("screenshot_count = %d, want 1", item.ScreenshotCount)
	}
	if item.Email != br.aliceEmail {
		t.Errorf("email = %q, want %q", item.Email, br.aliceEmail)
	}

	// admin-scoped key (literal ScopeAdmin, e.g. an operator key with
	// --allow-admin-scope) is also admitted.
	_, adminScopedKey := h.createScopedAPIKey(br.adminKey, br.adminPID, []string{"admin"}, true)
	res, buf = h.doRequest("GET", "/api/v1/bug-reports", adminScopedKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list with admin-scoped key: %d: %s", res.StatusCode, buf)
	}
}

func TestBugReports_Get_ReturnsZip(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	id := br.createBugReport(t)

	// Wrong scope: refused.
	res, buf := h.doRequest("GET", "/api/v1/bug-reports/"+id, br.aliceToken, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("get with end-user token: %d: %s", res.StatusCode, buf)
	}

	// Unknown id: 404.
	res, buf = h.doRequest("GET", "/api/v1/bug-reports/20200101T000000Z-deadbeef", br.bugReportKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown id: %d: %s", res.StatusCode, buf)
	}

	// Correct scope: a zip in the drop layout.
	res, buf = h.doRequest("GET", "/api/v1/bug-reports/"+id, br.bugReportKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get: %d: %s", res.StatusCode, buf)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		t.Fatalf("open response as zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"report.json", "screenshot-1.png", "meta.json"} {
		if !names[want] {
			t.Errorf("zip missing %s; entries=%v", want, names)
		}
	}
}

func TestBugReports_Delete_RemovesDrop(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h
	id := br.createBugReport(t)
	dropDir := filepath.Join(br.dir, id)

	// Wrong scope: refused, drop untouched.
	res, buf := h.doRequest("DELETE", "/api/v1/bug-reports/"+id, br.aliceToken, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("delete with end-user token: %d: %s", res.StatusCode, buf)
	}
	if _, err := os.Stat(dropDir); err != nil {
		t.Fatalf("drop removed despite refused delete: %v", err)
	}

	// Correct scope: 204, drop gone.
	res, buf = h.doRequest("DELETE", "/api/v1/bug-reports/"+id, br.bugReportKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d: %s", res.StatusCode, buf)
	}
	if _, err := os.Stat(dropDir); !os.IsNotExist(err) {
		t.Fatalf("drop still present after delete: %v", err)
	}

	// Second delete: 404.
	res, buf = h.doRequest("DELETE", "/api/v1/bug-reports/"+id, br.bugReportKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete: %d: %s", res.StatusCode, buf)
	}
}

// TestBugReports_ScopedKey_RefusedOnUnrelatedAdminEndpoints proves a
// bug-reports-only key grants nothing beyond the bug-reports surface
// (REQ-AUTH-SCOPE-02): it is refused with 403 problem+json on two
// unrelated admin endpoints.
func TestBugReports_ScopedKey_RefusedOnUnrelatedAdminEndpoints(t *testing.T) {
	br := newBugReportsHarness(t)
	h := br.h

	for _, path := range []string{"/api/v1/principals", "/api/v1/server/status"} {
		res, buf := h.doRequest("GET", path, br.bugReportKey, nil)
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("GET %s with bug-reports key: %d: %s", path, res.StatusCode, buf)
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("GET %s: Content-Type = %q, want application/problem+json", path, ct)
		}
	}
}

// TestBugReports_Lifecycle_BothBackends exercises create -> list -> get
// -> delete end to end and asserts each mutation produced an audit-log
// entry (REQ-ADM-300), against both SQLite and (when HEROLD_PG_DSN is
// set) Postgres via openSubmissionBackends.
func TestBugReports_Lifecycle_BothBackends(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			br := newBugReportsHarnessOnStore(t, be.fs, be.clk)
			h := br.h

			id := br.createBugReport(t)

			res, buf := h.doRequest("GET", "/api/v1/bug-reports", br.bugReportKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("list: %d: %s", res.StatusCode, buf)
			}
			var listOut struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			if err := json.Unmarshal(buf, &listOut); err != nil || len(listOut.Items) != 1 || listOut.Items[0].ID != id {
				t.Fatalf("list = %s, want exactly one item with id %s (err=%v)", buf, id, err)
			}

			res, _ = h.doRequest("GET", "/api/v1/bug-reports/"+id, br.bugReportKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("get: %d", res.StatusCode)
			}

			res, buf = h.doRequest("DELETE", "/api/v1/bug-reports/"+id, br.bugReportKey, nil)
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("delete: %d: %s", res.StatusCode, buf)
			}

			ctx := context.Background()
			entries, err := be.fs.Meta().ListAuditLog(ctx, store.AuditLogFilter{Action: "bugreport.create"})
			if err != nil || len(entries) != 1 || entries[0].Subject != "bugreport:"+id {
				t.Fatalf("bugreport.create audit entries = %+v (err=%v), want one for %s", entries, err, id)
			}
			entries, err = be.fs.Meta().ListAuditLog(ctx, store.AuditLogFilter{Action: "bugreport.download"})
			if err != nil || len(entries) != 1 || entries[0].Subject != "bugreport:"+id {
				t.Fatalf("bugreport.download audit entries = %+v (err=%v), want one for %s", entries, err, id)
			}
			entries, err = be.fs.Meta().ListAuditLog(ctx, store.AuditLogFilter{Action: "bugreport.delete"})
			if err != nil || len(entries) != 1 || entries[0].Subject != "bugreport:"+id {
				t.Fatalf("bugreport.delete audit entries = %+v (err=%v), want one for %s", entries, err, id)
			}
		})
	}
}
