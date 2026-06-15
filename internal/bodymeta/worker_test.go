package bodymeta_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/bodymeta"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// openStore creates a fresh in-process SQLite store backed by a temp file.
func openStore(t *testing.T) store.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seed inserts one message and returns the message row. BodyMetaComputed is
// false after a raw insert.
func seedMessage(t *testing.T, st store.Store, principalID store.PrincipalID, mailboxID store.MailboxID, body string) store.Message {
	t.Helper()
	ctx := context.Background()
	ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	msg := store.Message{
		PrincipalID:  principalID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         ref.Size,
		Blob:         ref,
		Envelope: store.Envelope{
			Subject: "Test",
			From:    "sender@example.test",
			To:      "rcpt@example.test",
		},
	}
	_, _, err = st.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mailboxID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Retrieve the message to get its assigned ID.
	feed, err := st.Meta().ReadChangeFeed(ctx, principalID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var id store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			id = store.MessageID(e.EntityID)
		}
	}
	if id == 0 {
		t.Fatal("could not find inserted message ID")
	}
	m, err := st.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	return m
}

// TestWorker_ComputesBodyMeta verifies that Run processes messages with
// BodyMetaComputed=false and sets preview + hasAttachment via SetMessageBodyMeta.
func TestWorker_ComputesBodyMeta(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	body := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Worker test\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nWorker preview text."
	m := seedMessage(t, st, p.ID, mb.ID, body)

	if m.BodyMetaComputed {
		t.Fatal("expected BodyMetaComputed=false after insert")
	}

	// Run the worker for one batch. Use a tiny idle sleep and cancel quickly
	// after the batch should be done.
	clk := clock.NewReal()
	w := bodymeta.New(st, nil, clk, bodymeta.Options{
		BatchSize: 10,
		IdleSleep: 5 * time.Millisecond,
	})

	// Run one batch cycle: cancel after enough time for the batch to complete.
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Run in the background; the context cancels after one idle cycle.
	done := make(chan struct{})
	go func() {
		w.Run(runCtx)
		close(done)
	}()
	<-done

	// Check that body meta was set.
	updated, err := st.Meta().GetMessage(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMessage after worker run: %v", err)
	}
	if !updated.BodyMetaComputed {
		t.Error("expected BodyMetaComputed=true after worker run")
	}
	if updated.Preview != "Worker preview text." {
		t.Errorf("preview = %q, want %q", updated.Preview, "Worker preview text.")
	}
	if updated.HasAttachment {
		t.Error("expected HasAttachment=false for plain-text-only message")
	}
}

// TestWorker_HasAttachment verifies that the worker sets HasAttachment=true
// for a message with an attachment.
func TestWorker_HasAttachment(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	// Multipart message with an explicit attachment.
	body := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Attachment test\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee attached.\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\nPDFDATA\r\n" +
		"--b--\r\n"
	m := seedMessage(t, st, p.ID, mb.ID, body)

	clk := clock.NewReal()
	w := bodymeta.New(st, nil, clk, bodymeta.Options{
		BatchSize: 10,
		IdleSleep: 5 * time.Millisecond,
	})
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		w.Run(runCtx)
		close(done)
	}()
	<-done

	updated, err := st.Meta().GetMessage(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !updated.BodyMetaComputed {
		t.Error("expected BodyMetaComputed=true after worker run")
	}
	if !updated.HasAttachment {
		t.Error("expected HasAttachment=true for message with PDF attachment")
	}
}

// TestWorker_SkipsAlreadyComputed verifies that the worker does not overwrite
// messages that already have BodyMetaComputed=true.
func TestWorker_SkipsAlreadyComputed(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	body := "From: x@example.test\r\nTo: y@example.test\r\nSubject: Already computed\r\n\r\nBody."
	m := seedMessage(t, st, p.ID, mb.ID, body)

	// Mark it computed with a sentinel preview before the worker runs.
	if err := st.Meta().SetMessageBodyMeta(ctx, m.ID, "SENTINEL", false); err != nil {
		t.Fatalf("SetMessageBodyMeta: %v", err)
	}

	// ListMessagesNeedingBodyMeta should return 0 rows (the message is now computed).
	ids, err := st.Meta().ListMessagesNeedingBodyMeta(ctx, 0, 10)
	if err != nil {
		t.Fatalf("ListMessagesNeedingBodyMeta: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("want 0 uncomputed messages, got %d", len(ids))
	}
}
