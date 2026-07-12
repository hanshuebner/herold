package webpush

// dispatcher_fcm_test.go — end-to-end coverage for the FCM transport
// (re #200). Per the project's fakes-before-fixes rule, the dev
// instance and CI have no real Firebase project to target; this file
// drives the full path — a store change-feed event through the
// dispatcher's rule evaluation, payload build, and coalescing, out to
// an in-tree fake FCM HTTP v1 endpoint (an httptest.Server) — so the
// FCM transport is proven deterministically without ever depending on
// google's real OAuth token endpoint or the live FCM API.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/fcm"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/vapid"
)

// fakeFCMGateway is the in-tree stand-in for the FCM HTTP v1
// messages:send endpoint. It records every POST (auth header + parsed
// body) and replies with a configurable status.
type fakeFCMGateway struct {
	mu      sync.Mutex
	status  int
	calls   []recordedFCMPost
	respond func(*http.Request) (int, []byte)
	srv     *httptest.Server
}

type recordedFCMPost struct {
	AuthHdr         string
	ContentType     string
	Token           string
	Data            map[string]string
	AndroidPriority string
}

// fcmWireEnvelope mirrors the request body internal/fcm.Sender sends;
// duplicated here (rather than imported) so the test asserts against
// the actual wire shape, not the sender's internal type.
type fcmWireEnvelope struct {
	Message struct {
		Token   string            `json:"token"`
		Data    map[string]string `json:"data"`
		Android struct {
			Priority string `json:"priority"`
		} `json:"android"`
	} `json:"message"`
}

func newFakeFCMGateway(status int) *fakeFCMGateway {
	g := &fakeFCMGateway{status: status}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handle))
	return g
}

func (g *fakeFCMGateway) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var env fcmWireEnvelope
	_ = json.Unmarshal(body, &env)
	g.mu.Lock()
	g.calls = append(g.calls, recordedFCMPost{
		AuthHdr:         r.Header.Get("Authorization"),
		ContentType:     r.Header.Get("Content-Type"),
		Token:           env.Message.Token,
		Data:            env.Message.Data,
		AndroidPriority: env.Message.Android.Priority,
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
	_, _ = w.Write([]byte(`{}`))
}

func (g *fakeFCMGateway) Calls() []recordedFCMPost {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]recordedFCMPost, len(g.calls))
	copy(out, g.calls)
	return out
}

func (g *fakeFCMGateway) Close() { g.srv.Close() }

// fcmDispatcherFixture bundles a dispatcher wired to a fake FCM
// gateway plus a principal with one FCM-transport subscription.
type fcmDispatcherFixture struct {
	store   store.Store
	clk     *clock.FakeClock
	disp    *Dispatcher
	pid     store.PrincipalID
	subID   store.PushSubscriptionID
	gateway *fakeFCMGateway
}

func newFCMDispatcherFixture(t *testing.T, status int, opts ...func(*Options)) *fcmDispatcherFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.Open(context.Background(), dbPath, nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gw := newFakeFCMGateway(status)
	t.Cleanup(gw.Close)

	sender, err := fcm.New(fcm.Options{
		Tokens:   fcm.StaticTokenSource("fake-fcm-bearer-token"),
		HTTPDoer: gw.srv.Client(),
		BaseURL:  gw.srv.URL + "/v1/projects/test-project/messages:send",
	})
	if err != nil {
		t.Fatalf("fcm.New: %v", err)
	}

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
		DeviceClientID: "android-device-1",
		Transport:      store.PushTransportFCM,
		FCMToken:       "device-registration-token-1",
		Verified:       true,
		Types:          []string{"Email", "ChatMessage"},
	})
	if err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}

	kp, err := vapid.Generate(nil)
	if err != nil {
		t.Fatalf("vapid.Generate: %v", err)
	}
	o := Options{
		Store:    st,
		VAPID:    vapid.NewWithKey(kp), // unused by the FCM path; present so Run's guard doesn't idle.
		FCM:      sender,
		Clock:    clk,
		Logger:   nil,
		HTTPDoer: http.DefaultClient,
		Hostname: "example.test",
	}
	for _, opt := range opts {
		opt(&o)
	}
	disp, err := New(o)
	if err != nil {
		t.Fatalf("webpush.New: %v", err)
	}

	return &fcmDispatcherFixture{
		store:   st,
		clk:     clk,
		disp:    disp,
		pid:     p.ID,
		subID:   subID,
		gateway: gw,
	}
}

