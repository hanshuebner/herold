package admin

// plugin_boot_healthy_test.go is the StartServer-level acceptance for re
// #398: StartServer must wait for every configured spam/mail.classify
// plugin to reach StateHealthy before binding any listener or closing
// Ready, bounded by the plugin supervisor's own handshake+configure
// timeouts, and its boot log must state when that happened relative to
// the listeners. delayspamfixture (internal/plugin/testdata) is a real
// child process whose OnConfigure sleeps for a caller-controlled
// duration, so both the wait and the timeout-with-a-clear-error path are
// deterministic instead of dependent on host load.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// buildDelaySpamFixtureBin compiles delayspamfixture (internal/plugin
// testdata) once for these tests -- a real spam-type child process whose
// OnConfigure delay is controlled by HEROLD_TEST_CONFIGURE_DELAY_MS, so
// the StartServer-vs-plugin-configure race (re #398) is reproduced
// deterministically rather than by racing against host load.
func buildDelaySpamFixtureBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "delayspamfixture")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/internal/plugin/testdata/delayspamfixture")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build delayspamfixture: %v\n%s", err, out)
	}
	return bin
}

// bootHealthyFixture writes a system.toml wiring delayspamfixture as a
// "spam" plugin plus a minimal listener set, and returns the loaded
// config. HEROLD_TEST_CONFIGURE_DELAY_MS must already be set in the
// test's environment (t.Setenv) before StartServer spawns the plugin.
func bootHealthyFixture(t *testing.T, pluginBin string) *sysconfig.Config {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite")
	sqlitetest.PrepareAt(t, dbPath)
	systomlPath := filepath.Join(dir, "system.toml")
	body := fmt.Sprintf(`
[server]
hostname = "mx.test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
shutdown_grace = "5s"
port_report_file = %q

[server.admin_tls]
source = "none"

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "none"

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

[[plugin]]
name = "delayspamfixture"
path = %q
type = "spam"
lifecycle = "long-running"

[observability]
log_format = "text"
log_level = "info"
metrics_bind = ""
`, dir, filepath.Join(dir, "ports.toml"), dbPath, pluginBin)
	if err := os.WriteFile(systomlPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("sysconfig.Load: %v", err)
	}
	return cfg
}

