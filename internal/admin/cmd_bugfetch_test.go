package admin

// cmd_bugfetch_test.go exercises `herold bug-fetch` against POST/GET/DELETE
// /api/v1/bug-reports (issue #416):
//
//   - runBugFetch's dry run lists without writing or deleting.
//   - runBugFetch's real run downloads each report's zip, writes it as a
//     drop directory (0700 dir, 0600 files, STATUS=new), and deletes it
//     on the server unless --keep is set.
//   - "no reports" is reported when the queue is empty.
//   - bugFetchCredentials resolves --server-url / bug-reports.toml /
//     $HEROLD_BUG_REPORTS_KEY per the documented precedence.
//   - an end-to-end CLI round trip against a real running server: two
//     reports posted via the multipart API are fetched, written to a
//     temp dir, and removed from the server.

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
	"strings"
	"testing"
	"time"
)

// fakeBugReportsServer is an in-process httptest-free stand-in for the
// bug-reports REST surface, driving runBugFetch directly against a
// bugReportsClient without a network round trip. It models exactly the
// list/download/delete contract POST /api/v1/bug-reports's siblings
// expose, so runBugFetch's control flow (dry-run, write, delete-unless-keep,
// failure reporting) is tested independently of protoadmin's HTTP wiring
// (covered separately by internal/protoadmin/bugreports_test.go and by
// TestBugFetch_EndToEnd below).
type fakeBugReportsServer struct {
	items     []bugReportSummary
	zips      map[string][]byte
	deleted   []string
	failList  bool
	failGetID string
}

func (f *fakeBugReportsServer) list(_ context.Context) ([]bugReportSummary, error) {
	if f.failList {
		return nil, fmt.Errorf("list: status 500: boom")
	}
	return f.items, nil
}

func (f *fakeBugReportsServer) download(_ context.Context, id string) ([]byte, error) {
	if id == f.failGetID {
		return nil, fmt.Errorf("download: status 500: boom")
	}
	data, ok := f.zips[id]
	if !ok {
		return nil, fmt.Errorf("download: status 404: not found")
	}
	return data, nil
}

func (f *fakeBugReportsServer) delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

var _ bugFetchServer = (*fakeBugReportsServer)(nil)

// minimalReportZip builds a zip matching what GET /api/v1/bug-reports/{id}
// actually serves: private/private.json nested under the repro-secrets
// subdirectory, not a flat private.json (re #416 coordinator correction).
func minimalReportZip(t *testing.T, sketch string) []byte {
	t.Helper()
	meta := fmt.Sprintf(`{"kind":"bug","sketch":%q,"descriptionEntered":true,"screenshotCount":1,"context":{"route":"thread/t1"}}`, sketch)
	return zipBundleFiles(t, map[string][]byte{
		"report.json":          []byte(meta),
		"report.md":            []byte("# " + sketch + "\n"),
		"logs.txt":             []byte("log line\n"),
		"crash.txt":            []byte("java.lang.IndexOutOfBoundsException\n\tat ...\n"),
		"screenshot-1.png":     []byte("\x89PNG\r\n\x1a\nfake"),
		"meta.json":            []byte(`{"principal_id":1,"email":"alice@example.local"}`),
		"private/private.json": []byte(`{"session":"do-not-leak"}`),
	})
}

func TestRunBugFetch_DryRun_ListsWithoutWriting(t *testing.T) {
	fake := &fakeBugReportsServer{items: []bugReportSummary{
		{ID: "r1", ReceivedAt: "2026-09-17T09:00:00Z", Title: "Thread jumps", ScreenshotCount: 1},
		{ID: "r2", ReceivedAt: "2026-09-17T09:05:00Z", Title: "Compose loses draft", ScreenshotCount: 2},
	}}
	out := t.TempDir()
	buf := &bytes.Buffer{}
	err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: out, DryRun: true})
	if err != nil {
		t.Fatalf("runBugFetch dry-run: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "would fetch r1") || !strings.Contains(got, "would fetch r2") {
		t.Fatalf("dry run output missing entries: %s", got)
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("dry run deleted reports: %v", fake.deleted)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 0 {
		t.Fatalf("dry run wrote %d entries into --out", len(entries))
	}
}

func TestRunBugFetch_NoReports(t *testing.T) {
	fake := &fakeBugReportsServer{}
	buf := &bytes.Buffer{}
	if err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: t.TempDir()}); err != nil {
		t.Fatalf("runBugFetch: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "no reports" {
		t.Fatalf("output = %q, want %q", buf.String(), "no reports")
	}
}

