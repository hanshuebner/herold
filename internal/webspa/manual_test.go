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

// newManualAssetDirServer builds a Manual Server pointing at a temp
// asset_dir pre-populated with the given files (filename -> content).
func newManualAssetDirServer(t *testing.T, files map[string]string) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	s, err := NewManual(ManualOptions{AssetDir: dir})
	if err != nil {
		t.Fatalf("NewManual: %v", err)
	}
	return s
}

// doManual issues a GET against a Manual server's handler and returns the
// recorded response.
func doManual(t *testing.T, s *Server, urlPath string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, urlPath, nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Result()
}

func TestManual_ServesIndex(t *testing.T) {
	s := newManualAssetDirServer(t, map[string]string{
		"index.html": "<!doctype html><html><body>Manual root</body></html>",
	})
	resp := doManual(t, s, "/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type=%q, want text/html", ct)
	}
}

func TestManual_ServesUserInstall(t *testing.T) {
	s := newManualAssetDirServer(t, map[string]string{
		"index.html":              "<!doctype html><html><body>root</body></html>",
		"user/install/index.html": "<!doctype html><html><body>User install chapter</body></html>",
	})
	resp := doManual(t, s, "/user/install")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /user/install: status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "User install chapter") {
		t.Errorf("body=%q, want to contain chapter content", string(body))
	}
}

func TestManual_ServesAdminInstall(t *testing.T) {
	s := newManualAssetDirServer(t, map[string]string{
		"index.html":               "<!doctype html><html><body>root</body></html>",
		"admin/install/index.html": "<!doctype html><html><body>Admin install chapter</body></html>",
	})
	resp := doManual(t, s, "/admin/install")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/install: status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Admin install chapter") {
		t.Errorf("body=%q, want to contain chapter content", string(body))
	}
}

func TestManual_AssetDir_MissingIndex_Rejected(t *testing.T) {
	dir := t.TempDir()
	_, err := NewManual(ManualOptions{AssetDir: dir})
	if err == nil {
		t.Fatal("expected error when asset_dir lacks index.html")
	}
}

func TestManual_AssetDir_MissingDir_Rejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := NewManual(ManualOptions{AssetDir: dir})
	if err == nil {
		t.Fatal("expected error for missing asset_dir")
	}
}

func TestManual_EmbeddedFS_Default(t *testing.T) {
	// No AssetDir -> embedded placeholder. The placeholder has
	// <h1>Herold ...</h1> content.
	s, err := NewManual(ManualOptions{})
	if err != nil {
		t.Fatalf("NewManual: %v", err)
	}
	resp := doManual(t, s, "/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<title>Herold") {
		t.Errorf("embedded index.html missing Herold title; body=%q", string(body))
	}
}

func TestManual_CSP_Header_Set(t *testing.T) {
	s := newManualAssetDirServer(t, map[string]string{
		"index.html": "<!doctype html><html><body>x</body></html>",
	})
	resp := doManual(t, s, "/")
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing CSP header")
	}
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP=%q missing %q", csp, want)
		}
	}
	// The manual CSP must NOT include wss:// (no WebSocket needed).
	if strings.Contains(csp, "wss://") {
		t.Errorf("CSP=%q must not contain wss:// (manual is static)", csp)
	}
}

func TestManual_404_MissingAsset(t *testing.T) {
	s := newManualAssetDirServer(t, map[string]string{
		"index.html": "<!doctype html><html><body>x</body></html>",
	})
	resp := doManual(t, s, "/missing-asset.css")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /missing-asset.css: status=%d, want 404", resp.StatusCode)
	}
}

func TestManual_SPA_Fallback_ExtensionFree(t *testing.T) {
	// Extension-free paths that do not match a file fall back to index.html
	// (the redirect page serves as the root for extension-free navigation).
	s := newManualAssetDirServer(t, map[string]string{
		"index.html": "<!doctype html><html><body>root</body></html>",
	})
	resp := doManual(t, s, "/some/unknown/path")
	defer resp.Body.Close()
	// Extension-free unknown paths fall back to index.html (SPA router fallback).
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /some/unknown/path: status=%d, want 200", resp.StatusCode)
	}
}

func TestManual_RejectsPOST(t *testing.T) {
	s := newManualAssetDirServer(t, map[string]string{
		"index.html": "<!doctype html><html><body>x</body></html>",
	})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status=%d, want 405", rr.Code)
	}
}