// TestStartServer_WaitsForClassifyPluginHealthyBeforeReady is the
// deterministic counterpart to TestSpamLLM_E2E_ConfiguredThroughSystemTOML
// (re #398): with a spam plugin whose configure round trip takes a known
// 400ms, Ready must not close before that elapsed, and a message
// delivered the instant Ready closes must carry the plugin's real
// verdict rather than a "plugin not configured" classification error.
// The boot log must also state the plugins-healthy line before any
// listener-bound line, per the issue's acceptance.
func TestStartServer_WaitsForClassifyPluginHealthyBeforeReady(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	bin := buildDelaySpamFixtureBin(t)
	const configureDelay = 400 * time.Millisecond
	t.Setenv("HEROLD_TEST_CONFIGURE_DELAY_MS", "400")

	cfg := bootHealthyFixture(t, bin)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var logBuf lockedLogBuffer
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})),
			Ready:            ready,
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
			t.Errorf("server did not shut down within grace window")
		}
	})
	waitForReady(t, ready, done)
	elapsed := time.Since(start)
	if elapsed < configureDelay/2 {
		t.Fatalf("Ready closed after %s, want it to have waited out most of the %s plugin configure delay", elapsed, configureDelay)
	}

	addrsMu.Lock()
	smtpAddr := addrs["smtp"]
	addrsMu.Unlock()
	if smtpAddr == "" {
		t.Fatalf("smtp listener not bound; addrs=%+v", addrs)
	}

	const domain = "test.local"
	clk := clock.NewReal()
	st, err := storeOpenForBootHealthyTest(t, ctx, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: domain, IsLocal: true, CreatedAt: clk.Now()}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dirAdapter := directory.New(st.Meta(), discardLogger(), clk, nil)
	pid, err := dirAdapter.CreatePrincipal(ctx, "alice@"+domain, "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	// Deliver the instant Ready closes -- the acceptance case, not a race
	// to avoid (re #398).
	deliverSpamLLME2EMessage(t, smtpAddr, domain)

	verifySt, err := storeOpenForBootHealthyTest(t, ctx, cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = verifySt.Close() }()
	var rec store.LLMClassificationRecord
	var found bool
	var lastErr string
	for !found {
		select {
		case <-done:
			t.Fatalf("server exited before the spam classification record was persisted; last: %s", lastErr)
		default:
		}
		mb, err := verifySt.Meta().GetMailboxByName(ctx, pid, "INBOX")
		if err != nil {
			lastErr = fmt.Sprintf("GetMailboxByName: %v", err)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		msgs, err := verifySt.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
		if err != nil || len(msgs) == 0 {
			lastErr = fmt.Sprintf("ListMessages: err=%v len=%d", err, len(msgs))
			time.Sleep(20 * time.Millisecond)
			continue
		}
		rec, err = verifySt.Meta().GetLLMClassification(ctx, msgs[0].ID)
		if err != nil || rec.SpamVerdict == nil {
			lastErr = fmt.Sprintf("GetLLMClassification: err=%v rec=%+v", err, rec)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		found = true
	}
	if *rec.SpamVerdict != "ham" {
		reason := ""
		if rec.SpamReason != nil {
			reason = *rec.SpamReason
		}
		t.Fatalf("SpamVerdict = %q, reason=%q, want %q (a %q verdict means the message reached the classify call before the plugin's configure landed)",
			*rec.SpamVerdict, reason, "ham", "unclassified")
	}

	logs := logBuf.String()
	healthyIdx := strings.Index(logs, "spam/classify plugins healthy")
	if healthyIdx < 0 {
		t.Fatalf("boot log missing the plugins-healthy line:\n%s", logs)
	}
	listenerIdx := strings.Index(logs, "listener bound")
	if listenerIdx < 0 {
		t.Fatalf("boot log missing a listener-bound line:\n%s", logs)
	}
	if healthyIdx > listenerIdx {
		t.Fatalf("plugins-healthy line must precede the first listener-bound line in the boot log:\n%s", logs)
	}
}

// TestStartServer_BootFailsWithClearErrorWhenClassifyPluginNeverHealthy
// proves the third branch the issue names: when a configured spam/
// classify plugin does not reach healthy within StartServer's bound, boot
// fails with a clear, named error instead of binding listeners and
// declaring itself ready anyway. The caller's own ctx timeout (2s) is
// shorter than the plugin's configure delay (30s) so the test observes
// StartServer's error path without waiting out the full production wait
// bound.
func TestStartServer_BootFailsWithClearErrorWhenClassifyPluginNeverHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	bin := buildDelaySpamFixtureBin(t)
	t.Setenv("HEROLD_TEST_CONFIGURE_DELAY_MS", "30000")

	cfg := bootHealthyFixture(t, bin)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := StartServer(ctx, cfg, StartOpts{
		Logger:           slog.New(slog.NewTextHandler(&lockedLogBuffer{}, &slog.HandlerOptions{Level: slog.LevelError})),
		ExternalShutdown: true,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("StartServer returned nil, want a boot error naming the plugin")
	}
	if !strings.Contains(err.Error(), "delayspamfixture") || !strings.Contains(err.Error(), "did not become healthy") {
		t.Fatalf("boot error should name the plugin and say it did not become healthy: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("StartServer took %s to fail, want it bounded well under the 30s configure delay", elapsed)
	}
}

// lockedLogBuffer is a bytes.Buffer safe for concurrent use: StartServer
// logs from several goroutines while the test reads the captured output.
type lockedLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLogBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLogBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func storeOpenForBootHealthyTest(t *testing.T, ctx context.Context, cfg *sysconfig.Config) (store.Store, error) {
	t.Helper()
	return openStore(ctx, cfg, discardLogger(), clock.NewReal())
}
