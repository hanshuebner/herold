package plugin_test

// supervisor_waithealthy_test.go is the internal/plugin-level acceptance
// for re #398: internal/admin.StartServer must not bind its listeners or
// close Ready until every configured spam/mail.classify plugin reports
// StateHealthy, bounded by the supervisor's own handshake+configure
// budget. These tests drive Manager.WaitHealthy directly against a real
// child process (delayspamfixture, testdata) whose OnConfigure sleeps
// for a caller-controlled duration, making the handshake+configure race
// StartServer must close deterministic instead of dependent on host
// load.

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

func buildDelaySpamFixture(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "delayspamfixture")
	cmd := exec.Command("go", "build", "-o", out, "github.com/hanshuebner/herold/internal/plugin/testdata/delayspamfixture")
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, outb)
	}
	return out
}

// TestManager_WaitHealthy_BlocksUntilConfigureCompletes proves
// WaitHealthy is a real completion signal, not an immediate return: it
// blocks for (approximately) delayspamfixture's configured OnConfigure
// delay, and once it returns nil, the plugin's spam.classify call
// answers with a real verdict rather than the -32603 "plugin not
// configured" error a premature classify call gets (the exact symptom
// #398 reports for mail delivered in the window StartServer failed to
// wait out).
func TestManager_WaitHealthy_BlocksUntilConfigureCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds a plugin binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("plugin supervisor uses POSIX signals")
	}
	bin := buildDelaySpamFixture(t)

	mgr := plugin.NewManager(plugin.ManagerOptions{
		Clock:         clock.NewReal(),
		ServerVersion: "test",
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const configureDelay = 400 * time.Millisecond
	p, err := mgr.Start(ctx, plugin.Spec{
		Name:      "delayspamfixture",
		Path:      bin,
		Type:      plugin.TypeSpam,
		Lifecycle: plugin.LifecycleLongRunning,
		Env:       []string{"HEROLD_TEST_CONFIGURE_DELAY_MS=400"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The same bound StartServer applies in production (re #398): the
	// supervisor's own per-attempt handshake+configure RPC budget plus a
	// fixed startup margin, not an arbitrary guess.
	waitCtx, waitCancel := context.WithTimeout(ctx, plugin.HandshakeTimeout+plugin.ConfigureTimeout+5*time.Second)
	defer waitCancel()
	start := time.Now()
	if err := mgr.WaitHealthy(waitCtx, []string{"delayspamfixture"}); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < configureDelay/2 {
		t.Fatalf("WaitHealthy returned after %s, want it to have waited out most of the %s configure delay", elapsed, configureDelay)
	}
	if p.State() != plugin.StateHealthy {
		t.Fatalf("state after WaitHealthy = %s, want healthy", p.State())
	}

	// The acceptance case: a classify call issued the instant WaitHealthy
	// returns gets a real verdict, never "plugin not configured".
	var res sdk.SpamClassifyResult
	callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
	defer callCancel()
	if err := p.Call(callCtx, sdk.MethodSpamClassify, sdk.SpamClassifyParams{}, &res); err != nil {
		t.Fatalf("spam.classify immediately after WaitHealthy: %v", err)
	}
	if res.Verdict != "ham" {
		t.Fatalf("Verdict = %q, want %q", res.Verdict, "ham")
	}
}

// TestManager_WaitHealthy_TimesOutWhileStillConfiguring proves the wait
// is bounded: given a ctx that expires before delayspamfixture's
// OnConfigure returns, WaitHealthy reports a clear, named error instead
// of blocking past the caller's budget.
func TestManager_WaitHealthy_TimesOutWhileStillConfiguring(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds a plugin binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("plugin supervisor uses POSIX signals")
	}
	bin := buildDelaySpamFixture(t)

	mgr := plugin.NewManager(plugin.ManagerOptions{
		Clock:         clock.NewReal(),
		ServerVersion: "test",
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, err := mgr.Start(ctx, plugin.Spec{
		Name:      "delayspamfixture",
		Path:      bin,
		Type:      plugin.TypeSpam,
		Lifecycle: plugin.LifecycleLongRunning,
		Env:       []string{"HEROLD_TEST_CONFIGURE_DELAY_MS=3000"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer waitCancel()
	err = mgr.WaitHealthy(waitCtx, []string{"delayspamfixture"})
	if err == nil {
		t.Fatal("WaitHealthy returned nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "delayspamfixture") || !strings.Contains(err.Error(), "did not become healthy") {
		t.Fatalf("error should name the plugin and say it did not become healthy: %v", err)
	}
	if p.State() == plugin.StateHealthy {
		t.Fatal("plugin reached healthy before its configured delay elapsed")
	}
}

// TestManager_WaitHealthy_ErrorWhenPluginNeverConfigures proves the
// third failure mode the issue names: a plugin whose manifest is
// permanently rejected (never reaches StateHealthy, cycles into
// StateDisabled once its crash budget is exhausted) makes WaitHealthy
// return promptly once the supervise loop gives up, naming the plugin,
// its terminal state, and its LastError -- the boot error StartServer
// surfaces instead of hanging until the caller's own ctx budget expires.
func TestManager_WaitHealthy_ErrorWhenPluginNeverConfigures(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds a plugin binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("plugin supervisor uses POSIX signals")
	}
	bin := buildSpamFixture(t)

	fake := clock.NewFake(time.Unix(0, 0).UTC())
	mgr := plugin.NewManager(plugin.ManagerOptions{
		Logger:        slog.New(slog.NewTextHandler(&lockedBuffer{}, nil)),
		Clock:         fake,
		ServerVersion: "test",
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// spamfixture with no HEROLD_TEST_SPAM_TEMPERATURE declares no
	// temperature at all, which Manifest.Validate always rejects
	// (REQ-FILT-12); MaxCrashes=1 over a generous window disables it
	// after its second failed attempt instead of the production default
	// of five.
	p, err := mgr.Start(ctx, plugin.Spec{
		Name:          "spamfixture",
		Path:          bin,
		Type:          plugin.TypeSpam,
		Lifecycle:     plugin.LifecycleLongRunning,
		MaxCrashes:    1,
		CrashWindow:   time.Hour,
		ShutdownGrace: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitErrCh := make(chan error, 1)
	go func() {
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer waitCancel()
		waitErrCh <- mgr.WaitHealthy(waitCtx, []string{"spamfixture"})
	}()

	// Drive the fake clock past the backoff delay between the first and
	// second failed attempt so the plugin reaches StateDisabled quickly
	// (same pattern as TestSupervisorIntegration_SpamPluginRequiresPinnedTemperature).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && p.State() != plugin.StateDisabled {
		time.Sleep(50 * time.Millisecond)
		fake.Advance(2 * time.Second)
	}
	if p.State() != plugin.StateDisabled {
		t.Fatalf("plugin never disabled (state=%s)", p.State())
	}

	select {
	case err := <-waitErrCh:
		if err == nil {
			t.Fatal("WaitHealthy returned nil for a plugin that never became healthy")
		}
		if !strings.Contains(err.Error(), "spamfixture") || !strings.Contains(err.Error(), "exited before becoming healthy") {
			t.Fatalf("error should name the plugin and say it exited before becoming healthy: %v", err)
		}
		if !strings.Contains(err.Error(), "manifest rejected") {
			t.Fatalf("error should carry the plugin's LastError (manifest rejection detail): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitHealthy did not return after the plugin was disabled")
	}
}
