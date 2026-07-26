package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestMetricsReachable_WhileACMEProvisioningBlocks is the regression guard
// for issue #268: a deploy's health check against the metrics listener
// (cfg.Observability.MetricsBind) must succeed while the synchronous
// initial ACME cert-provisioning call is still in flight, not only after
// it returns. Before the fix, the metrics HTTP server bound after
// EnsureCert returned, so a cold or renewing ACME cache (dns-01
// propagation can take well over a minute) left the port unreachable for
// longer than a deploy's health-check window and triggered a rollback of
// an otherwise-healthy binary.
//
// The ACME directory endpoint here never responds until its request
// context is cancelled, standing in for a stalled dns-01 propagation wait
// or any other slow step of initial cert issuance.
func TestMetricsReachable_WhileACMEProvisioningBlocks(t *testing.T) {
	blockCh := make(chan struct{})
	acmeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blockCh:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(blockCh)
		acmeSrv.Close()
	})

	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	systomlPath := filepath.Join(dir, "system.toml")
	toml := fmt.Sprintf(`
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
protocol = "http"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "http"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = "127.0.0.1:0"

[acme]
email = "ops@example.com"
directory_url = %q
`, dir, filepath.Join(dir, "ports.toml"), certPath, keyPath, filepath.Join(dir, "db.sqlite"),
		certPath, keyPath, certPath, keyPath, acmeSrv.URL)
	if err := os.WriteFile(systomlPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			ListenerAddrs:    addrs,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	// Poll for the metrics listener's resolved address. It must appear
	// well before the hung ACME directory fetch above ever unblocks.
	var metricsAddr string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		addrsMu.Lock()
		metricsAddr = addrs["metrics"]
		addrsMu.Unlock()
		if metricsAddr != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if metricsAddr == "" {
		t.Fatalf("metrics listener did not bind within timeout; ACME provisioning is blocking it (re #268)")
	}

	// Confirm initial ACME provisioning has NOT completed yet: the admin
	// listener (bound only after the synchronous ACME block returns)
	// must still be absent, proving the metrics check above happened
	// while provisioning was genuinely in progress.
	addrsMu.Lock()
	adminAddr := addrs["admin"]
	addrsMu.Unlock()
	if adminAddr != "" {
		t.Fatalf("admin listener already bound; ACME provisioning did not block as expected for this test")
	}

	resp, err := http.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics while ACME provisioning is in progress: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics while ACME provisioning blocks: status=%d, want 200", resp.StatusCode)
	}
}
