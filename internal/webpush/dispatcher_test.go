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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
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
	store   *fakestore.Store
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
	st, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
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
		MailboxID: mailboxes[0].ID,
		Blob:      ref,
		Size:      ref.Size,
		Envelope:  store.Envelope{From: "Bob <bob@example.test>", Subject: "hello"},
	}); err != nil {
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
	if !strings.Contains(string(plain), `"type":"verification"`) {
		t.Fatalf("verification payload missing type: %q", plain)
	}
	if !strings.Contains(string(plain), `"code":"abc123"`) {
		t.Fatalf("verification code not present: %q", plain)
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

// TestDispatcher_RunBlocksWithoutVAPID exercises the unconfigured-VAPID
// short-circuit: the dispatcher's Run stays alive on ctx but never
// reads the change feed.
func TestDispatcher_RunBlocksWithoutVAPID(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	st, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
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
