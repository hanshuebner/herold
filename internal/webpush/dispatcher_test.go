package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/vapid"
)

// fakeGateway captures POSTs and replies with a configurable status.
type fakeGateway struct {
	mu      sync.Mutex
	status  int
	calls   []recordedPost
	respond func(*http.Request) (int, []byte)
	srv     *httptest.Server
}

type recordedPost struct {
	URL      string
	TTL      string
	Urgency  string
	AuthHdr  string
	Encoding string
	Body     []byte
}

func newGateway(status int) *fakeGateway {
	g := &fakeGateway{status: status}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handle))
	return g
}

func (g *fakeGateway) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.calls = append(g.calls, recordedPost{
		URL:      r.URL.String(),
		TTL:      r.Header.Get("TTL"),
		Urgency:  r.Header.Get("Urgency"),
		AuthHdr:  r.Header.Get("Authorization"),
		Encoding: r.Header.Get("Content-Encoding"),
		Body:     body,
	})
	resp := g.respond
	status := g.status
	g.mu.Unlock()
	if resp != nil {
		s, b := resp(r)
		w.WriteHeader(s)
		_, _ = w.Write(b)
		return
	}
	w.WriteHeader(status)
}

func (g *fakeGateway) URL() string { return g.srv.URL + "/push" }

func (g *fakeGateway) Calls() []recordedPost {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]recordedPost, len(g.calls))
	copy(out, g.calls)
	return out
}

func (g *fakeGateway) Close() { g.srv.Close() }

// dispatcherFixture bundles the moving parts of an e2e push test.
type dispatcherFixture struct {
	store   store.Store
	clk     *clock.FakeClock
	disp    *Dispatcher
	pid     store.PrincipalID
	subID   store.PushSubscriptionID
	priv    *ecdh.PrivateKey
	auth    []byte
	gateway *fakeGateway
}

func newDispatcherFixture(t *testing.T, status int, opts ...func(*Options)) *dispatcherFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	auth := make([]byte, authSecretLen)
	rand.Read(auth)

	kp, err := vapid.Generate(nil)
	if err != nil {
		t.Fatalf("vapid.Generate: %v", err)
	}
	mgr := vapid.NewWithKey(kp)

	gw := newGateway(status)
	t.Cleanup(gw.Close)

	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if _, err := st.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
	}); err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	subID, err := st.Meta().InsertPushSubscription(context.Background(), store.PushSubscription{
		PrincipalID:    p.ID,
		DeviceClientID: "test-device",
		URL:            gw.URL(),
		P256DH:         priv.PublicKey().Bytes(),
		Auth:           auth,
		Verified:       true,
		Types:          []string{"Email"},
	})
	if err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}

	o := Options{
		Store:    st,
		VAPID:    mgr,
		Clock:    clk,
		Logger:   slog.Default(),
		HTTPDoer: gw.srv.Client(),
		Hostname: "example.test",
	}
	for _, opt := range opts {
		opt(&o)
	}
	disp, err := New(o)
	if err != nil {
		t.Fatalf("webpush.New: %v", err)
	}

	return &dispatcherFixture{
		store:   st,
		clk:     clk,
		disp:    disp,
		pid:     p.ID,
		subID:   subID,
		priv:    priv,
		auth:    auth,
		gateway: gw,
	}
}

// makeSubscription builds a fresh PushSubscription bound to the
// fixture's principal + recipient key material. Used by tests that need
// multiple subscriptions or need to swap VAPIDKeyAtRegistration (which
// the fakestore treats as immutable post-insert).
func (f *dispatcherFixture) makeSubscription(deviceID, vapidPub string) store.PushSubscription {
	return store.PushSubscription{
		PrincipalID:            f.pid,
		DeviceClientID:         deviceID,
		URL:                    f.gateway.URL(),
		P256DH:                 f.priv.PublicKey().Bytes(),
		Auth:                   f.auth,
		Verified:               true,
		Types:                  []string{"Email"},
		VAPIDKeyAtRegistration: vapidPub,
	}
}

