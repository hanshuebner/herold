package webpush

// dismiss_test.go covers the mail-dismiss push (re #481, REQ-PUSH-100..104):
// a message read, archived, or destroyed on another session must
// dismiss the notification an earlier arrival push caused, on every
// transport, exactly once per qualifying transition. Per the project's
// fakes-before-fixes rule the FCM leg drives the same in-tree fake
// gateway dispatcher_fcm_test.go (re #334) wires; the Web Push leg
// reuses dispatcher_test.go's fake gateway + recipient key material.
//
// Every scenario runs on both SQLite (the default) and Postgres (via
// HEROLD_PG_DSN) through the shared testDismiss* helpers below.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/fcm"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/vapid"
)

// dismissFixture bundles a dispatcher wired to both a Web Push and an
// FCM subscription on the same principal, plus an Inbox, an Archive,
// and a Sent mailbox, so a single fixture can drive every dismiss
// scenario against both transports.
type dismissFixture struct {
	store     store.Store
	clk       *clock.FakeClock
	disp      *Dispatcher
	pid       store.PrincipalID
	inboxID   store.MailboxID
	archiveID store.MailboxID
	sentID    store.MailboxID
	webSubID  store.PushSubscriptionID
	fcmSubID  store.PushSubscriptionID
	priv      *ecdh.PrivateKey
	auth      []byte
	webGW     *fakeGateway
	fcmGW     *fakeFCMGateway
}

