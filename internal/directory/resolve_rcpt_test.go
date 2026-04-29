package directory_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// storeAuditFilter is a helper returning an empty AuditLogFilter so the
// test code stays decoupled from the internal/store struct shape.
func storeAuditFilter() store.AuditLogFilter { return store.AuditLogFilter{} }

// fakeResolveRcptInvoker drives the resolver under test. The handler
// is swappable so individual tests script the per-call response or
// inject failures.
type fakeResolveRcptInvoker struct {
	calls atomic.Int64
	fn    func(context.Context, string, directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error)
}

func (f *fakeResolveRcptInvoker) InvokeResolveRcpt(ctx context.Context, plugin string, req directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
	f.calls.Add(1)
	return f.fn(ctx, plugin, req)
}

func newRcptResolver(t *testing.T, inv directory.ResolveRcptInvoker, perSec int) (*directory.RcptResolver, *clock.FakeClock, *fakestore.Store) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	r, err := directory.NewRcptResolver(directory.RcptResolverConfig{
		Invoker:  inv,
		Clock:    clk,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metadata: fs.Meta(),
		Limiter:  directory.NewResolveRcptRateLimiter(clk, perSec),
	})
	if err != nil {
		t.Fatalf("NewRcptResolver: %v", err)
	}
	return r, clk, fs
}

func TestResolveRcpt_Accept_Synthetic(t *testing.T) {
	observe.RegisterDirectoryRcptMetrics()
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "accept", RouteTag: "ticket:42"}, nil
		},
	}
	r, _, _ := newRcptResolver(t, inv, 0)
	dec := r.Resolve(context.Background(), "app-rcpt", directory.ResolveRcptRequest{
		Recipient: "reply+ticket-42@app.example.com",
		Envelope:  directory.ResolveRcptEnvelope{SourceIP: "203.0.113.1"},
	})
	if dec.Action != directory.ResolveRcptAccept {
		t.Fatalf("want accept, got %s", dec.Action)
	}
	if !dec.Synthetic {
		t.Fatalf("want synthetic=true (no principal_id)")
	}
	if dec.RouteTag != "ticket:42" {
		t.Fatalf("route_tag mismatch: %q", dec.RouteTag)
	}
	if got := testutil.ToFloat64(observe.DirectorySyntheticAcceptedTotal.WithLabelValues("app-rcpt")); got < 1 {
		t.Fatalf("synthetic_accepted_total not incremented: %v", got)
	}
}

func TestResolveRcpt_Accept_WithPrincipal(t *testing.T) {
	pid := uint64(1234)
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "accept", PrincipalID: &pid}, nil
		},
	}
	r, _, _ := newRcptResolver(t, inv, 0)
	dec := r.Resolve(context.Background(), "app-rcpt", directory.ResolveRcptRequest{
		Recipient: "x@app.example.com",
		Envelope:  directory.ResolveRcptEnvelope{SourceIP: "203.0.113.1"},
	})
	if dec.Action != directory.ResolveRcptAccept {
		t.Fatalf("want accept, got %s", dec.Action)
	}
	if dec.Synthetic {
		t.Fatalf("want synthetic=false (principal_id supplied)")
	}
	if dec.PrincipalID == nil || *dec.PrincipalID != pid {
		t.Fatalf("principal_id missing/wrong: %+v", dec.PrincipalID)
	}
}

func TestResolveRcpt_RejectAndDefer_DefaultCodes(t *testing.T) {
	cases := []struct {
		name        string
		respAction  string
		respCode    string
		wantAction  directory.ResolveRcptAction
		wantCodePfx string
	}{
		{"reject_default", "reject", "", directory.ResolveRcptReject, "5.1.1"},
		{"reject_override", "reject", "5.7.1", directory.ResolveRcptReject, "5.7.1"},
		{"reject_bad_class", "reject", "4.0.0", directory.ResolveRcptReject, "5.1.1"}, // wrong class -> default
		{"defer_default", "defer", "", directory.ResolveRcptDefer, "4.5.1"},
		{"defer_override", "defer", "4.7.1", directory.ResolveRcptDefer, "4.7.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := &fakeResolveRcptInvoker{
				fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
					return directory.ResolveRcptResponse{Action: c.respAction, Code: c.respCode}, nil
				},
			}
			r, _, _ := newRcptResolver(t, inv, 0)
			dec := r.Resolve(context.Background(), "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
			if dec.Action != c.wantAction {
				t.Fatalf("action: got %s want %s", dec.Action, c.wantAction)
			}
			if dec.Code != c.wantCodePfx {
				t.Fatalf("code: got %s want %s", dec.Code, c.wantCodePfx)
			}
		})
	}
}

