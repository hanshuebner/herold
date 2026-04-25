package protoevents_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoevents"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fakeInvoker is a stand-in for *plugin.Manager. Tests register
// per-(plugin, method) handlers; Call dispatches to them and records
// the JSON params.
type fakeInvoker struct {
	mu        sync.Mutex
	handlers  map[string]func(ctx context.Context, params any) error
	calls     map[string]int
	lastParam map[string]any
}

func newFakeInvoker() *fakeInvoker {
	return &fakeInvoker{
		handlers:  make(map[string]func(ctx context.Context, params any) error),
		calls:     make(map[string]int),
		lastParam: make(map[string]any),
	}
}

func (f *fakeInvoker) Handle(plugin, method string, h func(ctx context.Context, params any) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[plugin+"/"+method] = h
}

func (f *fakeInvoker) Calls(plugin, method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[plugin+"/"+method]
}

func (f *fakeInvoker) LastParams(plugin, method string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastParam[plugin+"/"+method]
}

func (f *fakeInvoker) Call(ctx context.Context, plugin, method string, params, result any) error {
	f.mu.Lock()
	key := plugin + "/" + method
	f.calls[key]++
	f.lastParam[key] = params
	h, ok := f.handlers[key]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("fakeInvoker: no handler for %s", key)
	}
	return h(ctx, params)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestEmit_ReachesPublishers registers a fake EventsHandler analogue and