// newDismissFixture builds a fixture backed by st, or a fresh
// temp-file SQLite store when st is nil. The caller owns closing a
// non-nil st (mirroring setupFixtureWithStore's convention elsewhere
// in the tree); a nil st is closed via t.Cleanup here.
func newDismissFixture(t *testing.T, st store.Store, opts ...func(*Options)) *dismissFixture {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	if st == nil {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		var err error
		st, err = storesqlite.OpenWithRand(ctx, dbPath, nil, clk, rand.Reader)
		if err != nil {
			t.Fatalf("storesqlite.OpenWithRand: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
	}

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	auth := make([]byte, authSecretLen)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("rand auth secret: %v", err)
	}

	webGW := newGateway(http.StatusCreated)
	t.Cleanup(webGW.Close)
	fcmGW := newFakeFCMGateway(http.StatusOK)
	t.Cleanup(fcmGW.Close)

	sender, err := fcm.New(fcm.Options{
		Tokens:   fcm.StaticTokenSource("fake-fcm-bearer-token"),
		HTTPDoer: fcmGW.srv.Client(),
		BaseURL:  fcmGW.srv.URL + "/v1/projects/test-project/messages:send",
	})
	if err != nil {
		t.Fatalf("fcm.New: %v", err)
	}

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: fmt.Sprintf("alice-dismiss-%d@example.test", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	if err != nil {
		t.Fatalf("InsertMailbox INBOX: %v", err)
	}
	archive, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Archive"})
	if err != nil {
		t.Fatalf("InsertMailbox Archive: %v", err)
	}
	sent, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Sent"})
	if err != nil {
		t.Fatalf("InsertMailbox Sent: %v", err)
	}

	webSubID, err := st.Meta().InsertPushSubscription(ctx, store.PushSubscription{
		PrincipalID:    p.ID,
		DeviceClientID: "web-device",
		URL:            webGW.URL(),
		P256DH:         priv.PublicKey().Bytes(),
		Auth:           auth,
		Verified:       true,
		Types:          []string{"Email"},
	})
	if err != nil {
		t.Fatalf("InsertPushSubscription (web): %v", err)
	}
	fcmSubID, err := st.Meta().InsertPushSubscription(ctx, store.PushSubscription{
		PrincipalID:    p.ID,
		DeviceClientID: "android-device",
		Transport:      store.PushTransportFCM,
		FCMToken:       "fcm-device-token-1",
		Verified:       true,
		Types:          []string{"Email"},
	})
	if err != nil {
		t.Fatalf("InsertPushSubscription (fcm): %v", err)
	}

	kp, err := vapid.Generate(nil)
	if err != nil {
		t.Fatalf("vapid.Generate: %v", err)
	}
	o := Options{
		Store:    st,
		VAPID:    vapid.NewWithKey(kp),
		FCM:      sender,
		Clock:    clk,
		HTTPDoer: webGW.srv.Client(),
		Hostname: "example.test",
	}
	for _, opt := range opts {
		opt(&o)
	}
	disp, err := New(o)
	if err != nil {
		t.Fatalf("webpush.New: %v", err)
	}
	// Fast-forward the cursor past any change-feed rows already present
	// (the Postgres leg shares one database, and thus one global
	// change feed, across every test function): a freshly constructed
	// Dispatcher's cursor starts at 0 in memory regardless of what a
	// prior test's dispatcher persisted under the same cursor key,
	// since only Run (never called by these tests) hydrates it from
	// storage. Without this, this fixture would replay earlier tests'
	// arrival and dismiss events through its own gateways.
	for {
		n, err := disp.tick(ctx)
		if err != nil {
			t.Fatalf("catch-up tick: %v", err)
		}
		if n == 0 {
			break
		}
	}

	return &dismissFixture{
		store:     st,
		clk:       clk,
		disp:      disp,
		pid:       p.ID,
		inboxID:   inbox.ID,
		archiveID: archive.ID,
		sentID:    sent.ID,
		webSubID:  webSubID,
		fcmSubID:  fcmSubID,
		priv:      priv,
		auth:      auth,
		webGW:     webGW,
		fcmGW:     fcmGW,
	}
}

func (f *dismissFixture) tick(t *testing.T) {
	t.Helper()
	if _, err := f.disp.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// insertMessage stores a message directly in mailboxID and ticks the
// dispatcher once so the resulting Created change-feed row is
// processed (an Inbox arrival records Inbox-presence in the dispatch
// tracker; the corresponding "mail" push, if any, is not asserted
// here). Returns the assigned MessageID.
func (f *dismissFixture) insertMessage(t *testing.T, mailboxID store.MailboxID, subject string) store.MessageID {
	t.Helper()
	ctx := context.Background()
	ref, err := f.store.Blobs().Put(ctx, strings.NewReader("body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		Blob:     ref,
		Size:     ref.Size,
		Envelope: store.Envelope{From: "Bob <bob@example.test>", Subject: subject},
	}, []store.MessageMailbox{{MailboxID: mailboxID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	rows, err := f.store.Meta().ListMessages(ctx, mailboxID, store.MessageFilter{Limit: 100})
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListMessages: %v %d", err, len(rows))
	}
	msgID := rows[len(rows)-1].ID
	f.tick(t)
	return msgID
}

// markSeen sets $seen on msgID's membership in mailboxID (mirroring a
// JMAP Email/set { keywords: {"$seen": true} } patch from a second
// session) and ticks the dispatcher.
func (f *dismissFixture) markSeen(t *testing.T, msgID store.MessageID, mailboxID store.MailboxID) {
	t.Helper()
	if _, err := f.store.Meta().UpdateMessageFlags(context.Background(), msgID, mailboxID,
		store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}
	f.tick(t)
}

// archive moves msgID from f.inboxID to f.archiveID (mirroring a JMAP
// Email/set mailboxIds patch that replaces Inbox with Archive from a
// second session) and ticks the dispatcher.
func (f *dismissFixture) archive(t *testing.T, msgID store.MessageID) {
	t.Helper()
	if err := f.store.Meta().MoveMessage(context.Background(), msgID, f.inboxID, f.archiveID); err != nil {
		t.Fatalf("MoveMessage: %v", err)
	}
	f.tick(t)
}

// destroy expunges msgID from mailboxID (mirroring JMAP Email/destroy,
// which calls ExpungeMessages once per remaining membership) and ticks
// the dispatcher.
func (f *dismissFixture) destroy(t *testing.T, msgID store.MessageID, mailboxID store.MailboxID) {
	t.Helper()
	if err := f.store.Meta().ExpungeMessages(context.Background(), mailboxID, []store.MessageID{msgID}); err != nil {
		t.Fatalf("ExpungeMessages: %v", err)
	}
	f.tick(t)
}

// setQuietHoursCoveringNow configures the web subscription's rules
// with a quiet-hours window that covers every hour of the day in the
// fixture's fake-clock timezone (UTC), so any arrival push would be
// suppressed while a dismiss (REQ-PUSH-102: exempt from quiet hours)
// must still go through.
func (f *dismissFixture) setQuietHoursCoveringNow(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	sub, err := f.store.Meta().GetPushSubscription(ctx, f.webSubID)
	if err != nil {
		t.Fatalf("GetPushSubscription: %v", err)
	}
	sub.NotificationRulesJSON = []byte(`{"master":true,"quietHours":{"startHourLocal":0,"endHourLocal":23,"tz":"UTC"}}`)
	if err := f.store.Meta().UpdatePushSubscription(ctx, sub); err != nil {
		t.Fatalf("UpdatePushSubscription: %v", err)
	}
}

// setMaster sets the web subscription's master switch, the only gate
// a dismiss respects (REQ-PUSH-102).
func (f *dismissFixture) setMaster(t *testing.T, on bool) {
	t.Helper()
	ctx := context.Background()
	sub, err := f.store.Meta().GetPushSubscription(ctx, f.webSubID)
	if err != nil {
		t.Fatalf("GetPushSubscription: %v", err)
	}
	sub.NotificationRulesJSON = []byte(fmt.Sprintf(`{"master":%v}`, on))
	if err := f.store.Meta().UpdatePushSubscription(ctx, sub); err != nil {
		t.Fatalf("UpdatePushSubscription: %v", err)
	}
}

// dismissPayload is the subset of the mail-dismiss wire shape the
// tests assert on.
type dismissPayload struct {
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	EmailID  string `json:"emailId"`
	ThreadID string `json:"threadId"`
	Reason   string `json:"reason"`
}

// lastWebDismiss decrypts and decodes the most recent Web Push POST,
// asserting it is shaped like a mail-dismiss payload.
func (f *dismissFixture) lastWebDismiss(t *testing.T) dismissPayload {
	t.Helper()
	calls := f.webGW.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one Web Push POST")
	}
	plain, err := decryptForTest(calls[len(calls)-1].Body, f.priv, f.auth)
	if err != nil {
		t.Fatalf("decryptForTest: %v", err)
	}
	var p dismissPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatalf("unmarshal web push dismiss payload: %v (%s)", err, plain)
	}
	return p
}

// lastFCMDismiss decodes the most recent FCM POST's data.payload,
// asserting it is shaped like a mail-dismiss payload.
func (f *dismissFixture) lastFCMDismiss(t *testing.T) dismissPayload {
	t.Helper()
	calls := f.fcmGW.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one FCM send")
	}
	raw, ok := calls[len(calls)-1].Data["payload"]
	if !ok {
		t.Fatalf("message.data.payload missing: %+v", calls[len(calls)-1].Data)
	}
	var p dismissPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal fcm dismiss payload: %v (%s)", err, raw)
	}
	return p
}

func assertDismiss(t *testing.T, p dismissPayload, wantEmailID string, wantReason string) {
	t.Helper()
	if p.Kind != "mail-dismiss" || p.Type != "mail-dismiss" {
		t.Fatalf("payload kind/type = %q/%q, want mail-dismiss/mail-dismiss", p.Kind, p.Type)
	}
	if p.EmailID != wantEmailID {
		t.Fatalf("emailId = %q, want %q", p.EmailID, wantEmailID)
	}
	if p.ThreadID == "" {
		t.Fatalf("threadId missing")
	}
	if p.Reason != wantReason {
		t.Fatalf("reason = %q, want %q", p.Reason, wantReason)
	}
}

// -- SQLite entry points --------------------------------------------

func TestDispatcher_MailDismiss_Seen(t *testing.T) {
	t.Parallel()
	testDismissSeen(t, nil)
}

func TestDispatcher_MailDismiss_Archive(t *testing.T) {
	t.Parallel()
	testDismissArchive(t, nil)
}

func TestDispatcher_MailDismiss_Destroy(t *testing.T) {
	t.Parallel()
	testDismissDestroy(t, nil)
}

func TestDispatcher_MailDismiss_NonInboxMessage_NoDismiss(t *testing.T) {
	t.Parallel()
	testDismissNonInbox(t, nil)
}

func TestDispatcher_MailDismiss_CoalescesWithinWindow(t *testing.T) {
	t.Parallel()
	testDismissCoalesces(t, nil)
}

func TestDispatcher_MailDismiss_RepeatedSeenFiresOnce(t *testing.T) {
	t.Parallel()
	testDismissRepeatedSeenFiresOnce(t, nil)
}

func TestDispatcher_MailDismiss_QuietHoursDoesNotSuppress(t *testing.T) {
	t.Parallel()
	testDismissQuietHours(t, nil)
}

func TestDispatcher_MailDismiss_MasterOffSuppresses(t *testing.T) {
	t.Parallel()
	testDismissMasterOff(t, nil)
}

func TestDispatcher_MailDismiss_BypassesRateLimiter(t *testing.T) {
	t.Parallel()
	testDismissBypassesRateLimiter(t, nil)
}

func TestDispatcher_MailDismiss_WithheldDuringRetryBackoff(t *testing.T) {
	t.Parallel()
	testDismissWithheldDuringRetryBackoff(t, nil)
}

// -- Postgres entry points -------------------------------------------
//
// Each opens its own connection against HEROLD_PG_DSN and skips when
// unset/unreachable, per the project's dual-backend testing rule
// (STANDARDS.md). Every message/mailbox/principal created below is
// scoped to a freshly inserted principal, so the tests may share one
// Postgres database safely.

func openPostgresForDismissTest(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.OpenWithRand(context.Background(), dsn, t.TempDir(), nil, nil, rand.Reader)
	if err != nil {
		t.Skipf("storepg.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDispatcher_MailDismiss_Seen_Postgres(t *testing.T) {
	testDismissSeen(t, openPostgresForDismissTest(t))
}

func TestDispatcher_MailDismiss_Archive_Postgres(t *testing.T) {
	testDismissArchive(t, openPostgresForDismissTest(t))
}

func TestDispatcher_MailDismiss_Destroy_Postgres(t *testing.T) {
	testDismissDestroy(t, openPostgresForDismissTest(t))
}

func TestDispatcher_MailDismiss_NonInboxMessage_NoDismiss_Postgres(t *testing.T) {
	testDismissNonInbox(t, openPostgresForDismissTest(t))
}

func TestDispatcher_MailDismiss_BypassesRateLimiter_Postgres(t *testing.T) {
	testDismissBypassesRateLimiter(t, openPostgresForDismissTest(t))
}

func TestDispatcher_MailDismiss_WithheldDuringRetryBackoff_Postgres(t *testing.T) {
	testDismissWithheldDuringRetryBackoff(t, openPostgresForDismissTest(t))
}

// -- shared scenario bodies -------------------------------------------

// testDismissSeen proves: marking an Inbox message $seen through a
// second session fires exactly one mail-dismiss push (reason "seen")
// on the Web Push subscription and one on the FCM subscription. The
// dispatcher never inspects which session or device caused the
// change, so this equally covers the "self-caused $seen" case named
// in the issue: a dismiss fires whether the mutation came from the
// same subscription's owning session or a different one.
func testDismissSeen(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	msgID := f.insertMessage(t, f.inboxID, "hello")

	f.markSeen(t, msgID, f.inboxID)

	wantID := fmt.Sprintf("%d", msgID)
	assertDismiss(t, f.lastWebDismiss(t), wantID, DismissReasonSeen)
	assertDismiss(t, f.lastFCMDismiss(t), wantID, DismissReasonSeen)
}

// testDismissArchive proves: moving an Inbox message to a non-Inbox
// mailbox (archiving) through a second session fires exactly one
// mail-dismiss push (reason "left-inbox") on each transport.
func testDismissArchive(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	msgID := f.insertMessage(t, f.inboxID, "hello")

	f.archive(t, msgID)

	wantID := fmt.Sprintf("%d", msgID)
	assertDismiss(t, f.lastWebDismiss(t), wantID, DismissReasonLeftInbox)
	assertDismiss(t, f.lastFCMDismiss(t), wantID, DismissReasonLeftInbox)
}

// testDismissDestroy proves: destroying an Inbox message (JMAP
// Email/destroy's ExpungeMessages call) through a second session
// fires exactly one mail-dismiss push (reason "destroyed") on each
// transport, even though the message row itself no longer exists by
// the time the dispatcher's asynchronous change-feed read reaches it.
func testDismissDestroy(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	msgID := f.insertMessage(t, f.inboxID, "hello")

	f.destroy(t, msgID, f.inboxID)

	wantID := fmt.Sprintf("%d", msgID)
	assertDismiss(t, f.lastWebDismiss(t), wantID, DismissReasonDestroyed)
	assertDismiss(t, f.lastFCMDismiss(t), wantID, DismissReasonDestroyed)
}

// testDismissNonInbox proves: a message that never held an Inbox-role
// membership (created directly in Sent) never produces a dismissal,
// whether it is marked seen or destroyed.
func testDismissNonInbox(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	// Baseline after fixture setup (the Postgres leg shares one
	// database's global change feed across test functions; the
	// fixture's own catch-up tick has already drained anything
	// pre-existing, but pin the baseline explicitly rather than
	// assuming it is exactly zero).
	webBase := len(f.webGW.Calls())
	fcmBase := len(f.fcmGW.Calls())
	msgID := f.insertMessage(t, f.sentID, "outbound copy")

	f.markSeen(t, msgID, f.sentID)
	if got := len(f.webGW.Calls()) - webBase; got != 0 {
		t.Fatalf("mark-seen on a Sent-only message produced %d Web Push POSTs, want 0", got)
	}
	if got := len(f.fcmGW.Calls()) - fcmBase; got != 0 {
		t.Fatalf("mark-seen on a Sent-only message produced %d FCM sends, want 0", got)
	}

	f.destroy(t, msgID, f.sentID)
	if got := len(f.webGW.Calls()) - webBase; got != 0 {
		t.Fatalf("destroying a Sent-only message produced %d Web Push POSTs, want 0", got)
	}
	if got := len(f.fcmGW.Calls()) - fcmBase; got != 0 {
		t.Fatalf("destroying a Sent-only message produced %d FCM sends, want 0", got)
	}
}

// testDismissCoalesces proves per-email coalescing (REQ-PUSH-103): a
// "seen" dismiss and a "destroyed" dismiss for the SAME message
// arriving within the dispatcher's coalescing window share a tag and
// collapse to one push at the moment of the second transition; the
// deferred push fires with the latest (destroyed) reason once the
// window elapses.
func testDismissCoalesces(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st, func(o *Options) { o.CoalesceWindow = 30 * time.Second })
	msgID := f.insertMessage(t, f.inboxID, "hello")
	// The arrival itself already posted (its own "email/<threadID>"
	// coalesce tag, separate from the dismiss tag); count dismiss
	// POSTs relative to that baseline.
	base := len(f.webGW.Calls())

	f.markSeen(t, msgID, f.inboxID)
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("expected 1 Web Push POST after the seen dismiss, got %d", got)
	}

	// A second qualifying transition for the SAME email inside the
	// window must not add a second POST yet.
	f.clk.Advance(5 * time.Second)
	f.destroy(t, msgID, f.inboxID)
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("second dismiss within the coalescing window POSTed immediately; got %d, want 1 (deferred)", got)
	}

	// Once the window elapses, the deferred push fires with the
	// latest (destroyed) payload.
	f.clk.Advance(30 * time.Second)
	if got := len(f.webGW.Calls()) - base; got != 2 {
		t.Fatalf("expected 2 Web Push POSTs after the coalescing window elapsed, got %d", got)
	}
	assertDismiss(t, f.lastWebDismiss(t), fmt.Sprintf("%d", msgID), DismissReasonDestroyed)
}