func TestRunBugFetch_RealRun_WritesAndDeletes(t *testing.T) {
	fake := &fakeBugReportsServer{
		items: []bugReportSummary{{ID: "r1", Title: "Thread jumps"}},
		zips:  map[string][]byte{"r1": minimalReportZip(t, "Thread jumps")},
	}
	out := t.TempDir()
	buf := &bytes.Buffer{}
	if err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: out}); err != nil {
		t.Fatalf("runBugFetch: %v", err)
	}
	dir := filepath.Join(out, "r1")
	if !strings.Contains(buf.String(), "bug-fetch: wrote "+dir) {
		t.Fatalf("output missing wrote line: %s", buf.String())
	}
	for _, name := range []string{"report.json", "report.md", "logs.txt", "crash.txt", "screenshot-1.png", "meta.json", "STATUS"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if !st.IsDir() && st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, st.Mode().Perm())
		}
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("drop dir mode = %v err=%v, want 0700", st.Mode(), err)
	}
	// private/private.json -- the drop layout's repro-secrets
	// subdirectory (.claude/commands/bug-inbox.md) -- is extracted at
	// its nested path, not flattened to the drop root.
	privateDir := filepath.Join(dir, "private")
	if st, err := os.Stat(privateDir); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("private/ = %v err=%v, want a 0700 directory", st, err)
	}
	if st, err := os.Stat(filepath.Join(privateDir, "private.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("private/private.json mode = %v err=%v, want 0600", st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "private.json")); !os.IsNotExist(err) {
		t.Errorf("private.json extracted flat (err=%v), want only under private/", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "STATUS")); string(got) != "new" {
		t.Errorf("STATUS = %q, want new", got)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "r1" {
		t.Fatalf("deleted = %v, want [r1]", fake.deleted)
	}
}

func TestRunBugFetch_Keep_SkipsDelete(t *testing.T) {
	fake := &fakeBugReportsServer{
		items: []bugReportSummary{{ID: "r1"}},
		zips:  map[string][]byte{"r1": minimalReportZip(t, "kept")},
	}
	buf := &bytes.Buffer{}
	if err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: t.TempDir(), Keep: true}); err != nil {
		t.Fatalf("runBugFetch: %v", err)
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("--keep deleted reports: %v", fake.deleted)
	}
}

func TestRunBugFetch_DownloadFailure_ReportsAndReturnsError(t *testing.T) {
	fake := &fakeBugReportsServer{
		items:     []bugReportSummary{{ID: "r1"}},
		failGetID: "r1",
	}
	buf := &bytes.Buffer{}
	err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: t.TempDir()})
	if err == nil {
		t.Fatalf("runBugFetch: want error, got nil")
	}
	if !strings.Contains(buf.String(), "r1:") {
		t.Fatalf("output missing failure line: %s", buf.String())
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("failed download still deleted: %v", fake.deleted)
	}
}

func TestRunBugFetch_ListFailure_ReturnsClearError(t *testing.T) {
	fake := &fakeBugReportsServer{failList: true}
	buf := &bytes.Buffer{}
	err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "bug-fetch:") {
		t.Fatalf("runBugFetch list failure = %v, want a bug-fetch-prefixed error", err)
	}
}

