package snooze_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/snooze"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// recordingHandler is a slog.Handler that captures every record so
// tests can assert on the structured fields of the per-message wake
// log line (issue #274) without scraping formatted text.
type recordingHandler struct {
	mu      *sync.Mutex
	records *[]slog.Record
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{mu: &sync.Mutex{}, records: &[]slog.Record{}}
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) find(msg string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range *h.records {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

func attrValue(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value
			ok = true
			return false
		}
		return true
	})
	return v, ok
}

// fixture holds a pre-baked principal + mailboxes so each test can
// focus on the snooze invariants without re-building the boilerplate.
// inboxID is the principal's Inbox (MailboxAttrInbox set) unless the
// test explicitly builds a no-Inbox fixture, in which case it is 0.
type fixture struct {
	store   store.Store
	clk     *clock.FakeClock
	pid     store.PrincipalID
	mbID    store.MailboxID // legacy alias for inboxID; kept for existing tests
	inboxID store.MailboxID
	logs    *recordingHandler
}

// newFixture opens a fresh on-disk SQLite store and seeds a principal
// with an INBOX mailbox carrying the MailboxAttrInbox attribute — the
// bit the default (no explicit snoozeWakeMailboxId) wake-destination
// resolution keys on (store.ResolveInboxMailbox). A fixture whose
// INBOX carries no attribute bit would silently pass the "wakes into
// Inbox" tests for the wrong reason (name-match fallback) rather than
// the primary attribute path a fresh mailbox created via JMAP/IMAP
// actually has.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return newFixtureWithStore(t, s, clk, "snooze@example.test")
}

// newFixturePostgres is newFixture's Postgres counterpart. Skips the
// test when HEROLD_PG_DSN is unset or the connection cannot be
// established, so the Postgres leg runs as a no-op locally without a
// running server and as a real parity check in CI's storepg leg.
func newFixturePostgres(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// storepg.Open connects to a persistent database rather than a
	// fresh per-test file, so a fixed address would collide with a
	// still-registered principal left behind by an earlier run against
	// the same database (mirrors setupFixturePostgres in
	// internal/protojmap/mail/email/email_test.go).
	canonicalEmail := fmt.Sprintf("snooze-pg-%d@example.test", time.Now().UnixNano())
	return newFixtureWithStore(t, s, clk, canonicalEmail)
}

func newFixtureWithStore(t *testing.T, s store.Store, clk *clock.FakeClock, canonicalEmail string) *fixture {
	t.Helper()
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: canonicalEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	return &fixture{
		store: s, clk: clk, pid: p.ID,
		mbID: mb.ID, inboxID: mb.ID,
		logs: newRecordingHandler(),
	}
}

// insertMailbox creates an additional (non-Inbox) mailbox for f's
// principal, e.g. to snooze a message from Sent/Archive and observe
// the wake destination separately from the origin.
func (f *fixture) insertMailbox(t *testing.T, name string) store.MailboxID {
	t.Helper()
	mb, err := f.store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid, Name: name,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(%s): %v", name, err)
	}
	return mb.ID
}

// snoozeMessageInto inserts a body into originMailbox and calls
// SetSnooze with the supplied deadline and wake destination (nil = no
// explicit destination, matching both the "not specified" and
// "pre-migration NULL row" cases — SetSnooze's contract makes the two
// indistinguishable at the store layer). Returns the message id.
func (f *fixture) snoozeMessageInto(
	t *testing.T,
	originMailbox store.MailboxID,
	body string,
	when time.Time,
	wake *store.MailboxID,
) store.MessageID {
	t.Helper()
	ctx := context.Background()
	ref, err := f.store.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: f.pid,
		Blob:        ref,
		Size:        ref.Size,
	}, []store.MessageMailbox{{MailboxID: originMailbox}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Walk the feed in pages so we don't truncate at the default 1000
	// when the test inserts large batches.
	var cursor store.ChangeSeq
	var id store.MessageID
	for {
		batch, err := f.store.Meta().ReadChangeFeed(ctx, f.pid, cursor, 1000)
		if err != nil {
			t.Fatalf("ReadChangeFeed: %v", err)
		}
		for _, e := range batch {
			cursor = e.Seq
			if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
				id = store.MessageID(e.EntityID)
			}
		}
		if len(batch) < 1000 {
			break
		}
	}
	if id == 0 {
		t.Fatalf("no created entry in feed")
	}
	if _, err := f.store.Meta().SetSnooze(ctx, id, originMailbox, &when, wake); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	return id
}

// snoozeMessage is the single-mailbox convenience wrapper used by the
// pre-existing tests: it snoozes a message inserted into f.mbID (the
// fixture's Inbox) with no explicit wake destination.
func (f *fixture) snoozeMessage(t *testing.T, body string, when time.Time) store.MessageID {
	t.Helper()
	return f.snoozeMessageInto(t, f.mbID, body, when, nil)
}