// testDismissRepeatedSeenFiresOnce proves the classification layer's
// own dedup: an unrelated later update to an already-dismissed,
// already-read Inbox message (still $seen, still in the Inbox) does
// not refire a "seen" dismissal.
func testDismissRepeatedSeenFiresOnce(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	msgID := f.insertMessage(t, f.inboxID, "hello")
	base := len(f.webGW.Calls())

	f.markSeen(t, msgID, f.inboxID)
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("expected 1 Web Push POST after the seen dismiss, got %d", got)
	}

	// Flip an unrelated flag (\Flagged) on the same, still-seen
	// membership; the message stays $seen and in the Inbox throughout.
	f.clk.Advance(time.Hour)
	if _, err := f.store.Meta().UpdateMessageFlags(context.Background(), msgID, f.inboxID,
		store.MessageFlagFlagged, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}
	f.tick(t)

	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("an unrelated flag change on an already-dismissed message POSTed again; got %d, want 1", got)
	}
}

// testDismissQuietHours proves REQ-PUSH-102's exemption: a dismiss
// still reaches the subscription while its rules configure quiet
// hours covering the whole day.
func testDismissQuietHours(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	f.setQuietHoursCoveringNow(t)
	msgID := f.insertMessage(t, f.inboxID, "hello")

	f.markSeen(t, msgID, f.inboxID)

	assertDismiss(t, f.lastWebDismiss(t), fmt.Sprintf("%d", msgID), DismissReasonSeen)
}

