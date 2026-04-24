package plugin_test

import (
	"context"
	"errors"
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
// through the complete lifecycle declared in docs/requirements/11-plugins.md:
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