func TestRunBugFetch_Limit(t *testing.T) {
	fake := &fakeBugReportsServer{items: []bugReportSummary{
		{ID: "r1"}, {ID: "r2"}, {ID: "r3"},
	}}
	buf := &bytes.Buffer{}
	err := runBugFetch(context.Background(), buf, fake, bugFetchOptions{OutDir: t.TempDir(), DryRun: true, Limit: 2})
	if err != nil {
		t.Fatalf("runBugFetch: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "r1") || !strings.Contains(got, "r2") || strings.Contains(got, "r3") {
		t.Fatalf("limit not applied: %s", got)
	}
}

func TestBugFetchCredentials_Precedence(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "bug-reports.toml")
	SetBugReportsCredentialsPath(credPath)
	t.Cleanup(func() { SetBugReportsCredentialsPath("") })

	// Neither file nor flag/env: clear error.
	if _, _, err := bugFetchCredentials(""); err == nil {
		t.Fatalf("want error with no server URL configured")
	}

	// File supplies both.
	if err := os.WriteFile(credPath, []byte("server_url = \"http://file.example\"\napi_key = \"hk_file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, key, err := bugFetchCredentials("")
	if err != nil || base != "http://file.example" || key != "hk_file" {
		t.Fatalf("file-sourced creds = %q, %q, %v", base, key, err)
	}

	// --server-url overrides the file's server_url.
	base, key, err = bugFetchCredentials("http://flag.example")
	if err != nil || base != "http://flag.example" || key != "hk_file" {
		t.Fatalf("flag override = %q, %q, %v", base, key, err)
	}

	// $HEROLD_BUG_REPORTS_KEY overrides the file's api_key.
	t.Setenv(bugFetchAPIKeyEnv, "hk_env")
	base, key, err = bugFetchCredentials("")
	if err != nil || base != "http://file.example" || key != "hk_env" {
		t.Fatalf("env override = %q, %q, %v", base, key, err)
	}
}

// zipBundleFiles builds a zip archive in memory from name -> content.
func zipBundleFiles(t *testing.T, entries map[string][]byte) []byte {
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

func TestWriteBugReportDrop_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	archive := zipBundleFiles(t, map[string][]byte{
		"report.json":      []byte(`{"kind":"bug"}`),
		"../../etc/passwd": []byte("root:x:0:0"),
	})
	dir := filepath.Join(root, "drop")
	err := writeBugReportDrop(dir, archive)
	if err == nil || !strings.Contains(err.Error(), "unsafe entry") {
		t.Fatalf("writeBugReportDrop with a traversal entry = %v, want an unsafe-entry error", err)
	}
	// Nothing escaped root: no "passwd" file anywhere under or above dir
	// within the temp root.
	if _, statErr := os.Stat(filepath.Join(root, "..", "etc", "passwd")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal entry escaped the drop dir")
	}
}

// ---- end-to-end CLI round trip -------------------------------------------

// TestBugFetch_EndToEnd drives `herold bug-fetch` against a fully wired
// running server: two bundles are posted via POST /api/v1/bug-reports
// with an end-user device token, then fetched and deleted with a
// bug-reports-scoped API key.
func TestBugFetch_EndToEnd(t *testing.T) {
	publicAddr, adminKey, _ := jmapBootstrapFixture(t)
	adminPID := whoamiPrincipalID(t, publicAddr, adminKey)

	const aliceEmail = "bugfetch-alice@example.com"
	const alicePassword = "correct-horse-battery-staple"
	createPrincipalHTTP(t, publicAddr, adminKey, aliceEmail, alicePassword)
	aliceToken := deviceTokenHTTP(t, publicAddr, aliceEmail, alicePassword)
	bugKey := createScopedAPIKeyHTTP(t, publicAddr, adminKey, adminPID, []string{"bug-reports"}, false)

	id1 := postBugReportHTTP(t, publicAddr, aliceToken, "Thread list jumps")
	id2 := postBugReportHTTP(t, publicAddr, aliceToken, "Compose loses draft")

	out := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		root := NewRootCmd()
		stdout := &bytes.Buffer{}
		root.SetOut(stdout)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{
			"bug-fetch",
			"--server-url", "http://" + publicAddr,
			"--out", out,
		}, args...))
		t.Setenv(bugFetchAPIKeyEnv, bugKey)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("bug-fetch %v: %v\n%s", args, err, stdout.String())
		}
		return stdout.String()
	}

	// Dry run: lists both, writes and deletes nothing.
	dry := run("--dry-run")
	if !strings.Contains(dry, "Thread list jumps") || !strings.Contains(dry, "Compose loses draft") {
		t.Fatalf("dry run did not list both reports:\n%s", dry)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("dry run wrote %d entries into --out", len(entries))
	}
	if remaining := listBugReportsHTTP(t, publicAddr, bugKey); len(remaining) != 2 {
		t.Fatalf("dry run changed the queue: %v", remaining)
	}

	// Real run: both drops on disk, both removed from the server.
	got := run()
	for _, id := range []string{id1, id2} {
		dir := filepath.Join(out, id)
		if !strings.Contains(got, "bug-fetch: wrote "+dir) {
			t.Errorf("output does not report %s:\n%s", dir, got)
		}
		for _, p := range []string{"report.json", "screenshot-1.png", "crash.txt", "meta.json", "STATUS", filepath.Join("private", "private.json")} {
			if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
				t.Errorf("%s: missing %s: %v", id, p, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "private.json")); !os.IsNotExist(err) {
			t.Errorf("%s: private.json extracted flat (err=%v), want only under private/", id, err)
		}
	}
	if remaining := listBugReportsHTTP(t, publicAddr, bugKey); len(remaining) != 0 {
		t.Fatalf("reports still queued after fetch: %v", remaining)
	}

	// Nothing left: the next run is a no-op.
	again := run()
	if !strings.Contains(again, "no reports") {
		t.Fatalf("second run should find nothing:\n%s", again)
	}
}