// testDismissMasterOff proves the one gate a dismiss does respect: the
// subscription's master switch.
func testDismissMasterOff(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	f.setMaster(t, false)
	base := len(f.webGW.Calls())
	msgID := f.insertMessage(t, f.inboxID, "hello")

	f.markSeen(t, msgID, f.inboxID)

	if got := len(f.webGW.Calls()) - base; got != 0 {
		t.Fatalf("master-off subscription received %d POSTs, want 0", got)
	}
}

// testDismissBypassesRateLimiter proves REQ-PUSH-102: a dismiss is
// exempt from the per-subscription rate limiter that gates arrivals.
// A single-token bucket is exhausted by one arrival (a second arrival
// immediately after is confirmed rate-limited), and the dismiss for
// the first message still reaches the subscription.
func testDismissBypassesRateLimiter(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st, func(o *Options) { o.RateLimitPerMinute = 1 })
	base := len(f.webGW.Calls())

	msgID := f.insertMessage(t, f.inboxID, "first")
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("expected 1 POST for the first arrival (consumes the only token), got %d", got)
	}

	// The bucket is now empty and the fake clock has not advanced, so
	// a second arrival is rate-limited: confirms the limiter is
	// actually exhausted at this point.
	f.insertMessage(t, f.inboxID, "second")
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("second arrival should have been rate-limited; got %d POSTs (delta), want 1", got)
	}

	// A dismiss for the first message still goes out despite the
	// exhausted bucket.
	f.markSeen(t, msgID, f.inboxID)
	if got := len(f.webGW.Calls()) - base; got != 2 {
		t.Fatalf("dismiss was starved by the exhausted rate limiter; got %d POSTs (delta), want 2", got)
	}
	assertDismiss(t, f.lastWebDismiss(t), fmt.Sprintf("%d", msgID), DismissReasonSeen)
}

