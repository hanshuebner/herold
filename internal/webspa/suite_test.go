package webspa

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small helper that creates dir/name with the given
// content. The intermediate directories are created on demand.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// newAssetDirServer builds a Server pointing at a temp asset_dir
// pre-populated with the given files (filename -> content).
func newAssetDirServer(t *testing.T, files map[string]string, publicHost string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	s, err := New(Options{SuiteAssetDir: dir, PublicHost: publicHost})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

// do issues a GET against the server's handler and returns the
// recorded response.
func do(t *testing.T, s *Server, urlPath string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, urlPath, nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Result()
}

func TestSpa_AssetDir_ServesIndex(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html><body>hi</body></html>",
	}, "")
	resp := do(t, s, "/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// The response contains the original index.html content plus injected
	// meta tags (REQ-CLOG-12). Check that the original content is present
	// rather than requiring an exact match.
	if !strings.Contains(string(body), "<html><body>hi</body></html>") {
		t.Errorf("body=%q, want to contain index.html content", string(body))
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control=%q, want no-cache", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type=%q, want text/html...", got)
	}
}

func TestSpa_AssetDir_ServesIndexHtml(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html>x</html>",
	}, "")
	resp := do(t, s, "/index.html")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control=%q, want no-cache", got)
	}
}

func TestSpa_AssetDir_ServesHashedAsset(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html":           "<html></html>",
		"assets/app-Abc123.js": "console.log('hi');",
	}, "")
	resp := do(t, s, "/assets/app-Abc123.js")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "console.log('hi');" {
		t.Errorf("body=%q", string(body))
	}
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "max-age=31536000") || !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control=%q, want immutable max-age=31536000", cc)
	}
}

func TestSpa_AssetDir_NonHashedAsset(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html":  "<html></html>",
		"favicon.ico": "icondata",
	}, "")
	resp := do(t, s, "/favicon.ico")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "max-age=3600") {
		t.Errorf("Cache-Control=%q, want max-age=3600", cc)
	}
	if strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control=%q must not be immutable for non-hashed asset", cc)
	}
}

func TestSpa_SPA_Fallback_RouterPath(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html>SPA</html>",
	}, "")
	resp := do(t, s, "/some/spa/route")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// The response contains the original index.html plus injected meta
	// tags (REQ-CLOG-12); check for the original content substring.
	if !strings.Contains(string(body), "<html>SPA</html>") {
		t.Errorf("body=%q, want to contain index.html content", string(body))
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control=%q, want no-cache for SPA fallback", got)
	}
}

func TestSpa_SPA_Fallback_AssetMissing(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html></html>",
	}, "")
	resp := do(t, s, "/missing-asset.js")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (must not return SPA shell for missing asset)", resp.StatusCode)
	}
}

func TestSpa_SPA_Fallback_NestedAssetMissing(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html></html>",
	}, "")
	resp := do(t, s, "/assets/app-NotPresent.js")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestSpa_ReservedAPIPath_404(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html></html>",
	}, "")
	cases := []string{
		"/api/v1/foo",
		"/.well-known/jmap",
		"/jmap",
		"/jmap/session",
		"/chat/ws",
		"/proxy/image",
		"/hooks/inbound",
		"/login",
		"/logout",
		"/auth/callback",
		"/oidc/callback",
		// /manual/ is reserved: the suite SPA catch-all must not absorb it.
		// The public mux mounts the manual handler at /manual/ before the
		// suite catch-all so this path never reaches the SPA handler in
		// production; the defensive guard keeps a future mis-ordering visible.
		"/manual/user/install",
		"/manual/",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			resp := do(t, s, p)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("path=%s status=%d, want 404", p, resp.StatusCode)
			}
		})
	}
}

func TestSpa_CSP_Header_Set(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html":           "<html></html>",
		"assets/app-Abc123.js": "x",
		"favicon.ico":          "y",
	}, "mail.example.com")
	cases := []string{"/", "/index.html", "/some/route", "/favicon.ico", "/assets/app-Abc123.js", "/missing.png"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			resp := do(t, s, p)
			defer resp.Body.Close()
			csp := resp.Header.Get("Content-Security-Policy")
			if csp == "" {
				t.Fatalf("missing CSP header on %s", p)
			}
			for _, want := range []string{
				"default-src 'self'",
				"script-src 'self'",
				"style-src 'self' 'unsafe-inline'",
				"img-src 'self' https: data: blob:",
				"connect-src 'self' wss://mail.example.com",
				"font-src 'self' data:",
				"frame-ancestors 'none'",
				"base-uri 'self'",
			} {
				if !strings.Contains(csp, want) {
					t.Errorf("CSP=%q missing %q", csp, want)
				}
			}
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options=%q, want nosniff", got)
			}
			if got := resp.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
				t.Errorf("Referrer-Policy=%q", got)
			}
			if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options=%q, want DENY", got)
			}
		})
	}
}

