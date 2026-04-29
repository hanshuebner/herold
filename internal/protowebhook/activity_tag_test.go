package protowebhook_test

// activity_tag_test.go verifies REQ-OPS-86 / REQ-OPS-86a for protowebhook:
// every log record the dispatcher emits carries a valid "activity" attribute
// from the closed enum {user, audit, system, poll, access, internal}.
//
// Focused tests assert specific activity values for high-value records:
//   - successful webhook delivery   → system (info)
//   - delivery permanently dropped  → system (warn / error)
//   - internal infrastructure error → internal (warn)

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// webhookCaptureHandler is a test-only slog.Handler that fires
// onRecord for every emitted record.
type webhookCaptureHandler struct {
	mu       sync.Mutex
	records  []webhookRecord
	pre      map[string]string
	onChange func()
}

type webhookRecord struct {
	activity string
	level    slog.Level
	msg      string
}

func (h *webhookCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *webhookCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	activity := h.pre["activity"]
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "activity" {
			activity = a.Value.String()
			return false
		}
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, webhookRecord{activity: activity, level: r.Level, msg: r.Message})
	fn := h.onChange
	h.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (h *webhookCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(h.pre)+len(attrs))
	for k, v := range h.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &webhookCaptureHandler{
		records:  h.records,
		pre:      merged,
		onChange: h.onChange,
	}
}

func (h *webhookCaptureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *webhookCaptureHandler) hasActivityLevel(activity string, minLevel slog.Level) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.activity == activity && r.level >= minLevel {
			return true
		}
	}
	return false
}

// newWebhookTagHarness constructs a minimal dispatcher test fixture with
// a recording logger.
func newWebhookTagHarness(t *testing.T, lg *slog.Logger, respond func(w http.ResponseWriter, r *http.Request)) (*protowebhook.Dispatcher, *fakestore.Store, *clock.FakeClock, *httptest.Server, chan struct{}) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}

	notify := make(chan struct{}, 64)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respond != nil {
			respond(w, r)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		select {
		case notify <- struct{}{}:
		default:
		}
	}))

	d := protowebhook.New(protowebhook.Options{
		Store:                   fs,
		Logger:                  lg,
		Clock:                   clk,
		HTTPClient:              recv.Client(),
		MaxConcurrentDeliveries: 4,
		PollInterval:            5 * time.Millisecond,
		BatchSize:               16,
		InlineBodyMaxSize:       1 << 20,
		CursorKey:               "webhook-tag-test-" + t.Name(),
		RetrySchedule:           []time.Duration{20 * time.Millisecond, 40 * time.Millisecond},
	})

	t.Cleanup(func() {
		recv.Close()
		_ = fs.Close()
	})
	return d, fs, clk, recv, notify
}

// seedAndDeliverMessage creates the minimal store objects and inserts a
// message that will trigger the webhook dispatcher.
func seedAndDeliverMessage(t *testing.T, ctx context.Context, fs *fakestore.Store, recvURL string) {
	t.Helper()
	p, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "user@tag.example",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := fs.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	_, err = fs.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind:    store.WebhookOwnerDomain,
		OwnerID:      "tag.example",
		TargetURL:    recvURL,
		DeliveryMode: store.DeliveryModeInline,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("InsertWebhook: %v", err)
	}
	raw := "From: alice@external.example\r\nTo: user@tag.example\r\nSubject: tag test\r\n\r\nhello\r\n"
	ref, err := fs.Blobs().Put(ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	_, _, err = fs.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Size:        ref.Size,
		Blob:        ref,
		Envelope: store.Envelope{
			Subject: "tag test",
			From:    "alice@external.example",
			To:      "user@tag.example",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
}

// waitNotify blocks until a delivery notification arrives (or times out),
// advancing the fake clock each iteration so the poll loop fires.
func waitNotify(t *testing.T, clk *clock.FakeClock, notify chan struct{}, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		clk.Advance(10 * time.Millisecond)
		select {
		case <-notify:
			return true
		case <-time.After(10 * time.Millisecond):
		}
	}
	return false
}

// TestWebhookActivityTag_Dispatch_IsSystem asserts that a successful
// webhook dispatch emits at least one activity=system record at info.
// REQ-OPS-86.
func TestWebhookActivityTag_Dispatch_IsSystem(t *testing.T) {
	t.Parallel()
	cap := &webhookCaptureHandler{}
	lg := slog.New(cap)
	d, fs, clk, recv, notify := newWebhookTagHarness(t, lg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("dispatcher did not return in time")
		}
	})

	seedAndDeliverMessage(t, ctx, fs, recv.URL)

	if !waitNotify(t, clk, notify, 5*time.Second) {
		t.Fatal("webhook POST not received within timeout")
	}
	// Give the success log a moment to be written.
	time.Sleep(20 * time.Millisecond)

	if !cap.hasActivityLevel(observe.ActivitySystem, slog.LevelInfo) {
		t.Error("expected at least one activity=system info record on successful dispatch (REQ-OPS-86)")
	}
}

// TestWebhookActivityTag_RetryExhausted_IsSystem asserts that a
// permanently-dropped delivery emits activity=system at error level.
// REQ-OPS-86.
func TestWebhookActivityTag_RetryExhausted_IsSystem(t *testing.T) {
	t.Parallel()
	cap := &webhookCaptureHandler{}
	lg := slog.New(cap)
	// Always respond 500 so retries exhaust.
	d, fs, clk, recv, _ := newWebhookTagHarness(t, lg, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("dispatcher did not return in time")
		}
	})

	seedAndDeliverMessage(t, ctx, fs, recv.URL)

	// Drive clock past the short retry schedule (2 retries: 20ms + 40ms)
	// plus extra to let all goroutines complete.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(100 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
		if cap.hasActivityLevel(observe.ActivitySystem, slog.LevelError) {
			break
		}
	}
	if !cap.hasActivityLevel(observe.ActivitySystem, slog.LevelError) {
		t.Error("expected activity=system error record after retries exhausted (REQ-OPS-86)")
	}
}

// TestWebhookActivityTag_AllRecordsTagged asserts no record from a
// full dispatch cycle is missing the activity attribute. REQ-OPS-86a.
func TestWebhookActivityTag_AllRecordsTagged(t *testing.T) {
	t.Parallel()
	observe.AssertActivityTagged(t, func(lg *slog.Logger) {
		d, fs, clk, recv, notify := newWebhookTagHarness(t, lg, nil)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Log("dispatcher did not return in time")
			}
		})

		seedAndDeliverMessage(t, ctx, fs, recv.URL)
		waitNotify(t, clk, notify, 5*time.Second)
		// Let the post-delivery log write flush.
		time.Sleep(20 * time.Millisecond)
		cancel()
	})
}