// testDismissWithheldDuringRetryBackoff proves a dismiss does not
// bypass the per-subscription retry-backoff window a recent delivery
// failure opened: an endpoint currently failing must not be hammered
// by a dismiss any more than by an arrival. Once the endpoint recovers
// and the backoff window elapses, the next qualifying transition
// reaches it normally.
func testDismissWithheldDuringRetryBackoff(t *testing.T, st store.Store) {
	f := newDismissFixture(t, st)
	var failing atomic.Bool
	failing.Store(true)
	f.webGW.respond = func(r *http.Request) (int, []byte) {
		if failing.Load() {
			return http.StatusServiceUnavailable, nil
		}
		return http.StatusCreated, nil
	}
	base := len(f.webGW.Calls())

	msgID := f.insertMessage(t, f.inboxID, "hello")
	// The arrival's 5xx attempt opened a retry-backoff window for the
	// subscription.
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("expected 1 (failed) POST attempt for the arrival, got %d", got)
	}

	// A dismiss while backoff is active is withheld.
	f.markSeen(t, msgID, f.inboxID)
	if got := len(f.webGW.Calls()) - base; got != 1 {
		t.Fatalf("dismiss reached the subscription during retry backoff; got %d POSTs (delta), want 1 (withheld)", got)
	}

	// The endpoint recovers and the backoff window elapses; the next
	// qualifying transition (archiving) reaches it.
	failing.Store(false)
	f.clk.Advance(2 * time.Second)
	f.archive(t, msgID)
	if got := len(f.webGW.Calls()) - base; got != 2 {
		t.Fatalf("expected the post-backoff dismiss to go out; got %d POSTs (delta), want 2", got)
	}
	assertDismiss(t, f.lastWebDismiss(t), fmt.Sprintf("%d", msgID), DismissReasonLeftInbox)
}