// mailboxIDs returns the set of mailbox ids msg currently belongs to.
func (f *fixture) mailboxIDs(t *testing.T, id store.MessageID) map[store.MailboxID]bool {
	t.Helper()
	m, err := f.store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage(%d): %v", id, err)
	}
	out := map[store.MailboxID]bool{}
	for _, mm := range m.Mailboxes {
		out[mm.MailboxID] = true
	}
	if len(out) == 0 {
		out[m.MailboxID] = true
	}
	return out
}

func newWorker(f *fixture) *snooze.Worker {
	return snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		Logger:       slog.New(f.logs),
		PollInterval: 30 * time.Second,
		BatchSize:    100,
	})
}

// runToRelease drives w.Run in the background until it has released at
// least want messages (or a 2s deadline expires), then cancels and
// waits for it to stop.
func runToRelease(t *testing.T, w *snooze.Worker, want int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(w.Released()) >= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker.Run: %v", err)
	}
	if got := int(w.Released()); got < want {
		t.Fatalf("Released = %d, want >= %d", got, want)
	}
}

func TestWorker_WakesDueMessages(t *testing.T) {
	f := newFixture(t)
	t1 := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	id1 := f.snoozeMessage(t, "msg1", t1)
	id2 := f.snoozeMessage(t, "msg2", t1)

	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
		BatchSize:    100,
	})
	// Advance past the deadline so the first tick processes both.
	f.clk.Advance(2 * time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Released() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.Released() < 2 {
		t.Fatalf("Released = %d, want >= 2", w.Released())
	}

	// Both messages should have SnoozedUntil cleared and "$snoozed"
	// keyword removed.
	for _, id := range []store.MessageID{id1, id2} {
		m, err := f.store.Meta().GetMessage(context.Background(), id)
		if err != nil {
			t.Fatalf("GetMessage(%d): %v", id, err)
		}
		if m.SnoozedUntil != nil {
			t.Errorf("msg %d: SnoozedUntil = %v, want nil", id, m.SnoozedUntil)
		}
		for _, k := range m.Keywords {
			if k == "$snoozed" {
				t.Errorf("msg %d: $snoozed keyword still set", id)
			}
		}
	}
}

func TestWorker_BoundedBatch(t *testing.T) {
	f := newFixture(t)
	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	const total = 1000
	for i := 0; i < total; i++ {
		f.snoozeMessage(t, "body", due)
	}
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
		BatchSize:    100,
	})
	f.clk.Advance(2 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// On slow CI runners with on-disk SQLite, releasing 1000 messages
	// (one tx per SetSnooze) can take >5s; the success path breaks
	// early, so a generous deadline only affects truly broken runs.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if w.Released() >= total {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.Released() < total {
		t.Fatalf("Released = %d, want %d", w.Released(), total)
	}
}

func TestWorker_ContextCancel_Stops(t *testing.T) {
	f := newFixture(t)
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("worker did not stop after ctx cancel")
	}
}

func TestWorker_NoDueMessages_NoOp(t *testing.T) {
	f := newFixture(t)
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// Give the worker a moment to run its first tick on an empty
	// store. Released should remain 0.
	time.Sleep(50 * time.Millisecond)
	if got := w.Released(); got != 0 {
		t.Fatalf("Released = %d on empty store, want 0", got)
	}
}

// -- issue #274: wake-destination behaviour --------------------------

// testWakeExplicitDestination is the shared body for the "explicit
// wake destination" acceptance case: a message snoozed from Sent with
// an explicit wake destination gains that destination's membership on
// wake while RETAINING its Sent membership, and the snooze clears.
func testWakeExplicitDestination(t *testing.T, f *fixture) {
	sent := f.insertMailbox(t, "Sent")
	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	wake := f.inboxID
	id := f.snoozeMessageInto(t, sent, "explicit-dest", due, &wake)

	w := newWorker(f)
	f.clk.Advance(2 * time.Hour)
	runToRelease(t, w, 1)

	got := f.mailboxIDs(t, id)
	if !got[sent] {
		t.Errorf("mailboxes = %v, want Sent (%d) retained", got, sent)
	}
	if !got[f.inboxID] {
		t.Errorf("mailboxes = %v, want Inbox (%d) gained", got, f.inboxID)
	}
	if len(got) != 2 {
		t.Errorf("mailboxes = %v, want exactly {Sent, Inbox}", got)
	}
	m, err := f.store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.SnoozedUntil != nil {
		t.Errorf("SnoozedUntil = %v, want nil", m.SnoozedUntil)
	}
}

func TestWorker_WakeExplicitDestination(t *testing.T) {
	testWakeExplicitDestination(t, newFixture(t))
}

func TestWorker_WakeExplicitDestination_Postgres(t *testing.T) {
	testWakeExplicitDestination(t, newFixturePostgres(t))
}