func TestResolveRcpt_Fallthrough(t *testing.T) {
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "fallthrough"}, nil
		},
	}
	r, _, _ := newRcptResolver(t, inv, 0)
	dec := r.Resolve(context.Background(), "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptFallthrough {
		t.Fatalf("want fallthrough, got %s", dec.Action)
	}
}

func TestResolveRcpt_TimeoutMapsToDefer443(t *testing.T) {
	observe.RegisterDirectoryRcptMetrics()
	beforeTO := testutil.ToFloat64(observe.DirectoryResolveRcptTimeoutsTotal.WithLabelValues("p"))
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{}, fmt.Errorf("%w: deadline", directory.ErrResolveRcptTimeout)
		},
	}
	r, _, _ := newRcptResolver(t, inv, 0)
	dec := r.Resolve(context.Background(), "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptDefer || dec.Code != "4.4.3" {
		t.Fatalf("want defer 4.4.3, got %s %s", dec.Action, dec.Code)
	}
	if got := testutil.ToFloat64(observe.DirectoryResolveRcptTimeoutsTotal.WithLabelValues("p")); got <= beforeTO {
		t.Fatalf("timeouts_total not incremented (%v -> %v)", beforeTO, got)
	}
}

func TestResolveRcpt_TransportErrorMapsToDefer443(t *testing.T) {
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{}, fmt.Errorf("%w: broken pipe", directory.ErrResolveRcptUnavailable)
		},
	}
	r, _, _ := newRcptResolver(t, inv, 0)
	dec := r.Resolve(context.Background(), "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptDefer || dec.Code != "4.4.3" {
		t.Fatalf("want defer 4.4.3, got %s %s", dec.Action, dec.Code)
	}
}

func TestResolveRcpt_RateLimit(t *testing.T) {
	observe.RegisterDirectoryRcptMetrics()
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "accept"}, nil
		},
	}
	r, _, _ := newRcptResolver(t, inv, 3)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		dec := r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "203.0.113.1"}})
		if dec.Action != directory.ResolveRcptAccept {
			t.Fatalf("call %d should accept, got %s", i, dec.Action)
		}
	}
	dec := r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "203.0.113.1"}})
	if dec.Action != directory.ResolveRcptRateLimited {
		t.Fatalf("4th call should be rate-limited, got %s", dec.Action)
	}
	if dec.Code != "4.7.1" {
		t.Fatalf("rate-limit code: got %s want 4.7.1", dec.Code)
	}
	// A different IP still proceeds.
	other := r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "203.0.113.2"}})
	if other.Action != directory.ResolveRcptAccept {
		t.Fatalf("other IP should not be rate-limited, got %s", other.Action)
	}
}

func TestResolveRcpt_RateLimitWindowSlides(t *testing.T) {
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "accept"}, nil
		},
	}
	r, clk, _ := newRcptResolver(t, inv, 2)
	req := directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}}
	ctx := context.Background()
	r.Resolve(ctx, "p", req)
	r.Resolve(ctx, "p", req)
	if d := r.Resolve(ctx, "p", req); d.Action != directory.ResolveRcptRateLimited {
		t.Fatalf("3rd call should rate-limit, got %s", d.Action)
	}
	clk.Advance(2 * time.Second)
	if d := r.Resolve(ctx, "p", req); d.Action != directory.ResolveRcptAccept {
		t.Fatalf("after window slides, accept again; got %s", d.Action)
	}
}

func TestResolveRcpt_NoPluginIsFallthrough(t *testing.T) {
	r, _, _ := newRcptResolver(t, nil, 0)
	dec := r.Resolve(context.Background(), "", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptFallthrough {
		t.Fatalf("want fallthrough, got %s", dec.Action)
	}
}

func TestResolveRcpt_NilInvokerYieldsDefer(t *testing.T) {
	r, _, _ := newRcptResolver(t, nil, 0)
	dec := r.Resolve(context.Background(), "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptDefer || dec.Code != "4.4.3" {
		t.Fatalf("nil-invoker should defer 4.4.3, got %s %s", dec.Action, dec.Code)
	}
}

// --- breaker -----------------------------------------------------

func TestBreaker_OpensAtThresholdAndShortCircuits(t *testing.T) {
	observe.RegisterDirectoryRcptMetrics()
	var failNext atomic.Bool
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			if failNext.Load() {
				return directory.ResolveRcptResponse{}, fmt.Errorf("%w: x", directory.ErrResolveRcptUnavailable)
			}
			return directory.ResolveRcptResponse{Action: "accept"}, nil
		},
	}
	r, _, _ := newRcptResolver(t, inv, 0)
	// Drive 25 calls all failing — well above the 20 minimum.
	failNext.Store(true)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	}
	beforeCalls := inv.calls.Load()
	// Next call should short-circuit without invoking the plugin.
	dec := r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptDefer || dec.Code != "4.4.3" {
		t.Fatalf("want defer 4.4.3 from open breaker, got %s %s", dec.Action, dec.Code)
	}
	if inv.calls.Load() != beforeCalls {
		t.Fatalf("breaker-open path should NOT call plugin")
	}
}