// TestDismissTracker_EvictsAtCapacity proves DismissTracker's memory
// bound: filling it past dismissMaxTracked never grows the underlying
// map beyond the cap, and a message tracked after an eviction round
// still classifies its transition correctly (eviction does not corrupt
// the tracker's own bookkeeping for entries it keeps). This is a pure
// in-memory property of DismissTracker with no store dependency, so it
// runs once rather than per backend.
func TestDismissTracker_EvictsAtCapacity(t *testing.T) {
	t.Parallel()
	tr := NewDismissTracker()
	for i := 0; i < dismissMaxTracked+1000; i++ {
		tr.markInInbox(store.MessageID(i + 1))
	}
	tr.mu.Lock()
	size := len(tr.state)
	tr.mu.Unlock()
	if size > dismissMaxTracked {
		t.Fatalf("tracker grew to %d entries, want <= %d (dismissMaxTracked)", size, dismissMaxTracked)
	}

	// A message tracked after the fill-past-capacity round above still
	// classifies its "left the Inbox" transition correctly: markInInbox
	// records presence, and a subsequent update() reporting no Inbox
	// membership fires left-inbox and drops the entry.
	fresh := store.MessageID(999_999_999)
	tr.markInInbox(fresh)
	if fireSeen, fireLeftInbox := tr.update(fresh, false, false); fireSeen || !fireLeftInbox {
		t.Fatalf("update(fresh, hasInbox=false) = (fireSeen=%v, fireLeftInbox=%v), want (false, true)", fireSeen, fireLeftInbox)
	}

	// The same message no longer classifies as a departure a second
	// time (its tracked state was consumed by the fire above), mirroring
	// testDismissRepeatedSeenFiresOnce's dedup for the "seen" reason.
	if fireSeen, fireLeftInbox := tr.update(fresh, false, false); fireSeen || fireLeftInbox {
		t.Fatalf("update(fresh, hasInbox=false) fired again after consumption: (%v, %v)", fireSeen, fireLeftInbox)
	}
}