// triggerEmailChange inserts a message and synchronously runs a tick
// so the dispatcher fans out the resulting change-feed entry. Mirrors
// dispatcherFixture.triggerEmailChange in dispatcher_test.go.
func (f *fcmDispatcherFixture) triggerEmailChange(t *testing.T) {
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
		Envelope: store.Envelope{From: "Bob <bob@example.test>", Subject: "hello from android"},
	}, []store.MessageMailbox{{MailboxID: mailboxes[0].ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := f.disp.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// triggerChatMessageChange inserts a DM chat message owned by f.pid and
// synchronously runs a tick, mirroring triggerEmailChange but for the
// "high" urgency event class (re #200 priority-mapping follow-up:
// urgencyForKind maps EntityKindChatMessage to "high").
func (f *fcmDispatcherFixture) triggerChatMessageChange(t *testing.T) {
	t.Helper()
	convID, err := f.store.Meta().InsertChatConversation(context.Background(), store.ChatConversation{
		Kind:                 store.ChatConversationKindDM,
		CreatedByPrincipalID: f.pid,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	pid := f.pid
	if _, err := f.store.Meta().InsertChatMessage(context.Background(), store.ChatMessage{
		ConversationID:    convID,
		SenderPrincipalID: &pid,
		BodyText:          "hi from android",
		BodyFormat:        store.ChatBodyFormatText,
	}); err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}
	if _, err := f.disp.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// TestDispatcher_FCM_EndToEnd proves the full path required by
// re #200: a store change-feed event reaches the dispatcher's shared
// rule evaluation and payload build, and is delivered through the FCM
// transport to the fake FCM endpoint with the expected token,
// Authorization header, and data payload.
func TestDispatcher_FCM_EndToEnd(t *testing.T) {
	t.Parallel()
	f := newFCMDispatcherFixture(t, http.StatusOK)
	f.triggerEmailChange(t)

	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one FCM send")
	}
	c := calls[len(calls)-1]
	if c.AuthHdr != "Bearer fake-fcm-bearer-token" {
		t.Fatalf("Authorization = %q, want Bearer fake-fcm-bearer-token", c.AuthHdr)
	}
	if c.ContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", c.ContentType)
	}
	if c.Token != "device-registration-token-1" {
		t.Fatalf("message.token = %q, want the registered device token", c.Token)
	}
	payload, ok := c.Data["payload"]
	if !ok || payload == "" {
		t.Fatalf("message.data.payload missing: %+v", c.Data)
	}
	if !strings.Contains(payload, `"type":"email"`) {
		t.Fatalf("payload missing type=email: %q", payload)
	}
	if !strings.Contains(payload, "hello from android") {
		t.Fatalf("payload missing the subject preview: %q", payload)
	}
	// Data-only: no "notification" key ever leaves the server, so the
	// app's FirebaseMessagingService always runs (architecture doc
	// 04-push.md), including when backgrounded.
	if _, ok := c.Data["notification"]; ok {
		t.Fatalf("data payload must not carry a notification key")
	}
	// Mail is a "normal" urgency event class (urgencyForKind); it must
	// NOT get the high-priority treatment (re #200 priority-mapping
	// follow-up).
	if c.AndroidPriority != "NORMAL" {
		t.Fatalf("android.priority = %q, want NORMAL for a mail push", c.AndroidPriority)
	}
}

// TestDispatcher_FCM_ChatMessageHighPriority proves the priority-
// mapping follow-up to re #200: a chat message is a "high" urgency
// event class (urgencyForKind), and the FCM transport must carry that
// through as AndroidConfig.priority = "HIGH" so the OS delivers it
// promptly through Doze/App Standby, matching Web Push's "high"
// Urgency header for the same event class.
func TestDispatcher_FCM_ChatMessageHighPriority(t *testing.T) {
	t.Parallel()
	f := newFCMDispatcherFixture(t, http.StatusOK)
	f.triggerChatMessageChange(t)

	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one FCM send")
	}
	c := calls[len(calls)-1]
	if c.AndroidPriority != "HIGH" {
		t.Fatalf("android.priority = %q, want HIGH for a chat message push", c.AndroidPriority)
	}
	payload, ok := c.Data["payload"]
	if !ok || payload == "" {
		t.Fatalf("message.data.payload missing: %+v", c.Data)
	}
	if !strings.Contains(payload, `"type":"chat"`) {
		t.Fatalf("payload does not look like a chat message envelope: %q", payload)
	}
}

// TestDispatcher_FCM_UnregisteredDeletesSubscription proves the token-
// lifecycle contract: FCM's 404 UNREGISTERED response (a stale token —
// app uninstalled or reinstalled with a new one) deletes the
// subscription, mirroring Web Push's 404/410 "gone" handling.
func TestDispatcher_FCM_UnregisteredDeletesSubscription(t *testing.T) {
	t.Parallel()
	f := newFCMDispatcherFixture(t, http.StatusNotFound)
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription %d still present after FCM 404 UNREGISTERED", f.subID)
	}
}

// TestDispatcher_FCM_5xxRetriesWithBackoff proves the FCM transport
// reuses the dispatcher's existing exponential-backoff retry schedule
// (no separate implementation to drift): a transient 503 does not
// destroy the subscription, and a second attempt is skipped until the
// backoff window elapses.
func TestDispatcher_FCM_5xxRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	f := newFCMDispatcherFixture(t, http.StatusServiceUnavailable, func(o *Options) {
		o.MaxAttempts5xx = 5
	})
	f.triggerEmailChange(t)
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err != nil {
		t.Fatalf("after first 5xx subscription must persist: %v", err)
	}
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("expected 1 FCM send after one 5xx, got %d", got)
	}
	f.clk.Advance(2 * time.Second)
	f.triggerEmailChange(t)
	if got := len(f.gateway.Calls()); got != 2 {
		t.Fatalf("expected 2 FCM sends after backoff elapsed, got %d", got)
	}
}

// TestDispatcher_FCM_4xxRetryThenDestroy proves non-429/404 4xx
// responses (e.g. FCM's 400 INVALID_ARGUMENT) get the same bounded
// retry-then-destroy budget as Web Push's non-410 4xx path.
func TestDispatcher_FCM_4xxRetryThenDestroy(t *testing.T) {
	t.Parallel()
	f := newFCMDispatcherFixture(t, http.StatusBadRequest, func(o *Options) {
		o.MaxAttempts4xx = 3
	})
	for i := 0; i < 3; i++ {
		f.triggerEmailChange(t)
		f.clk.Advance(time.Hour) // bypass retry backoff
	}
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), f.subID); err == nil {
		t.Fatalf("subscription still present after 3x FCM 400")
	}
}