func whoamiPrincipalID(t *testing.T, publicAddr, apiKey string) uint64 {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/api/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("whoami decode: %v body=%s", err, raw)
	}
	return out.PrincipalID
}

func createPrincipalHTTP(t *testing.T, publicAddr, adminKey, email, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"email": email, "password": password})
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/api/v1/principals", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create principal: status=%d body=%s", resp.StatusCode, raw)
	}
}

func deviceTokenHTTP(t *testing.T, publicAddr, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"email": email, "password": password, "device_label": "test"})
	resp, err := http.Post("http://"+publicAddr+"/api/v1/auth/device-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("device-token: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("device-token: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("device-token decode: %v body=%s", err, raw)
	}
	return out.Token
}

func createScopedAPIKeyHTTP(t *testing.T, publicAddr, callerKey string, pid uint64, scope []string, allowAdmin bool) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"label":             "bug-fetch-test-key",
		"scope":             scope,
		"allow_admin_scope": allowAdmin,
	})
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s/api/v1/principals/%d/api-keys", publicAddr, pid), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+callerKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create scoped api key: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create scoped api key: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("create scoped api key decode: %v body=%s", err, raw)
	}
	return out.Key
}

// postBugReportHTTP posts a minimal valid multipart bug report and
// returns its id.
func postBugReportHTTP(t *testing.T, publicAddr, bearer, sketch string) string {
	t.Helper()
	meta := fmt.Sprintf(`{"kind":"bug","sketch":%q,"descriptionEntered":true,"screenshotCount":1,"context":{"route":"thread/t1"}}`, sketch)
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	for name, content := range map[string][]byte{
		"report.json":      []byte(meta),
		"screenshot-1.png": []byte("\x89PNG\r\n\x1a\nfake"),
		"crash.txt":        []byte("java.lang.IndexOutOfBoundsException\n\tat ...\n"),
		"private.json":     []byte(`{"session":"do-not-leak"}`),
	} {
		fw, err := mw.CreateFormFile(name, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/api/v1/bug-reports", buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post bug report: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post bug report: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		t.Fatalf("post bug report decode: %v body=%s", err, raw)
	}
	return out.ID
}

func listBugReportsHTTP(t *testing.T, publicAddr, bearer string) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/api/v1/bug-reports", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list bug reports: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list bug reports: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("list bug reports decode: %v body=%s", err, raw)
	}
	ids := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		ids = append(ids, it.ID)
	}
	return ids
}
