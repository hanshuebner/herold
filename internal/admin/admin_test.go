package admin

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// minimalConfigFixture writes a system.toml and the associated cert/key
// pair under a temp dir. It returns the system.toml path and the resolved
// *sysconfig.Config.
func minimalConfigFixture(t *testing.T) (string, *sysconfig.Config) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	systomlPath := filepath.Join(dir, "system.toml")
	toml := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, certPath, keyPath, filepath.Join(dir, "db.sqlite"),
		certPath, keyPath, certPath, keyPath)
	if err := os.WriteFile(systomlPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return systomlPath, cfg
}

// startTestServer boots the full server against the minimalConfigFixture
// and returns the listener addresses plus a teardown. The caller cancels
// ctx and waits for doneCh to close.
func startTestServer(t *testing.T) (cfg *sysconfig.Config, addrs map[string]string, doneCh <-chan struct{}, cancel func()) {
	t.Helper()
	_, cfg = minimalConfigFixture(t)
	ctx, cancelFn := context.WithCancel(context.Background())
	addrs = make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}
	return cfg, addrs, done, cancelFn
}

func TestStartServer_BootsAndServesAdminStatus(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})
	adminAddr := addrs["admin"]
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}
	// Drive the admin REST surface's unauthenticated readiness probe to
	// prove boot reached the MarkReady path. Authenticated endpoints are
	// covered by internal/protoadmin's own test suite.
	base := "http://" + adminAddr
	u, err := url.Parse(base + "/api/v1/healthz/ready")
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	resp, err := http.Get(u.String())
	if err != nil {
		t.Fatalf("GET ready: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ready: got %d, want 200", resp.StatusCode)
	}
	smtpAddr := addrs["smtp"]
	if smtpAddr == "" {
		t.Fatalf("smtp listener not bound; addrs=%+v", addrs)
	}
	// Dial SMTP and read the banner to prove the protocol server is live.
	c, err := net.DialTimeout("tcp", smtpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read smtp banner: %v", err)
	}
	if !strings.HasPrefix(string(buf[:n]), "220") {
		t.Fatalf("smtp banner: got %q, want a 220 greeting", string(buf[:n]))
	}
	// Dial IMAP and read the banner.
	imapAddr := addrs["imap"]
	if imapAddr == "" {
		t.Fatalf("imap listener not bound; addrs=%+v", addrs)
	}
	ic, err := net.DialTimeout("tcp", imapAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial imap: %v", err)
	}
	defer ic.Close()
	_ = ic.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err = ic.Read(buf)
	if err != nil {
		t.Fatalf("read imap banner: %v", err)
	}
	if !strings.HasPrefix(string(buf[:n]), "* OK") {
		t.Fatalf("imap banner: got %q, want a '* OK' greeting", string(buf[:n]))
	}
}

func TestBootstrap_CreatesAdmin_Idempotent(t *testing.T) {
	cfgPath, cfg := minimalConfigFixture(t)
	home := t.TempDir()
	SetCredentialsPath(filepath.Join(home, "credentials.toml"))
	t.Cleanup(func() { SetCredentialsPath("") })
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{
		"--system-config", cfgPath,
		"bootstrap",
		"--email", "admin@test.local",
		"--password", "hunter2hunter2hunter2",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("first bootstrap: %v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "admin@test.local") {
		t.Fatalf("bootstrap did not print email: %s", out.String())
	}
	if !strings.Contains(out.String(), "api_key:") {
		t.Fatalf("bootstrap did not print api_key: %s", out.String())
	}
	// Regression guard for Wave 4 finding 1: the printed plaintext API
	// key must authenticate against the admin REST surface. Pre-fix the
	// CLI stored plaintext in APIKey.Hash while protoadmin verified the
	// SHA-256 digest, so every bootstrap key was a 401 and the row
	// doubled as a plaintext credential at rest. Proving the round-trip
	// without booting a full StartServer keeps this test focused on the
	// fix and out of the parallel agent's lifecycle work.
	apiKey := extractPrintedAPIKey(t, out.String())
	if !strings.HasPrefix(apiKey, protoadmin.APIKeyPrefix) {
		t.Fatalf("printed api key has unexpected shape: %q", apiKey)
	}
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := directory.New(st.Meta(), discardLogger(), clk, nil)
	rp := directoryoidc.New(st.Meta(), discardLogger(), &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(st, dir, rp, discardLogger(), clk, protoadmin.Options{})
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api-keys: got %d, want 200 (auth round-trip failed; bootstrap key not accepted): %s", rec.Code, rec.Body.String())
	}
	// A tampered key must be rejected so we are confident the SHA-256
	// hash actually gates the lookup (and not a coincidental match).
	rec = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey+"x")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api-keys (bad): got %d, want 401", rec.Code)
	}
	// Close the store before re-running bootstrap so the second
	// invocation can re-open it.
	_ = st.Close()
	// Second run should exit with ExitBootstrapAlreadyDone.
	root = NewRootCmd()
	out.Reset()
	errOut.Reset()
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{
		"--system-config", cfgPath,
		"bootstrap",
		"--email", "admin@test.local",
		"--password", "hunter2hunter2hunter2",
	})
	err = root.ExecuteContext(ctx)
	if err == nil {
		t.Fatalf("second bootstrap: want error, got nil")
	}
	var ec *exitCoded
	if !errors.As(err, &ec) {
		t.Fatalf("second bootstrap: want *exitCoded, got %T", err)
	}
	if ec.code != ExitBootstrapAlreadyDone {
		t.Fatalf("second bootstrap: code=%d, want %d", ec.code, ExitBootstrapAlreadyDone)
	}
}

