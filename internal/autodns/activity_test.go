package autodns_test

// activity_test.go: focused unit tests asserting REQ-OPS-86 activity
// tagging on high-value log records emitted by the autodns package.

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/autodns"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// autodnsCaptureHandler captures every emitted log record for assertion.
type autodnsCaptureHandler struct {
	mu      sync.Mutex
	records []autodnsCapturedRec
}

type autodnsCapturedRec struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

func (h *autodnsCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *autodnsCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	cr := autodnsCapturedRec{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		cr.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, cr)
	h.mu.Unlock()
	return nil
}

func (h *autodnsCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	pre := make(map[string]string, len(attrs))
	for _, a := range attrs {
		pre[a.Key] = a.Value.String()
	}
	return &autodnsCaptureChild{parent: h, pre: pre}
}

func (h *autodnsCaptureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *autodnsCaptureHandler) snapshot() []autodnsCapturedRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]autodnsCapturedRec, len(h.records))
	copy(out, h.records)
	return out
}

type autodnsCaptureChild struct {
	parent *autodnsCaptureHandler
	pre    map[string]string
}

func (c *autodnsCaptureChild) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *autodnsCaptureChild) Handle(_ context.Context, r slog.Record) error {
	cr := autodnsCapturedRec{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string, len(c.pre)),
	}
	for k, v := range c.pre {
		cr.attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		cr.attrs[a.Key] = a.Value.String()
		return true
	})
	c.parent.mu.Lock()
	c.parent.records = append(c.parent.records, cr)
	c.parent.mu.Unlock()
	return nil
}

func (c *autodnsCaptureChild) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(c.pre)+len(attrs))
	for k, v := range c.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &autodnsCaptureChild{parent: c.parent, pre: merged}
}

func (c *autodnsCaptureChild) WithGroup(_ string) slog.Handler { return c }

func findAutodnsRecord(recs []autodnsCapturedRec, pred func(autodnsCapturedRec) bool) (autodnsCapturedRec, bool) {
	for _, r := range recs {
		if pred(r) {
			return r, true
		}
	}
	return autodnsCapturedRec{}, false
}

// TestActivityTagged_AutoDNSPublishDomain verifies that the "autodns:
// domain published" record carries activity=system at info (REQ-OPS-86).
func TestActivityTagged_AutoDNSPublishDomain(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		pub, _, _, _ := newPublisherWithLogger(t, "fake-dns", log)
		_ = pub.PublishDomain(t.Context(), "example.test", samplePolicy())
	})

	cap := &autodnsCaptureHandler{}
	log := slog.New(cap)
	pub, _, _, _ := newPublisherWithLogger(t, "fake-dns", log)
	if err := pub.PublishDomain(t.Context(), "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	r, ok := findAutodnsRecord(cap.snapshot(), func(r autodnsCapturedRec) bool {
		return r.message == "autodns: domain published"
	})
	if !ok {
		t.Fatal("autodns domain published log not found")
	}
	if got := r.attrs["activity"]; got != observe.ActivitySystem {
		t.Errorf("domain published activity: got %q want %q", got, observe.ActivitySystem)
	}
	if r.level != slog.LevelInfo {
		t.Errorf("domain published level: got %v want Info", r.level)
	}
}

// TestActivityTagged_AutoDNSDriftDetected verifies that drift detection
// emits activity=system at warn (REQ-OPS-86).
func TestActivityTagged_AutoDNSDriftDetected(t *testing.T) {
	cap := &autodnsCaptureHandler{}
	log := slog.New(cap)
	pub, rec, _, _ := newPublisherWithLogger(t, "fake-dns", log)
	ctx := t.Context()
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	// Inject drift so the DKIM record returns a different value.
	rec.mu.Lock()
	rec.listFn = func(zone, name string) []presentCall {
		var out []presentCall
		for _, c := range rec.calls {
			if c.Zone == zone && c.Name == name {
				if name == "herold202601._domainkey.example.test" {
					tmp := c
					tmp.Value = "v=DKIM1; k=rsa; p=DRIFTED"
					out = append(out, tmp)
				} else {
					out = append(out, c)
				}
			}
		}
		return out
	}
	rec.mu.Unlock()
	report, err := pub.VerifyDomain(ctx, "example.test")
	if err != nil {
		t.Fatalf("VerifyDomain: %v", err)
	}
	if report.OK {
		t.Fatal("expected OK=false (drift injected)")
	}
	r, ok := findAutodnsRecord(cap.snapshot(), func(r autodnsCapturedRec) bool {
		return r.message == "autodns: reconciliation pass drift detected"
	})
	if !ok {
		t.Fatal("drift detected log record not found")
	}
	if got := r.attrs["activity"]; got != observe.ActivitySystem {
		t.Errorf("drift detected activity: got %q want %q", got, observe.ActivitySystem)
	}
	if r.level != slog.LevelWarn {
		t.Errorf("drift detected level: got %v want Warn", r.level)
	}
}

// TestActivityTagged_AutoDNSNoDrift verifies that a no-drift
// reconciliation pass emits activity=poll at debug (REQ-OPS-86).
func TestActivityTagged_AutoDNSNoDrift(t *testing.T) {
	cap := &autodnsCaptureHandler{}
	log := slog.New(cap)
	pub, _, _, _ := newPublisherWithLogger(t, "fake-dns", log)
	ctx := t.Context()
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	report, err := pub.VerifyDomain(ctx, "example.test")
	if err != nil {
		t.Fatalf("VerifyDomain: %v", err)
	}
	if !report.OK {
		t.Fatalf("expected OK=true (no drift)")
	}
	r, ok := findAutodnsRecord(cap.snapshot(), func(r autodnsCapturedRec) bool {
		return r.message == "autodns: reconciliation pass no drift"
	})
	if !ok {
		t.Fatal("no-drift poll record not found")
	}
	if got := r.attrs["activity"]; got != observe.ActivityPoll {
		t.Errorf("no-drift activity: got %q want %q", got, observe.ActivityPoll)
	}
	if r.level != slog.LevelDebug {
		t.Errorf("no-drift level: got %v want Debug", r.level)
	}
}

// newPublisherWithLogger is like newPublisher but accepts an explicit logger.
func newPublisherWithLogger(t *testing.T, pluginName string, log *slog.Logger) (*autodns.Publisher, *dnsRecorder, *fakeplugin.Registry, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	rec, plug := newDNSRecorder(pluginName)
	reg := fakeplugin.NewRegistry()
	reg.Register(plug)
	pub := autodns.New(autodns.Options{
		Store:             fs,
		Plugins:           fakeInvoker{reg: reg},
		Logger:            log,
		Clock:             clk,
		DefaultPluginName: pluginName,
		Hostname:          "mx.example.test",
	})
	return pub, rec, reg, clk
}