// triggerEmailChange inserts a message and synchronously runs a tick so
// the dispatcher fans out the resulting change-feed entry.
func (f *dispatcherFixture) triggerEmailChange(t *testing.T) {
	t.Helper()
	ref, err := f.store.Blobs().Put(context.Background(), strings.NewReader("body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	mailboxes, err := f.store.Meta().ListMailboxes(context.Background(), f.pid)
	if err != nil || len(mailboxes) == 0 {
		t.Fatalf("ListMailboxes: %v %d", err, len(mailboxes))
	}
	if _, _, err := f.store.Meta().InsertMessage(context.Background(), store.Message{
		Blob:     ref,
		Size:     ref.Size,
		Envelope: store.Envelope{From: "Bob <bob@example.test>", Subject: "hello"},
	}, []store.MessageMailbox{{MailboxID: mailboxes[0].ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := f.disp.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

func TestDispatcher_Success(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)
	f.triggerEmailChange(t)
	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one POST")
	}
	c := calls[len(calls)-1]
	if c.Encoding != "aes128gcm" {
		t.Fatalf("Content-Encoding=%q want aes128gcm", c.Encoding)
	}
	if c.TTL == "" {
		t.Fatalf("TTL header missing")
	}
	if !strings.HasPrefix(c.AuthHdr, "vapid t=") {
		t.Fatalf("Authorization=%q want vapid t=...", c.AuthHdr)
	}
	// Body must decrypt with our recipient key.
	plain, err := decryptForTest(c.Body, f.priv, f.auth)
	if err != nil {
		t.Fatalf("decryptForTest: %v", err)
	}
	if !strings.Contains(string(plain), `"type":"email"`) {
		t.Fatalf("payload missing type=email: %q", plain)
	}
}

func TestDispatcher_GoneDeletesSubscription(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusGone)
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription %d still present after 410", f.subID)
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPushSubscription: want ErrNotFound, got %v", err)
	}
}

func TestDispatcher_NotFoundDeletesSubscription(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusNotFound)
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription %d still present after 404", f.subID)
	}
}

func TestDispatcher_4xxRetryThenDestroy(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusBadRequest, func(o *Options) {
		o.MaxAttempts4xx = 3
	})
	// Three consecutive 400s should destroy the subscription.
	for i := 0; i < 3; i++ {
		f.triggerEmailChange(t)
		f.clk.Advance(time.Hour) // bypass retry backoff
	}
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription still present after 3x 400")
	}
}

func TestDispatcher_5xxRetryWithBackoff(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusServiceUnavailable, func(o *Options) {
		o.MaxAttempts5xx = 5
	})
	// First failure schedules a retry — the subscription stays.
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err != nil {
		t.Fatalf("after first 5xx subscription must persist: %v", err)
	}
	// Subsequent retries before backoff elapses are skipped — verify
	// the gateway saw exactly the one POST so far.
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("expected 1 POST after one 5xx, got %d", got)
	}
	// Advance past the first retry window (1s); the next tick
	// re-issues the POST.
	f.clk.Advance(2 * time.Second)
	f.triggerEmailChange(t)
	if got := len(f.gateway.Calls()); got != 2 {
		t.Fatalf("expected 2 POSTs after backoff elapsed, got %d", got)
	}
}

func TestDispatcher_RateLimitedAfterBurst(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated, func(o *Options) {
		o.RateLimitPerMinute = 5
	})
	for i := 0; i < 7; i++ {
		f.triggerEmailChange(t)
	}
	calls := f.gateway.Calls()
	if len(calls) > 5 {
		t.Fatalf("RateLimitPerMinute=5 but gateway saw %d POSTs", len(calls))
	}
}

