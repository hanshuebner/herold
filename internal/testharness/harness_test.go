package testharness_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

func TestStart_NoLeakedGoroutines(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	func() {
		// Inner function so t.Cleanup runs when it returns only if we
		// call the redundant cleanup explicitly; we do, below.
		srv, cleanup := testharness.Start(t, testharness.Options{
			Listeners: []testharness.ListenerSpec{
				{Name: "smtp", Protocol: "smtp"},
				{Name: "admin", Protocol: "admin"},
			},
		})
		if _, ok := srv.ListenerAddr("smtp"); !ok {
			t.Fatalf("expected smtp listener to be bound")
		}
		cleanup()
	}()

	// Give goroutines a chance to exit; they are already joined via
	// WaitGroup inside Close but the scheduler may have them in runnable
	// state briefly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if delta := runtime.NumGoroutine() - before; delta <= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if delta := runtime.NumGoroutine() - before; delta > 2 {
		t.Fatalf("goroutine leak: before=%d after=%d delta=%d", before, runtime.NumGoroutine(), delta)
	}
}

func TestAdvance_FiresTimer(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	srv, _ := testharness.Start(t, testharness.Options{Clock: fc})
	ch := srv.Clock.After(500 * time.Millisecond)
	select {
	case <-ch:
		t.Fatalf("After fired before Advance")
	default:
	}
	srv.Advance(500 * time.Millisecond)
	select {
	case <-ch:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatalf("After did not fire after Advance")
	}
}

func TestDialSMTP_NoHandlerYet(t *testing.T) {
	srv, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{
			{Name: "smtp", Protocol: "smtp"},
		},
	})
	_, err := srv.DialSMTP(context.Background())
	if err == nil {
		t.Fatalf("expected ErrListenerHasNoHandler, got nil")
	}
	// The Wave 0 accept loop closes conns on accept; DialSMTP returns the
	// typed error without ever opening the socket because no handler is
	// attached.
	if !errorsIsHandlerMissing(err) {
		t.Fatalf("expected ErrListenerHasNoHandler, got %v", err)
	}
}

func TestDNSAndPluginForwarding(t *testing.T) {
	srv, _ := testharness.Start(t, testharness.Options{})
	srv.AddDNSRecord("example.test", "A", "127.0.0.1")
	ips, err := srv.DNS.LookupA(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("LookupA: %v", err)
	}
	if len(ips) != 1 || ips[0].String() != "127.0.0.1" {
		t.Fatalf("LookupA wrong result: %v", ips)
	}

	p := fakeplugin.New("echo", "echo")
	srv.RegisterPlugin("echo", p)
	if _, ok := srv.Plugins.Get("echo"); !ok {
		t.Fatalf("plugin echo not registered")
	}
}

func errorsIsHandlerMissing(err error) bool {
	if err == nil {
		return false
	}
	// ErrListenerHasNoHandler is wrapped; use simple string match because we
	// don't want a transitive errors package just for this test.
	return contains(err.Error(), "listener has no handler") || contains(err.Error(), "no smtp listener")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