// asserts the dispatcher delivers an Emit'd event to it via the
// PluginInvoker boundary.
func TestEmit_ReachesPublishers(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	inv := newFakeInvoker()

	received := make(chan map[string]any, 1)
	inv.Handle("nats", protoevents.MethodEventsPublish, func(ctx context.Context, params any) error {
		m, ok := params.(map[string]any)
		if !ok {
			return errors.New("unexpected params shape")
		}
		select {
		case received <- m:
		default:
		}
		return nil
	})

	d, err := protoevents.New(protoevents.Options{
		Plugins:     inv,
		Logger:      discardLogger(),
		Clock:       clk,
		PluginNames: []string{"nats"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(loopDone)
	}()

	pid := store.PrincipalID(42)
	payload, _ := protoevents.MarshalPayload(protoevents.AuthSuccessPayload{
		PrincipalID: 42,
		Protocol:    "imap",
		SourceIP:    "10.0.0.1",
	})
	if err := d.Emit(ctx, protoevents.Event{
		Kind:        protoevents.EventAuthSuccess,
		Subject:     "42",
		PrincipalID: &pid,
		Payload:     payload,
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	select {
	case got := <-received:
		ev, _ := got["event"].(map[string]any)
		if ev == nil {
			t.Fatalf("missing event in params: %#v", got)
		}
		if ev["kind"] != string(protoevents.EventAuthSuccess) {
			t.Fatalf("kind: got %v", ev["kind"])
		}
		if ev["id"] == "" {
			t.Fatalf("ID was not assigned")
		}
		if _, ok := ev["payload"].(map[string]any); !ok {
			t.Fatalf("payload not decoded: %#v", ev["payload"])
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for publish")
	}
	cancel()
	<-loopDone
}

// TestEmit_ChangeFeed_DerivedKinds drives the fakestore change feed and
// asserts that mail.received events appear at the registered publisher.
func TestEmit_ChangeFeed_DerivedKinds(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	inv := newFakeInvoker()
	got := make(chan map[string]any, 4)
	inv.Handle("bus", protoevents.MethodEventsPublish, func(_ context.Context, params any) error {
		m := params.(map[string]any)
		ev := m["event"].(map[string]any)
		if ev["kind"] == string(protoevents.EventMailReceived) {
			select {
			case got <- ev:
			default:
			}
		}
		return nil
	})

	d, err := protoevents.New(protoevents.Options{
		Store:                st,
		Plugins:              inv,
		Logger:               discardLogger(),
		Clock:                clk,
		PluginNames:          []string{"bus"},
		ChangeFeedPrincipals: []store.PrincipalID{p.ID},
		PollInterval:         50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	loopDone := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(loopDone)
	}()

	// Insert a message, which appends a (Email, Created) StateChange.
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		MailboxID: mb.ID,
		Size:      42,
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	// The dispatcher fires an After timer using the FakeClock, so
	// advance it to wake the poller.
	deadline := time.Now().Add(2 * time.Second)
	for {
		clk.Advance(100 * time.Millisecond)
		select {
		case ev := <-got:
			if ev["kind"] != string(protoevents.EventMailReceived) {
				t.Fatalf("kind: got %v", ev["kind"])
			}
			cancel()
			<-loopDone
			return
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for mail.received")
		}
	}
}

// TestPublisher_Failure_RetriesThenLogged drives a permanently-failing
// publisher and asserts the retry budget is consumed exactly once
// before a publish.failed envelope is enqueued. With MaxRetries=3 the
// dispatcher invokes Call exactly 4 times for the original event.
func TestPublisher_Failure_RetriesThenLogged(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	inv := newFakeInvoker()
	var firstKindCalls atomic.Int32
	failureSeen := make(chan struct{}, 1)
	inv.Handle("nats", protoevents.MethodEventsPublish, func(_ context.Context, params any) error {
		ev := params.(map[string]any)["event"].(map[string]any)
		switch ev["kind"] {
		case string(protoevents.EventAuthSuccess):
			firstKindCalls.Add(1)
			return errors.New("permanent failure")
		case string(protoevents.EventPublishFailed):
			select {
			case failureSeen <- struct{}{}:
			default:
			}
			return nil
		}
		return nil
	})

	d, err := protoevents.New(protoevents.Options{
		Plugins:        inv,
		Logger:         discardLogger(),
		Clock:          clk,
		PluginNames:    []string{"nats"},
		MaxRetries:     3,
		RetryBackoff:   time.Millisecond,
		PublishTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(loopDone)
	}()

	if err := d.Emit(ctx, protoevents.Event{
		Kind: protoevents.EventAuthSuccess,
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Drive backoff timers (FakeClock).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(10 * time.Millisecond)
		if firstKindCalls.Load() >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := firstKindCalls.Load(); got != 4 {
		t.Fatalf("firstKindCalls: got %d, want 4 (1 initial + 3 retries)", got)
	}

	select {
	case <-failureSeen:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for publish.failed envelope")
	}
	cancel()
	<-loopDone
}

// TestBuffer_FullBlocksEmit fills a tiny buffer, parks the dispatcher
// loop, and asserts Emit blocks until ctx fires (the buffer's only
// drain is the loop, which never starts here).
func TestBuffer_FullBlocksEmit(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	d, err := protoevents.New(protoevents.Options{
		Plugins:     newFakeInvoker(),
		Logger:      discardLogger(),
		Clock:       clk,
		PluginNames: []string{"nats"},
		BufferSize:  1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Do not start the loop. The first Emit fills the buffer; the
	// second blocks until ctx deadline.
	if err := d.Emit(context.Background(), protoevents.Event{Kind: protoevents.EventAuthSuccess}); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = d.Emit(ctx, protoevents.Event{Kind: protoevents.EventAuthSuccess})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Emit: got %v, want DeadlineExceeded", err)
	}
}

// TestCursorResume_AcrossRestart inserts a message, runs a dispatcher
// to consume the change-feed event, restarts a fresh dispatcher with
// the same store, inserts another message, and confirms only the
// second event is delivered to the new dispatcher (the cursor was
// persisted).
func TestCursorResume_AcrossRestart(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	runOnce := func(ctx context.Context, recv chan<- string) (*protoevents.Dispatcher, chan struct{}) {
		inv := newFakeInvoker()
		inv.Handle("bus", protoevents.MethodEventsPublish, func(_ context.Context, params any) error {
			ev := params.(map[string]any)["event"].(map[string]any)
			if ev["kind"] == string(protoevents.EventMailReceived) {
				p := ev["payload"].(map[string]any)
				select {
				case recv <- p["message_id"].(string):
				default:
				}
			}
			return nil
		})
		d, err := protoevents.New(protoevents.Options{
			Store:                st,
			Plugins:              inv,
			Logger:               discardLogger(),
			Clock:                clk,
			PluginNames:          []string{"bus"},
			ChangeFeedPrincipals: []store.PrincipalID{p.ID},
			PollInterval:         20 * time.Millisecond,
			MaxRetries:           1,
			RetryBackoff:         time.Millisecond,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		done := make(chan struct{})
		go func() {
			_ = d.Run(ctx)
			close(done)
		}()
		return d, done
	}

	first := make(chan string, 4)
	ctx1, cancel1 := context.WithCancel(ctx)
	_, done1 := runOnce(ctx1, first)

	id1, _, err := st.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Size: 1})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	_ = id1
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(50 * time.Millisecond)
		select {
		case <-first:
			goto firstDone
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("first dispatcher did not deliver mail.received")
firstDone:
	// Allow the cursor write to settle (the dispatcher writes after
	// emitting). One more poll cycle does it.
	clk.Advance(50 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	cancel1()
	<-done1

	// Verify the persisted cursor advanced past the first message's
	// state-change row.
	cursorKey := fmt.Sprintf("events.changefeed.%d", p.ID)
	got, err := st.Meta().GetFTSCursor(ctx, cursorKey)
	if err != nil {
		t.Fatalf("GetFTSCursor: %v", err)
	}
	if got == 0 {
		t.Fatalf("expected non-zero cursor after first run")
	}

	// Second run: insert another message and assert only that one
	// surfaces.
	second := make(chan string, 4)
	ctx2, cancel2 := context.WithCancel(ctx)
	_, done2 := runOnce(ctx2, second)
	defer func() {
		cancel2()
		<-done2
	}()

	id2, _, err := st.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Size: 1})
	if err != nil {
		t.Fatalf("InsertMessage(2): %v", err)
	}
	_ = id2

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(50 * time.Millisecond)
		select {
		case msgID := <-second:
			// Should be the second message; the first must not
			// reappear.
			if msgID == fmt.Sprintf("%d", id1) {
				t.Fatalf("first message replayed after restart")
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("second dispatcher did not deliver mail.received")
}

// TestEvent_IDIsULIDShape verifies the assigned ID is 26 chars from
// the Crockford-Base32 alphabet, matching the ULID spec.
func TestEvent_IDIsULIDShape(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	d, _ := protoevents.New(protoevents.Options{
		Plugins:     newFakeInvoker(),
		Logger:      discardLogger(),
		Clock:       clk,
		PluginNames: []string{"x"},
		BufferSize:  4,
	})
	// We cannot inspect the in-flight Event from outside, so probe via
	// TryEmit + then drain through eventToParams indirectly: read the
	// channel by creating an invoker that captures.
	inv := newFakeInvoker()
	captured := make(chan string, 1)
	inv.Handle("x", protoevents.MethodEventsPublish, func(_ context.Context, params any) error {
		ev := params.(map[string]any)["event"].(map[string]any)
		captured <- ev["id"].(string)
		return nil
	})
	d2, _ := protoevents.New(protoevents.Options{
		Plugins:     inv,
		Logger:      discardLogger(),
		Clock:       clk,
		PluginNames: []string{"x"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() { _ = d2.Run(ctx); close(loopDone) }()
	if err := d2.Emit(ctx, protoevents.Event{Kind: protoevents.EventAuthSuccess}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	select {
	case id := <-captured:
		if len(id) != 26 {
			t.Fatalf("id length: got %d (%q)", len(id), id)
		}
		const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
		for _, c := range id {
			ok := false
			for _, a := range alphabet {
				if a == c {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("id contains non-Crockford char %q in %q", c, id)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out")
	}
	cancel()
	<-loopDone
	_ = d
}

// TestPayload_RoundTrips covers payload marshalling for every kind.
func TestPayload_RoundTrips(t *testing.T) {
	t.Parallel()
	cases := []any{
		protoevents.MailReceivedPayload{MessageID: "1", Sender: "a@b", Recipient: "c@d"},
		protoevents.MailSentPayload{QueueID: "q", Sender: "a@b", Recipient: "c@d"},
		protoevents.MailDeliveredPayload{QueueID: "q", Recipient: "c@d", DurationMS: 5},
		protoevents.MailDeferredPayload{QueueID: "q", Recipient: "c@d", Reason: "tmp"},
		protoevents.MailFailedPayload{QueueID: "q", Recipient: "c@d", FinalReason: "550"},
		protoevents.MailSpamVerdictPayload{MessageID: "1", Verdict: "spam", Confidence: 0.9},
		protoevents.AuthSuccessPayload{PrincipalID: 1, Protocol: "imap"},
		protoevents.AuthFailurePayload{Protocol: "smtp", Reason: "bad pass"},
		protoevents.AuthTOTPEnrollPayload{PrincipalID: 1},
		protoevents.AuthOIDCLinkPayload{PrincipalID: 1, Provider: "google"},
		protoevents.QueueRetryPayload{QueueID: "q", Attempt: 2},
		protoevents.ACMECertPayload{Hostname: "mx"},
		protoevents.DKIMKeyRotatedPayload{Domain: "example.com", NewSelector: "k1"},
		protoevents.PluginLifecyclePayload{PluginName: "nats", Phase: "started"},
		protoevents.WebhookFailurePayload{WebhookID: "wh1", Attempts: 3},
		protoevents.PublishFailedPayload{PluginName: "nats", OrigID: "x"},
	}
	for i, c := range cases {
		raw, err := protoevents.MarshalPayload(c)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		var into map[string]any
		if err := json.Unmarshal(raw, &into); err != nil {
			t.Fatalf("case %d: unmarshal: %v", i, err)
		}
	}
}