// testWakeDefaultsToInbox is the shared body for "no explicit wake
// destination wakes into Inbox": SetSnooze is called with wake=nil
// (the store-level state a pre-migration NULL row also has), and the
// worker resolves the principal's Inbox as the destination.
func testWakeDefaultsToInbox(t *testing.T, f *fixture) {
	archive := f.insertMailbox(t, "Archive")
	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	id := f.snoozeMessageInto(t, archive, "default-dest", due, nil)

	w := newWorker(f)
	f.clk.Advance(2 * time.Hour)
	runToRelease(t, w, 1)

	got := f.mailboxIDs(t, id)
	if !got[archive] {
		t.Errorf("mailboxes = %v, want Archive (%d) retained", got, archive)
	}
	if !got[f.inboxID] {
		t.Errorf("mailboxes = %v, want Inbox (%d) gained by default", got, f.inboxID)
	}
}

func TestWorker_WakeDefaultsToInbox(t *testing.T) {
	testWakeDefaultsToInbox(t, newFixture(t))
}

func TestWorker_WakeDefaultsToInbox_Postgres(t *testing.T) {
	testWakeDefaultsToInbox(t, newFixturePostgres(t))
}

// TestWorker_NoInbox_WakesInPlace covers the fallback when a
// principal has no resolvable Inbox at all: the message wakes in its
// origin mailbox with no membership add.
func TestWorker_NoInbox_WakesInPlace(t *testing.T) {
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "no-inbox@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	// Neither mailbox carries MailboxAttrInbox nor is named "INBOX":
	// store.ResolveInboxMailbox returns nil for this principal.
	archive, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Archive"})
	if err != nil {
		t.Fatalf("InsertMailbox(Archive): %v", err)
	}
	f := &fixture{store: s, clk: clk, pid: p.ID, mbID: archive.ID, logs: newRecordingHandler()}

	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	id := f.snoozeMessageInto(t, archive.ID, "no-inbox", due, nil)

	w := newWorker(f)
	f.clk.Advance(2 * time.Hour)
	runToRelease(t, w, 1)

	got := f.mailboxIDs(t, id)
	if len(got) != 1 || !got[archive.ID] {
		t.Errorf("mailboxes = %v, want exactly {Archive (%d)} (wake in place)", got, archive.ID)
	}
	m, err := f.store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.SnoozedUntil != nil {
		t.Errorf("SnoozedUntil = %v, want nil", m.SnoozedUntil)
	}
}

// TestWorker_WakeIntoExistingMembership_Tolerated covers the
// AddMessageToMailbox-is-not-idempotent contract: when the message is
// already a member of the wake destination (a race, or an operator
// filing it there manually before the wake fires), AddMessageToMailbox
// returns store.ErrConflict and the worker tolerates it as a no-op,
// still clearing the snooze.
func TestWorker_WakeIntoExistingMembership_Tolerated(t *testing.T) {
	f := newFixture(t)
	sent := f.insertMailbox(t, "Sent")
	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	wake := f.inboxID
	id := f.snoozeMessageInto(t, sent, "already-member", due, &wake)

	// Pre-add the Inbox membership before the wake fires.
	if _, _, err := f.store.Meta().AddMessageToMailbox(context.Background(), id, f.inboxID); err != nil {
		t.Fatalf("AddMessageToMailbox (pre-seed): %v", err)
	}

	w := newWorker(f)
	f.clk.Advance(2 * time.Hour)
	runToRelease(t, w, 1)

	got := f.mailboxIDs(t, id)
	if !got[sent] || !got[f.inboxID] || len(got) != 2 {
		t.Errorf("mailboxes = %v, want exactly {Sent, Inbox}", got)
	}
	m, err := f.store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.SnoozedUntil != nil {
		t.Errorf("SnoozedUntil = %v, want nil after tolerated ErrConflict", m.SnoozedUntil)
	}
}

// TestWorker_LogsPerMessageWake pins the observability requirement
// from issue #274's comments: a wake must log the woken message id
// plus its source and destination mailbox, not just an aggregate
// count. A production outage where the only record of a wake was
// `"snooze: released batch" count=1` (no ids) made a real snoozed
// reminder impossible to identify after the fact.
func TestWorker_LogsPerMessageWake(t *testing.T) {
	f := newFixture(t)
	sent := f.insertMailbox(t, "Sent")
	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	wake := f.inboxID
	id := f.snoozeMessageInto(t, sent, "logged", due, &wake)

	w := newWorker(f)
	f.clk.Advance(2 * time.Hour)
	runToRelease(t, w, 1)

	recs := f.logs.find("snooze: woke message")
	if len(recs) != 1 {
		t.Fatalf("got %d \"snooze: woke message\" records, want 1", len(recs))
	}
	r := recs[0]
	msgID, ok := attrValue(r, "message_id")
	if !ok || msgID.Uint64() != uint64(id) {
		t.Errorf("message_id = %v (ok=%v), want %d", msgID, ok, id)
	}
	src, ok := attrValue(r, "source_mailbox_id")
	if !ok || src.Uint64() != uint64(sent) {
		t.Errorf("source_mailbox_id = %v (ok=%v), want %d", src, ok, sent)
	}
	dest, ok := attrValue(r, "destination_mailbox_id")
	if !ok || dest.Uint64() != uint64(f.inboxID) {
		t.Errorf("destination_mailbox_id = %v (ok=%v), want %d", dest, ok, f.inboxID)
	}
}