func TestSpa_CSP_Header_NoPublicHost(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html></html>",
	}, "")
	resp := do(t, s, "/")
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("CSP=%q must contain connect-src 'self'", csp)
	}
	if strings.Contains(csp, "wss://") {
		t.Errorf("CSP=%q should not include wss:// when PublicHost empty", csp)
	}
}

func TestSpa_EmbeddedFS_Default(t *testing.T) {
	// No SuiteAssetDir -> embedded suite placeholder. The placeholder
	// has a <title>Herold</title> and references the make build-web
	// build step; we assert on the title substring rather than the
	// whole body so a future placeholder rewrite doesn't break the
	// test as long as the contract holds.
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp := do(t, s, "/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, "<title>Herold</title>") {
		t.Errorf("embedded index.html missing <title>Herold</title>; body=%q", bs)
	}
}

// TestSpa_AssetDir_AcceptsRelative documents the deliberate relaxation
// of the absolute-path requirement: dev configs may set
// suite_asset_dir to a relative path (the equivalent of pointing
// directly at web/apps/suite/dist for hot-reload), so relative paths
// must round-trip. Relative paths are resolved via filepath.Abs at
// construction time so later cwd changes do not move the asset root.
func TestSpa_AssetDir_AcceptsRelative(t *testing.T) {
	abs := t.TempDir()
	if err := os.WriteFile(filepath.Join(abs, "index.html"),
		[]byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)
	if err := os.Chdir(parent); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if _, err := New(Options{SuiteAssetDir: base}); err != nil {
		t.Fatalf("relative asset_dir should be accepted: %v", err)
	}
}

func TestSpa_AssetDir_RejectsMissingIndex(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(Options{SuiteAssetDir: dir}); err == nil {
		t.Fatal("expected error when asset_dir lacks index.html")
	}
}

func TestSpa_AssetDir_RejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := New(Options{SuiteAssetDir: dir}); err == nil {
		t.Fatal("expected error for missing asset_dir")
	}
}

func TestSpa_AssetDir_RejectsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{SuiteAssetDir: f}); err == nil {
		t.Fatal("expected error when asset_dir is a file")
	}
}

func TestSpa_PathTraversal_404(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html></html>",
	}, "")
	// httptest cleans these but the explicit paths through the
	// handler tests defence-in-depth -- Clean produces "/etc/passwd"
	// for "/../../etc/passwd" which becomes the SPA-fallback case.
	resp := do(t, s, "/../../etc/passwd")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		t.Errorf("traversal path: status=%d", resp.StatusCode)
	}
	// The body must NOT be /etc/passwd.
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "root:") {
		t.Errorf("traversal returned passwd content: %q", string(body))
	}
}

func TestSpa_HEAD_Index(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html>hi</html>",
	}, "")
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", len(body))
	}
}

func TestSpa_RejectsPOST(t *testing.T) {
	s, _ := newAssetDirServer(t, map[string]string{
		"index.html": "<html></html>",
	}, "")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status=%d, want 405", rr.Code)
	}
}

func TestSpa_isContentAddressed(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"app-Df8z2X.js", true},
		{"index-9f3b1a.css", true},
		{"vendor.4f1c2a.js", true},
		{"chunk-abcDEF1.mjs", true},
		{"favicon.ico", false},
		{"robots.txt", false},
		{"app.js", false},
		{"index.html", false},
		// pure-letter or pure-digit suffixes do not count as a hash.
		{"img-icon.png", false},
		{"asset-12345.png", false},  // no alpha
		{"asset-ABCDEF.png", false}, // no digit
		// Real Vite 4-style hash with both alpha and digit.
		{"app-DfNz2k.js", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isContentAddressed(c.name)
			if got != c.want {
				t.Errorf("isContentAddressed(%q)=%v, want %v", c.name, got, c.want)
			}
		})
	}
}

func TestSpa_hasFileExtension(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/foo.js", true},
		{"/path/to/asset.css", true},
		{"/", false},
		{"/login", false},
		{"/some/spa/route", false},
		{"/.gitkeep", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := hasFileExtension(c.path)
			if got != c.want {
				t.Errorf("hasFileExtension(%q)=%v, want %v", c.path, got, c.want)
			}
		})
	}
}
