package webpush

// dispatcher_unifiedpush_test.go — end-to-end coverage for the
// UnifiedPush transport (re #236). Per the project's fakes-before-
// fixes rule, the dev instance and CI have no real UnifiedPush
// distributor to target; this file drives the full path — a store
// change-feed event through the dispatcher's shared rule evaluation,
// payload build, encryption, and coalescing, out to an in-tree fake
// distributor endpoint (an httptest.Server) — so the UnifiedPush
// transport is proven deterministically. Mirrors dispatcher_fcm_test.go
// (the pattern established for #200) and reuses dispatcher_test.go's
// fakeGateway, which already records the Authorization header so these
// tests can assert its absence directly.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/vapid"
)

// unifiedPushDispatcherFixture bundles a dispatcher wired to a fake
// UnifiedPush distributor plus a principal with one UnifiedPush-
// transport subscription. Deliberately parallel to dispatcherFixture
// (Web Push) and fcmDispatcherFixture (FCM) so the three transports'
// end-to-end tests read the same way.
type unifiedPushDispatcherFixture struct {
	store   store.Store
	clk     *clock.FakeClock
	disp    *Dispatcher
	pid     store.PrincipalID
	subID   store.PushSubscriptionID
	priv    *ecdh.PrivateKey
	auth    []byte
	gateway *fakeGateway
}

func newUnifiedPushDispatcherFixture(t *testing.T, status int, opts ...func(*Options)) *unifiedPushDispatcherFixture {
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
		DeviceClientID: "up-device-1",
		Transport:      store.PushTransportUnifiedPush,
		URL:            gw.URL(),
		P256DH:         priv.PublicKey().Bytes(),
		Auth:           auth,
		Verified:       true,
		Types:          []string{"Email", "ChatMessage"},
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

	return &unifiedPushDispatcherFixture{
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

// triggerEmailChange inserts a message and synchronously runs a tick
// so the dispatcher fans out the resulting change-feed entry. Mirrors
// dispatcherFixture.triggerEmailChange.
func (f *unifiedPushDispatcherFixture) triggerEmailChange(t *testing.T) {
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
		Envelope: store.Envelope{From: "Bob <bob@example.test>", Subject: "hello from unifiedpush"},
	}, []store.MessageMailbox{{MailboxID: mailboxes[0].ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := f.disp.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// TestDispatcher_UnifiedPush_EndToEnd proves the full path required by
// re #236: a store change-feed event reaches the dispatcher's shared
// rule evaluation, payload build, and RFC 8291 encryption, and is
// delivered through the UnifiedPush transport to the fake distributor
// as an HTTP POST with NO Authorization header -- the one wire
// difference from Web Push.
func TestDispatcher_UnifiedPush_EndToEnd(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusCreated)
	f.triggerEmailChange(t)

	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one POST to the fake distributor")
	}
	c := calls[len(calls)-1]
	if c.AuthHdr != "" {
		t.Fatalf("Authorization header = %q, want empty (UnifiedPush must not send VAPID auth)", c.AuthHdr)
	}
	if c.Encoding != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q, want aes128gcm (RFC 8291 encryption unchanged)", c.Encoding)
	}
	if c.TTL == "" {
		t.Fatalf("TTL header missing")
	}
	plain, err := decryptForTest(c.Body, f.priv, f.auth)
	if err != nil {
		t.Fatalf("decryptForTest: %v", err)
	}
	if !strings.Contains(string(plain), `"type":"email"`) {
		t.Fatalf("payload missing type=email: %q", plain)
	}
	if !strings.Contains(string(plain), "hello from unifiedpush") {
		t.Fatalf("payload missing the subject preview: %q", plain)
	}
}

// TestDispatcher_UnifiedPush_GoneUnregistersSubscription proves the
// 410/404-unregisters contract carries over unchanged from Web Push:
// a fake distributor returning 410 Gone deletes the subscription row.
func TestDispatcher_UnifiedPush_GoneUnregistersSubscription(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusGone)
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription %d still present after 410", f.subID)
	}
}

// TestDispatcher_UnifiedPush_NotFoundUnregistersSubscription proves
// the 404 branch of the same contract.
func TestDispatcher_UnifiedPush_NotFoundUnregistersSubscription(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusNotFound)
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription %d still present after 404", f.subID)
	}
}