// extractPrintedAPIKey scans the bootstrap CLI output for the
// "  api_key: <value>" line and returns <value>.
func extractPrintedAPIKey(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "api_key:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "api_key:"))
		}
	}
	t.Fatalf("api_key not found in bootstrap output: %s", output)
	return ""
}

func TestConfigCheck_Ok(t *testing.T) {
	cfgPath, _ := minimalConfigFixture(t)
	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"server", "config-check", cfgPath})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("config-check: %v", err)
	}
}

func TestCLI_PrincipalList_RequiresAPIKey(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	adminAddr := addrs["admin"]
	if adminAddr == "" {
		t.Fatalf("admin not bound: %+v", addrs)
	}
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{
		"--server-url", "http://" + adminAddr,
		"principal", "list",
	})
	err := root.ExecuteContext(context.Background())
	// Without an API key the CLI's client hits auth middleware and
	// gets 401. The CLI surfaces that as an actionable error mentioning
	// authentication. End-to-end: CLI → admin client → protoadmin router
	// → auth middleware → RFC 7807 response → CLI error wrap.
	if err == nil {
		t.Fatalf("want error from unauthenticated call; got %s", out.String())
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(strings.ToLower(err.Error()), "auth") {
		t.Fatalf("error not actionable: %v", err)
	}
}

func TestConfigCheck_Missing(t *testing.T) {
	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"server", "config-check", "/definitely/does/not/exist/system.toml"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	var ec *exitCoded
	if !errors.As(err, &ec) || ec.code != ExitConfigInvalid {
		t.Fatalf("exit code: got %v, want %d", err, ExitConfigInvalid)
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Fatalf("error message not actionable: %v", err)
	}
}

func TestReloadConfig_LogLevelChange_ApplyLive(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	newCfg := *cfg
	newCfg.Observability.LogLevel = "debug"
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	rt := &Runtime{level: level}
	rt.cfg.Store(cfg)
	if err := ReloadConfig(context.Background(), rt, &newCfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if level.Level() != slog.LevelDebug {
		t.Fatalf("log level not applied: got %v, want debug", level.Level())
	}
	if rt.cfg.Load() != &newCfg {
		t.Fatalf("runtime config not updated")
	}
}

func TestReloadConfig_DataDirChange_Rejected(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	newCfg := *cfg
	newCfg.Server.DataDir = "/srv/new"
	rt := &Runtime{level: new(slog.LevelVar)}
	rt.cfg.Store(cfg)
	err := ReloadConfig(context.Background(), rt, &newCfg)
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if !errors.Is(err, sysconfig.ErrCannotApplyLive) {
		t.Fatalf("want ErrCannotApplyLive, got %v", err)
	}
	if rt.cfg.Load() != cfg {
		t.Fatalf("runtime cfg changed despite reject")
	}
}

// generateSelfSignedCert writes a cert+key pair under dir for dnsNames and
// returns their paths. Mirrors the helper in internal/tls/tls_test.go.
func generateSelfSignedCert(t *testing.T, dir string, dnsNames []string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}