func TestDispatcher_VerificationPing_Success(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)
	sub, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	sub.VerificationCode = "abc123"
	if err := f.disp.SendVerificationPing(context.Background(), sub); err != nil {
		t.Fatalf("SendVerificationPing: %v", err)
	}
	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("no gateway POST")
	}
	plain, err := decryptForTest(calls[0].Body, f.priv, f.auth)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !strings.Contains(string(plain), `"@type":"PushVerification"`) {
		t.Fatalf("verification payload missing @type: %q", plain)
	}
	if !strings.Contains(string(plain), `"verificationCode":"abc123"`) {
		t.Fatalf("verificationCode not present: %q", plain)
	}
}

func TestDispatcher_VerificationPing_GoneDeletes(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusGone)
	sub, _ := f.store.Meta().GetPushSubscription(context.Background(), f.subID)
	sub.VerificationCode = "x"
	if err := f.disp.SendVerificationPing(context.Background(), sub); err == nil {
		t.Fatalf("expected error on 410")
	}
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription not deleted after verification 410")
	}
}

// TestDispatcher_TypeFilter_SkipsNonMatching ensures a subscription with
// Types=["ChatMessage"] does not receive an Email change.
func TestDispatcher_TypeFilter_SkipsNonMatching(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)
	sub, _ := f.store.Meta().GetPushSubscription(context.Background(), f.subID)
	sub.Types = []string{"ChatMessage"}
	if err := f.store.Meta().UpdatePushSubscription(context.Background(), sub); err != nil {
		t.Fatalf("UpdatePushSubscription: %v", err)
	}
	f.triggerEmailChange(t)
	if got := len(f.gateway.Calls()); got != 0 {
		t.Fatalf("type-filtered subscription received %d pushes", got)
	}
}

// shutdownFlushStore wraps a real store so the in-loop SetFTSCursor
// path returns context.Canceled while the shutdown-path SetFTSCursor
// (which uses a fresh background ctx) is allowed through. Models the
// race the cursor-on-shutdown fix guards against.
type shutdownFlushStore struct {
	store.Store
	failingMeta *shutdownFlushMeta
}

func (s shutdownFlushStore) Meta() store.Metadata { return s.failingMeta }

type shutdownFlushMeta struct {
	store.Metadata
	parentCtx context.Context
}

func (m *shutdownFlushMeta) SetFTSCursor(ctx context.Context, key string, seq uint64) error {
	if ctx == m.parentCtx {
		return context.Canceled
	}
	return m.Metadata.SetFTSCursor(ctx, key, seq)
}

