package plugin_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/plugin"
)

// TestSupervisorIntegration_EchoPlugin drives the real herold-echo binary
// through the complete lifecycle declared in docs/design/server/requirements/11-plugins.md:
// initialize, configure, health, custom RPC, crash + restart, timeout, and
// graceful shutdown.
//
// Goroutine timing in child-process boot is inherently real-time: the test
// uses a FakeClock only for the supervisor's restart-backoff scheduling and
// falls back to short real-clock deadlines where the OS owns the wait.
func TestSupervisorIntegration_EchoPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds a plugin binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("plugin supervisor uses POSIX signals")
	}

	bin := buildEcho(t)

	fake := clock.NewFake(time.Unix(0, 0).UTC())
	mgr := plugin.NewManager(plugin.ManagerOptions{
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

	p, err := mgr.Start(ctx, plugin.Spec{
		Name:      "echo",
		Path:      bin,
		Type:      plugin.TypeEcho,
		Lifecycle: plugin.LifecycleLongRunning,
		Options:   map[string]any{"greeting": "hi"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForState(t, p, plugin.StateHealthy, 5*time.Second)

	// Manifest was populated by handshake.
	mf := p.Manifest()
	if mf == nil || mf.Name != "herold-echo" {
		t.Fatalf("manifest not populated: %+v", mf)
	}

	// Custom echo.Ping round-trip.
	callPing(t, ctx, p, "hello")

	// Kill the child; supervisor restarts.
	pid := p.PID()
	if pid == 0 {
		t.Fatal("PID=0 before crash")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	// Push the fake clock forward to unblock the backoff sleep.
	// The supervisor waits on Clock.After(delay); advancing beyond that
	// fires every pending waiter deterministically.
	time.Sleep(100 * time.Millisecond) // let the supervisor observe exit
	for i := 0; i < 5; i++ {
		fake.Advance(2 * time.Second)
		if p.State() == plugin.StateHealthy && p.PID() != 0 && p.PID() != pid {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	waitForState(t, p, plugin.StateHealthy, 5*time.Second)
	if p.PID() == pid {
		t.Fatal("plugin did not restart after SIGKILL")
	}

	// Ping again after restart.
	callPing(t, ctx, p, "hello-after-restart")

	// Force a timeout with the slow custom method.
	shortCtx, shortCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shortCancel()
	var res map[string]any
	err = p.Call(shortCtx, "echo.Sleep", map[string]any{"ms": 2000}, &res)
	if err == nil {
		t.Fatal("expected timeout on slow call, got nil")
	}
	var rpcErr *plugin.Error
	if errors.As(err, &rpcErr) {
		if rpcErr.Code != plugin.ErrCodeTimeout {
			t.Fatalf("want ErrCodeTimeout, got code=%d msg=%s", rpcErr.Code, rpcErr.Message)
		}
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded or timeout rpc, got %v", err)
	}

	// Plugin should survive the timeout.
	if p.State() == plugin.StateExited || p.State() == plugin.StateDisabled {
		t.Fatalf("plugin died after timeout: state=%s", p.State())
	}
	callPing(t, ctx, p, "hello-after-timeout")

	// Graceful shutdown within grace window.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.State() != plugin.StateExited && p.State() != plugin.StateDisabled {
		t.Fatalf("state after Stop = %s", p.State())
	}
}

func buildEcho(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "herold-echo")
	cmd := exec.Command("go", "build", "-o", out, "github.com/hanshuebner/herold/plugins/herold-echo")
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, outb)
	}
	return out
}

// TestSupervisorIntegration_SpamPluginRequiresPinnedTemperature drives the
// REQ-FILT-12 contract through a real child process: the supervisor
// refuses to load a spam-type plugin whose manifest does not pin
// temperature to 0, and loads one that does. spamfixture (testdata) is a
// throwaway plugin whose declared temperature is controlled by an env
// var, so both outcomes are exercised against the real handshake path
// rather than a Manifest.Validate unit test alone.
//
// Manager.Start's returned error is non-nil only for a malformed Spec
// (see its doc comment); a bad manifest is instead surfaced as a
// logged "plugin manifest invalid" diagnostic and the plugin cycling
// through its crash-restart loop into StateDisabled once its (tightly
// bounded, for this test) crash budget is exhausted — the same failure
// mode as any other handshake rejection (REQ-PLUG-05).
func TestSupervisorIntegration_SpamPluginRequiresPinnedTemperature(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds a plugin binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("plugin supervisor uses POSIX signals")
	}

	bin := buildSpamFixture(t)

	t.Run("undeclared temperature is refused", func(t *testing.T) {
		testSpamFixtureRefused(t, bin, nil)
	})
	t.Run("non-zero temperature is refused", func(t *testing.T) {
		testSpamFixtureRefused(t, bin, []string{"HEROLD_TEST_SPAM_TEMPERATURE=0.7"})
	})
	t.Run("pinned zero temperature is accepted", func(t *testing.T) {
		var logBuf bytes.Buffer
		fake := clock.NewFake(time.Unix(0, 0).UTC())
		mgr := plugin.NewManager(plugin.ManagerOptions{
			Logger:        slog.New(slog.NewTextHandler(&logBuf, nil)),
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

		p, err := mgr.Start(ctx, plugin.Spec{
			Name:      "spamfixture",
			Path:      bin,
			Type:      plugin.TypeSpam,
			Lifecycle: plugin.LifecycleLongRunning,
			Env:       []string{"HEROLD_TEST_SPAM_TEMPERATURE=0"},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		waitForState(t, p, plugin.StateHealthy, 5*time.Second)
		mf := p.Manifest()
		if mf == nil || mf.Temperature == nil || *mf.Temperature != 0 {
			t.Fatalf("manifest temperature not pinned to 0: %+v", mf)
		}
	})
}

// testSpamFixtureRefused starts spamfixture with env (which declares a
// manifest that does not pin temperature to 0) and asserts the
// supervisor logs the refusal by name and disables the plugin rather
// than ever reaching StateHealthy.
func testSpamFixtureRefused(t *testing.T, bin string, env []string) {
	t.Helper()
	var logBuf bytes.Buffer
	fake := clock.NewFake(time.Unix(0, 0).UTC())
	mgr := plugin.NewManager(plugin.ManagerOptions{
		Logger:        slog.New(slog.NewTextHandler(&logBuf, nil)),
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

	p, err := mgr.Start(ctx, plugin.Spec{
		Name:      "spamfixture",
		Path:      bin,
		Type:      plugin.TypeSpam,
		Lifecycle: plugin.LifecycleLongRunning,
		Env:       env,
		// Small, generous-window crash budget so the test reaches
		// StateDisabled in two quick fake-clock-driven restarts
		// instead of the production default of five.
		MaxCrashes:  1,
		CrashWindow: time.Hour,
		// A rejected handshake leaves p.manifest unset, so teardown's
		// grace period falls back to its 10s default; override it so
		// the child (still blocked reading stdin, since a manifest
		// rejection happens before the plugin could ever be told to
		// shut down) is reaped quickly instead of stalling each cycle
		// for 10 real seconds.
		ShutdownGrace: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && p.State() != plugin.StateDisabled {
		time.Sleep(100 * time.Millisecond)
		fake.Advance(2 * time.Second)
	}
	if p.State() != plugin.StateDisabled {
		t.Fatalf("plugin never disabled after repeated manifest rejection (state=%s)", p.State())
	}
	if p.State() == plugin.StateHealthy {
		t.Fatal("plugin reached StateHealthy despite an unpinned temperature")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "plugin manifest invalid") {
		t.Fatalf("expected a manifest-invalid log line; got:\n%s", logs)
	}
	if !strings.Contains(logs, "temperature") || !strings.Contains(logs, "spamfixture") {
		t.Fatalf("log should name the plugin and mention temperature:\n%s", logs)
	}
}

func buildSpamFixture(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "spamfixture")
	cmd := exec.Command("go", "build", "-o", out, "github.com/hanshuebner/herold/internal/plugin/testdata/spamfixture")
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, outb)
	}
	return out
}

func waitForState(t *testing.T, p *plugin.Plugin, want plugin.State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.State() == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("plugin never reached state %s (current=%s)", want, p.State())
}

func callPing(t *testing.T, ctx context.Context, p *plugin.Plugin, msg string) {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var res map[string]any
	if err := p.Call(cctx, "echo.Ping", map[string]any{"msg": msg}, &res); err != nil {
		t.Fatalf("echo.Ping(%q): %v", msg, err)
	}
	got, _ := res["msg"].(string)
	if !strings.EqualFold(got, msg) {
		t.Fatalf("echo.Ping returned %q, want %q", got, msg)
	}
}
