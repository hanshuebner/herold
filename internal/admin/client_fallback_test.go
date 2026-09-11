package admin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// re #315: the CLI's admin client pins server_url in credentials.toml at
// bootstrap time. When the admin listener later moves (system.toml
// changes but nobody re-bootstraps), the stale stored URL must not win
// silently over the listener --system-config names -- these tests cover
// the read-time fallback: retry against the config-derived URL, warn
// once, and rewrite the file so the next call needs no fallback.

// newFallbackTestListener starts a plain HTTP server bound to an
// OS-assigned loopback port and returns its dial address ("host:port")
// alongside a teardown func. The handler always answers 200 with an
// empty JSON object -- these tests only exercise transport-level
// reachability, not any real admin API semantics.
func newFallbackTestListener(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	return ln.Addr().String(), srv.Close
}

// reservedClosedAddr returns a loopback "host:port" that reliably refuses
// connections: a real port is reserved via Listen and immediately
// released, so nothing else is bound there for the life of the test.
func reservedClosedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// writeFallbackSystemConfig writes a minimal, strictly valid system.toml
// with a "kind = \"admin\"" listener at adminAddr (and a throwaway public
// listener), returning the file's path. This is the config
// clientFromGlobals derives the fallback admin URL from.
func writeFallbackSystemConfig(t *testing.T, adminAddr string) string {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	content := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
port_report_file = %q

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "http"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = %q
protocol = "http"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, filepath.Join(dir, "ports.toml"), certPath, keyPath, filepath.Join(dir, "db.sqlite"), adminAddr)

	path := filepath.Join(dir, "system.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	return path
}

// writeFallbackCredentials seeds a scratch credentials.toml and points
// the package-wide credentials path at it for the duration of the test.
func writeFallbackCredentials(t *testing.T, apiKey, serverURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.toml")
	raw, err := toml.Marshal(credentialsFile{APIKey: apiKey, ServerURL: serverURL})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	SetCredentialsPath(path)
	t.Cleanup(func() { SetCredentialsPath("") })
	return path
}

// captureStderr redirects the process's real os.Stderr for the duration
// of fn and returns everything written to it. clientFromGlobals warns
// directly to os.Stderr (not through cobra's captured writer), so this
// is the only way to observe the warning end-to-end.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

func readCredentialsFile(t *testing.T, path string) credentialsFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	var f credentialsFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse credentials: %v", err)
	}
	return f
}