// TestDispatcher_PersistsCursorOnShutdown asserts that the Run loop's
// shutdown defer flushes the in-memory cursor to disk when the
// in-loop SetFTSCursor lost its race with ctx cancellation. A restart
// with the same cursor key must NOT re-deliver the message that was
// already pushed before shutdown.
func TestDispatcher_PersistsCursorOnShutdown(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	auth := make([]byte, authSecretLen)
	rand.Read(auth)

	kp, err := vapid.Generate(nil)
	if err != nil {
		t.Fatalf("vapid.Generate: %v", err)
	}
	mgr := vapid.NewWithKey(kp)

	gw := newGateway(http.StatusCreated)
	t.Cleanup(gw.Close)

	seedCtx := context.Background()
	p, err := st.Meta().InsertPrincipal(seedCtx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(seedCtx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	if _, err := st.Meta().InsertPushSubscription(seedCtx, store.PushSubscription{
		PrincipalID:    p.ID,
		DeviceClientID: "test-device",
		URL:            gw.URL(),
		P256DH:         priv.PublicKey().Bytes(),
		Auth:           auth,
		Verified:       true,
		Types:          []string{"Email"},
	}); err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}

	const cursorKey = "webpush-shutdown-flush-test"
	runCtx, cancel := context.WithCancel(context.Background())
	wrapped := shutdownFlushStore{
		Store:       st,
		failingMeta: &shutdownFlushMeta{Metadata: st.Meta(), parentCtx: runCtx},
	}
	disp, err := New(Options{
		Store:        wrapped,
		VAPID:        mgr,
		Clock:        clk,
		Logger:       slog.Default(),
		HTTPDoer:     gw.srv.Client(),
		Hostname:     "example.test",
		PollInterval: 20 * time.Millisecond,
		CursorKey:    cursorKey,
	})
	if err != nil {
		t.Fatalf("webpush.New: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- disp.Run(runCtx) }()

	// Insert one message so the change-feed reader has something to
	// process.
	ref, err := st.Blobs().Put(seedCtx, strings.NewReader("body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(seedCtx, store.Message{
		Blob: ref, Size: ref.Size,
		Envelope: store.Envelope{From: "Bob <bob@example.test>", Subject: "hello"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	// Drive Run until the dispatcher's tick has fully completed for at
	// least one delivery: cursor advanced AND the gateway saw the call.
	// Waiting on cursor advance only is not enough — we want to confirm
	// the push really left the dispatcher. Waiting on gw.Calls() only is
	// not enough either, because deliver is invoked from processChange
	// BEFORE the cursor.Store at the end of tick, so the gateway can see
	// the call before the cursor moves.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if disp.cursor.Load() > 0 && len(gw.Calls()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("first dispatcher did not deliver and advance cursor (gw.Calls=%d, cursor=%d)",
				len(gw.Calls()), disp.cursor.Load())
		}
		clk.Advance(20 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}

	memCursor := disp.cursor.Load()

	cancel()
	<-done

	persisted, err := st.Meta().GetFTSCursor(seedCtx, cursorKey)
	if err != nil {
		t.Fatalf("GetFTSCursor post-shutdown: %v", err)
	}
	// The in-memory cursor we sampled before cancel is a lower
	// bound: the dispatcher may have ticked further while we were
	// preparing to cancel. The shutdown flush must persist at least
	// memCursor (and may persist more); without the fix, persisted
	// would be 0.
	finalMemCursor := disp.cursor.Load()
	if persisted < memCursor {
		t.Fatalf("post-shutdown persisted cursor = %d, sampled in-memory = %d (shutdown flush did not run)", persisted, memCursor)
	}
	if persisted != finalMemCursor {
		t.Fatalf("post-shutdown persisted cursor = %d, final in-memory = %d (shutdown flush did not catch the latest advance)", persisted, finalMemCursor)
	}

	deliveriesAfterFirst := len(gw.Calls())

	// Restart with the same cursor key against the unwrapped store.
	// The new dispatcher must NOT re-deliver the existing message.
	disp2, err := New(Options{
		Store:        st,
		VAPID:        mgr,
		Clock:        clk,
		Logger:       slog.Default(),
		HTTPDoer:     gw.srv.Client(),
		Hostname:     "example.test",
		PollInterval: 20 * time.Millisecond,
		CursorKey:    cursorKey,
	})
	if err != nil {
		t.Fatalf("webpush.New (2): %v", err)
	}
	runCtx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- disp2.Run(runCtx2) }()
	for j := 0; j < 8; j++ {
		clk.Advance(20 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}
	cancel2()
	<-done2

	if got := len(gw.Calls()); got != deliveriesAfterFirst {
		t.Fatalf("after restart deliveries = %d, want %d (no double delivery)", got, deliveriesAfterFirst)
	}
}

// TestDispatcher_RunBlocksWithoutVAPID exercises the unconfigured-VAPID
// short-circuit: the dispatcher's Run stays alive on ctx but never
// reads the change feed.
func TestDispatcher_RunBlocksWithoutVAPID(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	defer st.Close()
	disp, err := New(Options{
		Store:  st,
		VAPID:  vapid.New(), // unconfigured
		Clock:  clk,
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- disp.Run(ctx) }()
	time.Sleep(50 * time.Millisecond) // Run should be blocked on ctx
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return after cancel")
	}
}
