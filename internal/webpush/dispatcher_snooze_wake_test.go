package webpush

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/snooze"
	"github.com/hanshuebner/herold/internal/store"
)

// insertSnoozedMessage inserts a message into originMailbox, snoozed to
// wake at `due`, waking into wakeMailboxID (nil = no explicit
// destination). Returns the assigned MessageID.
func (f *dispatcherFixture) insertSnoozedMessage(
	t *testing.T,
	originMailbox store.MailboxID,
	due time.Time,
	wakeMailboxID *store.MailboxID,
	subject string,
) store.MessageID {
	t.Helper()
	ctx := context.Background()
	ref, err := f.store.Blobs().Put(ctx, strings.NewReader("wake-body-"+subject))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: f.pid,
		Blob:        ref,
		Size:        ref.Size,
		Envelope:    store.Envelope{From: "Bob <bob@example.test>", Subject: subject},
	}, []store.MessageMailbox{{MailboxID: originMailbox}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Drain the resulting Created change so it does not pollute the
	// dispatcher tick this test is measuring; the caller inspects
	// gateway.Calls() from this point forward.
	if _, err := f.disp.tick(ctx); err != nil {
		t.Fatalf("drain-tick: %v", err)
	}
	var id store.MessageID
	var cursor store.ChangeSeq
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
	if _, err := f.store.Meta().SetSnooze(ctx, id, originMailbox, &due, wakeMailboxID); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	return id
}

// runSnoozeWorkerOnce drives one release of every currently-due snoozed
// message through the real snooze.Worker (not a hand-rolled
// replica), so this test exercises the exact wake mechanism the
// server runs in production.
func (f *dispatcherFixture) runSnoozeWorkerOnce(t *testing.T, want int) {
	t.Helper()
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
		BatchSize:    100,
	})
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

// TestDispatcher_SnoozeWake_DestinationDiffers_FiresNotification pins
// issue #274's claim that AddMessageToMailbox's ChangeOpCreated state
// change (the wake-destination membership add) is picked up by the
// webpush payload/rules path exactly like an ordinary new-mail
// delivery: BuildPayload accepts the Created event and Evaluate
// classifies it as EventTypeMail, so a wake into a different mailbox
// than the message's origin produces a push POST.
func TestDispatcher_SnoozeWake_DestinationDiffers_FiresNotification(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)
	ctx := context.Background()

	sent, err := f.store.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: f.pid, Name: "Sent"})
	if err != nil {
		t.Fatalf("InsertMailbox(Sent): %v", err)
	}
	mailboxes, err := f.store.Meta().ListMailboxes(ctx, f.pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	var inbox store.Mailbox
	for _, mb := range mailboxes {
		if mb.Name == "INBOX" {
			inbox = mb
		}
	}
	if inbox.ID == 0 {
		t.Fatalf("fixture INBOX not found")
	}

	due := f.clk.Now().Add(-time.Minute)
	wake := inbox.ID
	f.insertSnoozedMessage(t, sent.ID, due, &wake, "wake-differs")

	before := len(f.gateway.Calls())
	f.runSnoozeWorkerOnce(t, 1)
	if _, err := f.disp.tick(ctx); err != nil {
		t.Fatalf("dispatcher tick: %v", err)
	}
	after := len(f.gateway.Calls())
	if after <= before {
		t.Fatalf("expected a new push POST after destination-differs wake; before=%d after=%d", before, after)
	}
}

// TestDispatcher_SnoozeWake_DestinationEqualsOrigin_Observed drives the
// wake-in-place case (explicit wake destination == the message's
// current mailbox, so the worker never calls AddMessageToMailbox and
// only SetSnooze's ChangeOpUpdated is appended) through the same
// webpush payload/rules path. Observed result: a push POST fires here
// too. Neither buildEmailPayload (payload.go) nor classifyEvent /
// Evaluate (rules.go) discriminate on StateChange.Op — buildEmailPayload
// accepts Created and Updated alike, and Evaluate never inspects Op —
// so an Updated-only Email change reaches a push subscriber exactly
// like a Created one. This test pins that observed behaviour so a
// future change that starts filtering on Op is caught here rather than
// silently reintroducing the "wake in place: no reminder" gap.
func TestDispatcher_SnoozeWake_DestinationEqualsOrigin_Observed(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)
	ctx := context.Background()

	mailboxes, err := f.store.Meta().ListMailboxes(ctx, f.pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	var inbox store.Mailbox
	for _, mb := range mailboxes {
		if mb.Name == "INBOX" {
			inbox = mb
		}
	}
	if inbox.ID == 0 {
		t.Fatalf("fixture INBOX not found")
	}

	due := f.clk.Now().Add(-time.Minute)
	wake := inbox.ID // == origin: wake in place
	f.insertSnoozedMessage(t, inbox.ID, due, &wake, "wake-in-place")

	before := len(f.gateway.Calls())
	f.runSnoozeWorkerOnce(t, 1)
	if _, err := f.disp.tick(ctx); err != nil {
		t.Fatalf("dispatcher tick: %v", err)
	}
	after := len(f.gateway.Calls())
	if after <= before {
		t.Fatalf("wake-in-place (Updated-only) produced no push POST; before=%d after=%d", before, after)
	}
}