// TestClientFromGlobals_StaleServerURL_FallsBackAndRewrites covers the
// marquee case: credentials.toml's server_url points at a closed port,
// --system-config names a listener that is actually reachable. The
// first call must succeed by retrying against the config-derived URL,
// warn exactly once naming both URLs and the credentials file, and
// rewrite the file so a second call needs no retry.
func TestClientFromGlobals_StaleServerURL_FallsBackAndRewrites(t *testing.T) {
	goodAddr, closeGood := newFallbackTestListener(t)
	defer closeGood()
	staleAddr := reservedClosedAddr(t)

	cfgPath := writeFallbackSystemConfig(t, goodAddr)
	credPath := writeFallbackCredentials(t, "testkey", "http://"+staleAddr)

	g := &globalOptions{configPath: cfgPath}

	var client *Client
	stderr := captureStderr(t, func() {
		var err error
		client, err = clientFromGlobals(g)
		if err != nil {
			t.Fatalf("clientFromGlobals: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.do(ctx, http.MethodGet, "/api/v1/anything", nil, nil); err != nil {
			t.Fatalf("do: %v", err)
		}
	})

	if !strings.Contains(stderr, "http://"+staleAddr) {
		t.Errorf("warning missing stale URL: %q", stderr)
	}
	if !strings.Contains(stderr, "http://"+goodAddr) {
		t.Errorf("warning missing derived URL: %q", stderr)
	}
	if !strings.Contains(stderr, credPath) {
		t.Errorf("warning missing credentials path: %q", stderr)
	}
	if n := strings.Count(stderr, "unreachable"); n != 1 {
		t.Errorf("expected exactly one warning, got %d: %q", n, stderr)
	}

	f := readCredentialsFile(t, credPath)
	if f.ServerURL != "http://"+goodAddr {
		t.Errorf("credentials file server_url = %q, want %q", f.ServerURL, "http://"+goodAddr)
	}
	if f.APIKey != "testkey" {
		t.Errorf("credentials file api_key = %q, want unchanged %q", f.APIKey, "testkey")
	}

	// A second call from a fresh client (as a second CLI invocation
	// would build) now finds matching URLs and needs no warning.
	stderr2 := captureStderr(t, func() {
		client2, err := clientFromGlobals(g)
		if err != nil {
			t.Fatalf("clientFromGlobals (2nd): %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client2.do(ctx, http.MethodGet, "/api/v1/anything", nil, nil); err != nil {
			t.Fatalf("do (2nd): %v", err)
		}
	})
	if stderr2 != "" {
		t.Errorf("second invocation should be silent, got %q", stderr2)
	}
}

// TestClientFromGlobals_StaleServerURL_NoSystemConfig_ErrorNamesFile
// covers the case with no usable fallback: --system-config is unset (or
// unresolvable), so the client cannot derive an alternate URL. The
// connection error must name the credentials file and the --server-url
// override so the operator knows where to look.
func TestClientFromGlobals_StaleServerURL_NoSystemConfig_ErrorNamesFile(t *testing.T) {
	staleAddr := reservedClosedAddr(t)
	credPath := writeFallbackCredentials(t, "testkey", "http://"+staleAddr)

	g := &globalOptions{configPath: ""}
	client, err := clientFromGlobals(g)
	if err != nil {
		t.Fatalf("clientFromGlobals: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.do(ctx, http.MethodGet, "/api/v1/anything", nil, nil)
	if err == nil {
		t.Fatal("do: expected error against a closed port, got nil")
	}
	if !strings.Contains(err.Error(), credPath) {
		t.Errorf("error should name the credentials file %q: %v", credPath, err)
	}
	if !strings.Contains(err.Error(), "--server-url") {
		t.Errorf("error should name the --server-url flag: %v", err)
	}
}

// TestClientFromGlobals_MatchingServerURL_NoWarningNoRewrite covers the
// steady state: the stored server_url already matches the URL
// --system-config derives, so calls succeed directly with no warning and
// no rewrite of the credentials file.
func TestClientFromGlobals_MatchingServerURL_NoWarningNoRewrite(t *testing.T) {
	addr, closeSrv := newFallbackTestListener(t)
	defer closeSrv()

	cfgPath := writeFallbackSystemConfig(t, addr)
	credPath := writeFallbackCredentials(t, "testkey", "http://"+addr)
	before := readCredentialsFile(t, credPath)

	g := &globalOptions{configPath: cfgPath}
	var client *Client
	stderr := captureStderr(t, func() {
		var err error
		client, err = clientFromGlobals(g)
		if err != nil {
			t.Fatalf("clientFromGlobals: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.do(ctx, http.MethodGet, "/api/v1/anything", nil, nil); err != nil {
			t.Fatalf("do: %v", err)
		}
	})
	if stderr != "" {
		t.Errorf("matching URLs should be silent, got %q", stderr)
	}
	after := readCredentialsFile(t, credPath)
	if after != before {
		t.Errorf("credentials file changed: before=%+v after=%+v", before, after)
	}
}

// TestClientFromGlobals_ExplicitServerURLFlag_NoFallback verifies an
// explicit --server-url always wins outright and wires no fallback, even
// when a --system-config listener would resolve to something else and
// the flag's URL is unreachable.
func TestClientFromGlobals_ExplicitServerURLFlag_NoFallback(t *testing.T) {
	goodAddr, closeGood := newFallbackTestListener(t)
	defer closeGood()
	staleAddr := reservedClosedAddr(t)

	cfgPath := writeFallbackSystemConfig(t, goodAddr)
	// No stored credentials file at all -- the flag alone drives this.
	SetCredentialsPath(filepath.Join(t.TempDir(), "credentials.toml"))
	t.Cleanup(func() { SetCredentialsPath("") })

	g := &globalOptions{configPath: cfgPath, serverURL: "http://" + staleAddr}
	client, err := clientFromGlobals(g)
	if err != nil {
		t.Fatalf("clientFromGlobals: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.do(ctx, http.MethodGet, "/api/v1/anything", nil, nil)
	if err == nil {
		t.Fatal("do: expected error, --server-url must not fall back to the config-derived URL")
	}
}
