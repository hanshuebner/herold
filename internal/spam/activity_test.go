package spam

// activity_test.go covers REQ-OPS-86a for the spam classifier: every log
// record emitted from Classify must carry a valid "activity" attribute, and
// the specific events (request, verdict, failure) must land on the correct
// activity + level combinations as specified by the task's activity guide.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/observe"
)

// --- shared recording handler -----------------------------------------------

// sharedHandler records all log records (including from WithAttrs children)
// into a single shared slice, so tests can assert over all records regardless
// of how deeply the logger was scoped.
type sharedHandler struct {
	mu      *sync.Mutex
	records *[]spamEvent
	pre     map[string]string
}

type spamEvent struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

func newSpamHandler() *sharedHandler {
	mu := &sync.Mutex{}
	recs := &[]spamEvent{}
	return &sharedHandler{mu: mu, records: recs, pre: map[string]string{}}
}

func (h *sharedHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *sharedHandler) Handle(_ context.Context, r slog.Record) error {
	ev := spamEvent{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string, len(h.pre)),
	}
	for k, v := range h.pre {
		ev.attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		ev.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	*h.records = append(*h.records, ev)
	h.mu.Unlock()
	return nil
}

func (h *sharedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(h.pre)+len(attrs))
	for k, v := range h.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &sharedHandler{mu: h.mu, records: h.records, pre: merged}
}

func (h *sharedHandler) WithGroup(_ string) slog.Handler { return h }

func (h *sharedHandler) snapshot() []spamEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]spamEvent, len(*h.records))
	copy(out, *h.records)
	return out
}

func assertSpamAllTagged(t *testing.T, evs []spamEvent) {
	t.Helper()
	for _, e := range evs {
		act, ok := e.attrs["activity"]
		if !ok {
			t.Errorf("spam record %q missing activity attribute (REQ-OPS-86a)", e.message)
			continue
		}
		if !observe.IsValidActivity(act) {
			t.Errorf("spam record %q has invalid activity %q (REQ-OPS-86a)", e.message, act)
		}
	}
}

// --- AssertActivityTagged integration ----------------------------------------

// TestClassify_AssertActivityTagged uses the observe.AssertActivityTagged
// helper to confirm that a successful classification produces only
// properly-tagged records (REQ-OPS-86a).
func TestClassify_AssertActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		invoker := newFakeInvoker()
		invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
			return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
		})
		c := New(invoker, log, clock.NewFake(time.Now()))
		_, _ = c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "p")
	})
}

// TestClassify_Error_AssertActivityTagged confirms error-path records are
// also tagged when the plugin fails.
func TestClassify_Error_AssertActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		invoker := newFakeInvoker()
		invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
			return nil, errors.New("plugin crashed")
		})
		c := New(invoker, log, clock.NewFake(time.Now()))
		_, _ = c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "p")
	})
}

// TestClassify_Timeout_AssertActivityTagged confirms timeout-path records are
// also tagged.
func TestClassify_Timeout_AssertActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		invoker := newFakeInvoker()
		invoker.handle("slow", ClassifyMethod, func(ctx context.Context, _ any) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		c := New(invoker, log, clock.NewFake(time.Now()))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, _ = c.Classify(ctx, buildMessage(t, canonMsg), nil, "slow")
	})
}

// --- Specific activity + level assertions ------------------------------------

// TestClassify_Success_SystemDebug verifies that the classification request
// and verdict log lines are activity=system at debug level.
func TestClassify_Success_SystemDebug(t *testing.T) {
	h := newSpamHandler()
	log := slog.New(h)

	invoker := newFakeInvoker()
	invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.95}`), nil
	})
	c := New(invoker, log, clock.NewFake(time.Now()))
	r, err := c.Classify(
		context.Background(),
		buildMessage(t, canonMsg),
		newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, "example.com"),
		"p",
	)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict != Spam {
		t.Fatalf("expected spam verdict, got %v", r.Verdict)
	}

	evs := h.snapshot()
	assertSpamAllTagged(t, evs)

	// Both request and verdict records must be system/debug.
	for _, e := range evs {
		if e.attrs["activity"] != observe.ActivitySystem {
			t.Errorf("success record %q: want activity=system, got %q", e.message, e.attrs["activity"])
		}
		if e.level != slog.LevelDebug {
			t.Errorf("success record %q: want level=debug, got %v", e.message, e.level)
		}
	}

	// Subsystem and classifier attrs must appear (pre-scoped via With).
	for _, e := range evs {
		if e.attrs["subsystem"] != "spam" {
			t.Errorf("success record %q: want subsystem=spam, got %q", e.message, e.attrs["subsystem"])
		}
		if e.attrs["classifier"] != "p" {
			t.Errorf("success record %q: want classifier=p, got %q", e.message, e.attrs["classifier"])
		}
	}
}

// TestClassify_Failure_SystemWarn verifies that plugin errors produce
// activity=system at warn level.
func TestClassify_Failure_SystemWarn(t *testing.T) {
	h := newSpamHandler()
	log := slog.New(h)

	invoker := newFakeInvoker()
	invoker.handle("broken", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return nil, errors.New("plugin crashed")
	})
	c := New(invoker, log, clock.NewFake(time.Now()))
	_, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "broken")
	if err == nil {
		t.Fatal("expected error from broken plugin")
	}

	evs := h.snapshot()
	assertSpamAllTagged(t, evs)

	// Find the warn record; there may also be a debug request record.
	var warnEvs []spamEvent
	for _, e := range evs {
		if e.level == slog.LevelWarn {
			warnEvs = append(warnEvs, e)
		}
	}
	if len(warnEvs) == 0 {
		t.Fatal("expected at least one warn record from plugin failure")
	}
	for _, e := range warnEvs {
		if e.attrs["activity"] != observe.ActivitySystem {
			t.Errorf("failure warn record %q: want activity=system, got %q", e.message, e.attrs["activity"])
		}
	}
}

// TestClassify_Timeout_SystemWarn verifies that timeouts produce
// activity=system at warn level.
func TestClassify_Timeout_SystemWarn(t *testing.T) {
	h := newSpamHandler()
	log := slog.New(h)

	invoker := newFakeInvoker()
	invoker.handle("slow", ClassifyMethod, func(ctx context.Context, _ any) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	c := New(invoker, log, clock.NewFake(time.Now()))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Classify(ctx, buildMessage(t, canonMsg), nil, "slow")
	if err == nil {
		t.Fatal("expected timeout error")
	}

	evs := h.snapshot()
	assertSpamAllTagged(t, evs)

	var warnEvs []spamEvent
	for _, e := range evs {
		if e.level == slog.LevelWarn {
			warnEvs = append(warnEvs, e)
		}
	}
	if len(warnEvs) == 0 {
		t.Fatal("expected at least one warn record from timeout")
	}
	for _, e := range warnEvs {
		if e.attrs["activity"] != observe.ActivitySystem {
			t.Errorf("timeout warn record %q: want activity=system, got %q", e.message, e.attrs["activity"])
		}
	}
}