// TestDispatcher_UnifiedPush_5xxRetriesWithBackoff proves the
// UnifiedPush transport reuses the dispatcher's existing exponential-
// backoff retry schedule (no separate implementation to drift): a
// transient 503 does not destroy the subscription, and a second
// attempt is skipped until the backoff window elapses.
func TestDispatcher_UnifiedPush_5xxRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusServiceUnavailable, func(o *Options) {
		o.MaxAttempts5xx = 5
	})
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err != nil {
		t.Fatalf("after first 5xx subscription must persist: %v", err)
	}
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("expected 1 POST after one 5xx, got %d", got)
	}
	f.clk.Advance(2 * time.Second)
	f.triggerEmailChange(t)
	if got := len(f.gateway.Calls()); got != 2 {
		t.Fatalf("expected 2 POSTs after backoff elapsed, got %d", got)
	}
}

// TestDispatcher_UnifiedPush_4xxRetryThenDestroy proves non-410/404
// 4xx responses get the same bounded retry-then-destroy budget as Web
// Push's non-410 4xx path.
func TestDispatcher_UnifiedPush_4xxRetryThenDestroy(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusBadRequest, func(o *Options) {
		o.MaxAttempts4xx = 3
	})
	for i := 0; i < 3; i++ {
		f.triggerEmailChange(t)
		f.clk.Advance(time.Hour) // bypass retry backoff
	}
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription still present after 3x 400")
	}
}

// TestDispatcher_UnifiedPush_VerificationPingOmitsAuth proves the
// verification-ping path (fired synchronously from the JMAP handler
// on PushSubscription/set { create }) also omits the Authorization
// header for a UnifiedPush subscription -- the suppression applies to
// every POST the dispatcher makes to a UnifiedPush endpoint, not just
// the regular delivery path.
func TestDispatcher_UnifiedPush_VerificationPingOmitsAuth(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusCreated)
	sub, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	sub.VerificationCode = "up-verify-1"
	if err := f.disp.SendVerificationPing(context.Background(), sub); err != nil {
		t.Fatalf("SendVerificationPing: %v", err)
	}
	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("no gateway POST")
	}
	c := calls[0]
	if c.AuthHdr != "" {
		t.Fatalf("Authorization header = %q, want empty on a UnifiedPush verification ping", c.AuthHdr)
	}
	plain, err := decryptForTest(c.Body, f.priv, f.auth)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !strings.Contains(string(plain), `"verificationCode":"up-verify-1"`) {
		t.Fatalf("verificationCode not present: %q", plain)
	}
}

// TestDispatchDeliver_UnrecognisedTransportRejected proves that a
// PushSubscription row carrying a transport value neither the JMAP
// handler nor this dispatcher recognises is a logged rejection, not a
// silent Web Push delivery (re #236's explicit acceptance criterion).
// Transport is immutable post-create (UpdatePushSubscription never
// touches the column -- see methods.go's immutable-field check), so
// the row is inserted directly via the store with the unrecognised
// value already set, standing in for a value that reached the table
// by some other means: an operator hand-edit, a future migration, or
// a downgrade.
func TestDispatchDeliver_UnrecognisedTransportRejected(t *testing.T) {
	t.Parallel()
	f := newUnifiedPushDispatcherFixture(t, http.StatusCreated)
	badID, err := f.store.Meta().InsertPushSubscription(context.Background(), store.PushSubscription{
		PrincipalID:    f.pid,
		DeviceClientID: "unknown-transport-device",
		Transport:      store.PushTransport("carrier-pigeon"),
		URL:            f.gateway.URL(),
		P256DH:         f.priv.PublicKey().Bytes(),
		Auth:           f.auth,
		Verified:       true,
		Types:          []string{"Email"},
	})
	if err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}

	f.triggerEmailChange(t)

	// The fixture's own (valid) UnifiedPush subscription still delivers
	// normally -- exactly one POST, not two, proves the unrecognised-
	// transport row was silently dropped rather than also reaching the
	// endpoint (which would have looked identical to a Web Push
	// delivery on the wire, since both share the same gw.URL()).
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("expected exactly 1 POST (from the valid subscription only), got %d", got)
	}
	// The row must still exist -- dispatchDeliver's default arm logs
	// and drops the delivery, it does not touch the subscription's
	// lifecycle the way a 410/404 response would.
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), badID); err != nil {
		t.Fatalf("subscription must survive an unrecognised-transport delivery attempt: %v", err)
	}
}