func TestBreaker_HalfOpenRecovers(t *testing.T) {
	observe.RegisterDirectoryRcptMetrics()
	var failing atomic.Bool
	failing.Store(true)
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			if failing.Load() {
				return directory.ResolveRcptResponse{}, fmt.Errorf("%w: x", directory.ErrResolveRcptUnavailable)
			}
			return directory.ResolveRcptResponse{Action: "accept"}, nil
		},
	}
	r, clk, _ := newRcptResolver(t, inv, 0)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	}
	// Confirm open.
	beforeCalls := inv.calls.Load()
	r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if inv.calls.Load() != beforeCalls {
		t.Fatalf("expected open breaker (no plugin call)")
	}
	// Cooldown elapses; one probe permitted; backend healthy.
	failing.Store(false)
	clk.Advance(61 * time.Second)
	dec := r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec.Action != directory.ResolveRcptAccept {
		t.Fatalf("half-open probe should succeed, got %s", dec.Action)
	}
	// Subsequent calls should also pass.
	dec2 := r.Resolve(ctx, "p", directory.ResolveRcptRequest{Recipient: "x@y", Envelope: directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"}})
	if dec2.Action != directory.ResolveRcptAccept {
		t.Fatalf("post-recovery call should accept, got %s", dec2.Action)
	}
}

func TestResolveRcpt_AuditLogWritten(t *testing.T) {
	inv := &fakeResolveRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "accept", RouteTag: "ticket:1"}, nil
		},
	}
	r, _, fs := newRcptResolver(t, inv, 0)
	r.Resolve(context.Background(), "app-rcpt", directory.ResolveRcptRequest{
		Recipient: "reply+1@app.example.com",
		Envelope:  directory.ResolveRcptEnvelope{SourceIP: "203.0.113.5", MailFrom: "alice@example.net"},
	})
	rows, err := fs.Meta().ListAuditLog(context.Background(), storeAuditFilter())
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	var found bool
	for _, e := range rows {
		if e.Action == "smtp.rcpt.resolve" && e.Subject == "rcpt:reply+1@app.example.com" {
			found = true
			if e.Metadata["plugin"] != "app-rcpt" {
				t.Fatalf("audit metadata plugin: %v", e.Metadata)
			}
			if e.Metadata["route_tag"] != "ticket:1" {
				t.Fatalf("audit metadata route_tag: %v", e.Metadata)
			}
			if e.Metadata["action"] != "accept" {
				t.Fatalf("audit metadata action: %v", e.Metadata)
			}
		}
	}
	if !found {
		t.Fatalf("expected smtp.rcpt.resolve audit row, got %d rows", len(rows))
	}
}

// TestActivityTagged_ResolveRcpt_Failure asserts that the "resolve_rcpt failed"
// warn path carries activity=system (REQ-OPS-86a).
func TestActivityTagged_ResolveRcpt_Failure(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
		if err != nil {
			t.Fatalf("fakestore: %v", err)
		}
		defer fs.Close()
		inv := &fakeResolveRcptInvoker{
			fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
				return directory.ResolveRcptResponse{}, fmt.Errorf("%w: timed out", directory.ErrResolveRcptTimeout)
			},
		}
		r, err := directory.NewRcptResolver(directory.RcptResolverConfig{
			Invoker:  inv,
			Clock:    clk,
			Logger:   log,
			Metadata: fs.Meta(),
		})
		if err != nil {
			t.Fatalf("NewRcptResolver: %v", err)
		}
		r.Resolve(context.Background(), "plugin", directory.ResolveRcptRequest{
			Recipient: "x@y",
			Envelope:  directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"},
		})
	})
}

// Confirm sentinel errors classify cleanly via errors.Is.
func TestResolveRcptSentinelErrorsAreErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("call failed: %w", directory.ErrResolveRcptTimeout)
	if !errors.Is(wrapped, directory.ErrResolveRcptTimeout) {
		t.Fatalf("errors.Is should match ErrResolveRcptTimeout")
	}
	wrapped = fmt.Errorf("call failed: %w", directory.ErrResolveRcptUnavailable)
	if !errors.Is(wrapped, directory.ErrResolveRcptUnavailable) {
		t.Fatalf("errors.Is should match ErrResolveRcptUnavailable")
	}
}
