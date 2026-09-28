package protosmtp_test

// deliver_dedup_msgid_test.go covers re #496: SMTP delivery inserted a
// message unconditionally, so a message the IMAP mirror already imported
// for the same principal under the same Message-ID became a second live
// row in the same thread the moment the same mail also arrived by SMTP
// (production thread 3994, pairs 4001/4002 and 4016/4017).
//
// TestDelivery_DedupMessageID_MirrorFirst_* reproduces the mirror-then-SMTP
// ordering with two copies whose trace headers genuinely differ, as on
// production: the seeded "mirror" row carries the upstream account's own
// Received: chain (no herold stamp), and the SMTP delivery carries herold's
// own Received:/Authentication-Results: prefix -- their stored bytes never
// match. The dedup guard matches by normalised Message-ID header alone
// (mirroring imapimport/sync.go's ingestMessage, REQ-IMAP-IMP-30), so the
// SMTP delivery still folds onto the mirror row instead of creating a
// second one.
//
// TestDelivery_LLMRecord_DuplicateMessageID (deliver_llm_record_msgid_test.go)
// covers the companion "genuine collision" case: a sender reusing a
// Message-ID for an unrelated message now also folds (matching the
// importer's own acceptance of that rare case), landing in both the
// original and the redelivery's resolved mailboxes.
//
// TestDelivery_DedupNoMessageID_BlobHashFallback_* covers work item 4's
// third scenario: a message with no Message-ID header at all falls back
// to the blob-hash dedup (REQ-IMAP-IMP-30's own no-Message-ID fallback),
// folding a byte-identical redelivery onto the existing row.
//
// TestDelivery_DedupMessageID_CrossPrincipal_TwoRows_* pins the dedup
// lookup's principal scope: the same Message-ID delivered to two
// different principals must never fold across accounts -- each principal
// gets its own row.

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_DedupMessageID_MirrorFirst_SQLite(t *testing.T) {
	testDeliveryDedupMirrorFirst(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_DedupMessageID_MirrorFirst_Postgres(t *testing.T) {
	testDeliveryDedupMirrorFirst(t, newPGStoreFactory)
}

func testDeliveryDedupMirrorFirst(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	ctx := context.Background()

	mb, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	label, err := f.ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.principal,
		Name:        "classic-computing.de",
	})
	if err != nil {
		t.Fatalf("InsertMailbox (provenance label): %v", err)
	}

	// Seed the "mirror" copy: an IMAP-import-style row carrying the
	// upstream account's own raw bytes -- its own Received: chain, never
	// touched by herold -- unread, in Inbox and the account's provenance
	// label, exactly like thread 3994's imap-import half (re #496).
	mirrorRaw := "Received: from mail.classic-computing.de (mail.classic-computing.de [198.51.100.7])\r\n" +
		"\tby mx.classic-computing.de with ESMTPS id abc123; Sat, 26 Sep 2026 18:00:07 +0000\r\n" +
		"From: bob@sender.test\r\nTo: alice@example.test\r\n" +
		"Message-ID: <mirror-dedup-1@sender.test>\r\n" +
		"Subject: dedup probe\r\n\r\nBody text.\r\n"
	blobRef, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(mirrorRaw))
	if err != nil {
		t.Fatalf("Blobs.Put (mirror): %v", err)
	}
	mirrorMsg := store.Message{
		PrincipalID:  f.principal,
		Size:         blobRef.Size,
		Blob:         blobRef,
		InternalDate: f.ha.Clock.Now(),
		ReceivedAt:   f.ha.Clock.Now(),
		Envelope: store.Envelope{
			Subject:   "dedup probe",
			From:      "bob@sender.test",
			To:        "alice@example.test",
			MessageID: "mirror-dedup-1@sender.test",
		},
		IngestSource:    store.IngestSourceIMAPImport,
		IngestSourceRef: "classic-computing.de",
	}
	mirrorUID, _, err := f.ha.Store.Meta().InsertMessage(ctx, mirrorMsg, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage (mirror): %v", err)
	}
	mirrorID, err := f.ha.Store.Meta().GetMessageIDByMailboxUID(ctx, mb.ID, mirrorUID)
	if err != nil {
		t.Fatalf("GetMessageIDByMailboxUID: %v", err)
	}
	if _, _, err := f.ha.Store.Meta().AddMessageToMailbox(ctx, mirrorID, label.ID); err != nil {
		t.Fatalf("AddMessageToMailbox (label): %v", err)
	}
	mirrorBefore, err := f.ha.Store.Meta().GetMessage(ctx, mirrorID)
	if err != nil {
		t.Fatalf("GetMessage (mirror, before SMTP): %v", err)
	}

	// Push-dispatcher invariant, half one: the mirror's own placement into
	// Inbox produced exactly one ChangeOpCreated entry for (message,
	// Inbox) -- the "one arrival" a genuinely new message produces.
	feedBefore, err := f.ha.Store.Meta().ReadChangeFeed(ctx, f.principal, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed (before SMTP): %v", err)
	}
	var maxSeqBefore store.ChangeSeq
	arrivalsIntoInbox := 0
	for _, e := range feedBefore {
		if e.Seq > maxSeqBefore {
			maxSeqBefore = e.Seq
		}
		if e.Kind == store.EntityKindEmail && e.EntityID == uint64(mirrorID) &&
			e.ParentEntityID == uint64(mb.ID) && e.Op == store.ChangeOpCreated {
			arrivalsIntoInbox++
		}
	}
	if arrivalsIntoInbox != 1 {
		t.Fatalf("ChangeOpCreated(message=%d, mailbox=Inbox) count before SMTP delivery = %d, want 1", mirrorID, arrivalsIntoInbox)
	}

	// The SMTP half of the pair arrives seconds later, carrying herold's
	// own Received:/Authentication-Results: prefix -- genuinely different
	// bytes from the mirror's copy, exactly like production's 4001/4002
	// and 4016/4017 pairs.
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\n" +
		"Message-ID: <mirror-dedup-1@sender.test>\r\n" +
		"Subject: dedup probe\r\n\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	finalMsgs, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages (final): %v", err)
	}
	if len(finalMsgs) != 1 {
		t.Fatalf("after mirror+SMTP redelivery: want 1 row in INBOX, got %d", len(finalMsgs))
	}
	final, err := f.ha.Store.Meta().GetMessage(ctx, finalMsgs[0].ID)
	if err != nil {
		t.Fatalf("GetMessage(final): %v", err)
	}
	if final.ID != mirrorID {
		t.Fatalf("dedup created a new row (id=%d) instead of folding onto the mirror row (id=%d)", final.ID, mirrorID)
	}
	if final.IngestSource != store.IngestSourceIMAPImport {
		t.Fatalf("dedup fold rewrote IngestSource = %q, want %q (the existing row's provenance)",
			final.IngestSource, store.IngestSourceIMAPImport)
	}
	if final.Flags&store.MessageFlagSeen != 0 {
		t.Fatalf("dedup fold must keep the existing row's seen state (neither copy was seen); Flags = %v", final.Flags)
	}
	if final.ThreadID != mirrorBefore.ThreadID {
		t.Fatalf("dedup fold changed the existing row's thread: ThreadID = %d, want %d", final.ThreadID, mirrorBefore.ThreadID)
	}
	foundLabel := false
	for _, mm := range final.Mailboxes {
		if mm.MailboxID == label.ID {
			foundLabel = true
		}
	}
	if !foundLabel {
		t.Fatalf("dedup fold dropped the mirror's provenance-label membership: mailboxes = %+v", final.Mailboxes)
	}

	// Push-dispatcher invariant, half two: the redelivery only touches a
	// mailbox (Inbox) the row already belonged to, so it must produce no
	// additional ChangeOpCreated entry there -- the push dispatcher
	// (internal/webpush) denies any Email event whose Op is not
	// ChangeOpCreated (REQ-PUSH-81, rules.go ReasonDroppedNotArrival), so
	// a second ChangeOpCreated here would read as a second "new mail"
	// arrival and double-notify the principal for one redelivered
	// message.
	feedAfter, err := f.ha.Store.Meta().ReadChangeFeed(ctx, f.principal, maxSeqBefore, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed (after SMTP): %v", err)
	}
	for _, e := range feedAfter {
		if e.Kind == store.EntityKindEmail && e.EntityID == uint64(mirrorID) &&
			e.ParentEntityID == uint64(mb.ID) && e.Op == store.ChangeOpCreated {
			t.Fatalf("redelivery produced a second ChangeOpCreated(message=%d, mailbox=Inbox) entry -- the push dispatcher would send a duplicate new-mail notification", mirrorID)
		}
	}
}

func TestDelivery_DedupNoMessageID_BlobHashFallback_SQLite(t *testing.T) {
	testDeliveryDedupNoMessageIDBlobHashFallback(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_DedupNoMessageID_BlobHashFallback_Postgres(t *testing.T) {
	testDeliveryDedupNoMessageIDBlobHashFallback(t, newPGStoreFactory)
}

// testDeliveryDedupNoMessageIDBlobHashFallback covers the no-Message-ID
// branch of dedupOntoExistingMessage: without a Message-ID header to key
// on, the lookup falls back to matching the stored blob hash, exactly as
// imapimport/sync.go's ingestMessage does for the same case
// (REQ-IMAP-IMP-30) -- without it a no-Message-ID message redelivered
// with byte-identical content would insert a fresh row every time.
func testDeliveryDedupNoMessageIDBlobHashFallback(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	ctx := context.Background()

	mb, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}

	// Deliberately no Message-ID header.
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: no message-id dedup probe\r\n\r\nBody text.\r\n.\r\n"

	deliver := func(t *testing.T) {
		t.Helper()
		cli, closeFn := f.dial(t)
		defer closeFn()
		mustOK(t, cli, 220)
		cli.send(t, "EHLO client.example.test")
		mustOK(t, cli, 250)
		cli.send(t, "MAIL FROM:<bob@sender.test>")
		mustOK(t, cli, 250)
		cli.send(t, "RCPT TO:<alice@example.test>")
		mustOK(t, cli, 250)
		cli.send(t, "DATA")
		mustOK(t, cli, 354)
		cli.sendRaw(t, []byte(body))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	}

	// Probe delivery: learn the exact bytes this pipeline stores for this
	// content -- the Received/Authentication-Results prefix it stamps
	// depends on session/clock state this test does not otherwise
	// control.
	deliver(t)
	probes, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages (probe): %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probe delivery: want 1 message, got %d", len(probes))
	}
	probe := probes[0]
	if probe.Envelope.MessageID != "" {
		t.Fatalf("test fixture bug: probe message unexpectedly carries a Message-ID %q", probe.Envelope.MessageID)
	}
	rdr, err := f.ha.Store.Blobs().Get(ctx, probe.Blob.Hash)
	if err != nil {
		t.Fatalf("Blobs().Get(probe): %v", err)
	}
	raw, err := io.ReadAll(rdr)
	rdr.Close()
	if err != nil {
		t.Fatalf("read probe blob: %v", err)
	}

	// Roll back the probe: remove its only membership, which deletes the
	// row (and its blob refcount) entirely, so the pre-existing row
	// seeded below is the only prior copy.
	if err := f.ha.Store.Meta().RemoveMessageFromMailbox(ctx, probe.ID, mb.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox (probe): %v", err)
	}

	// Seed a pre-existing row carrying this exact no-Message-ID content,
	// unread in Inbox -- e.g. an IMAP-mirrored message whose source never
	// set a Message-ID header.
	blobRef, err := f.ha.Store.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Blobs.Put (existing): %v", err)
	}
	existingMsg := store.Message{
		PrincipalID:     f.principal,
		Size:            blobRef.Size,
		Blob:            blobRef,
		InternalDate:    f.ha.Clock.Now(),
		ReceivedAt:      f.ha.Clock.Now(),
		Envelope:        probe.Envelope,
		IngestSource:    store.IngestSourceIMAPImport,
		IngestSourceRef: "classic-computing.de",
	}
	if _, _, err := f.ha.Store.Meta().InsertMessage(ctx, existingMsg, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage (existing): %v", err)
	}
	existingRows, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages (after seeding existing): %v", err)
	}
	if len(existingRows) != 1 {
		t.Fatalf("after seeding the existing row: want 1 message in INBOX, got %d", len(existingRows))
	}
	existingID := existingRows[0].ID

	// The SMTP redelivery arrives with byte-identical content (the fake
	// clock held by the harness is not advanced, and the connection
	// parameters are unchanged).
	deliver(t)

	finalMsgs, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages (final): %v", err)
	}
	if len(finalMsgs) != 1 {
		t.Fatalf("after redelivering a no-Message-ID message with identical content: want 1 row in INBOX, got %d", len(finalMsgs))
	}
	if finalMsgs[0].ID != existingID {
		t.Fatalf("dedup created a new row (id=%d) instead of folding onto the existing no-Message-ID row (id=%d) via the blob-hash fallback",
			finalMsgs[0].ID, existingID)
	}
	final, err := f.ha.Store.Meta().GetMessage(ctx, finalMsgs[0].ID)
	if err != nil {
		t.Fatalf("GetMessage(final): %v", err)
	}
	if final.IngestSource != store.IngestSourceIMAPImport {
		t.Fatalf("dedup fold rewrote IngestSource = %q, want %q (the existing row's provenance)",
			final.IngestSource, store.IngestSourceIMAPImport)
	}
}

func TestDelivery_DedupMessageID_CrossPrincipal_TwoRows_SQLite(t *testing.T) {
	testDeliveryDedupCrossPrincipalTwoRows(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_DedupMessageID_CrossPrincipal_TwoRows_Postgres(t *testing.T) {
	testDeliveryDedupCrossPrincipalTwoRows(t, newPGStoreFactory)
}

// testDeliveryDedupCrossPrincipalTwoRows pins the dedup lookup's
// principal scope: GetMessageByMessageIDHeader/GetMessageByBlobHash are
// both (principalID, key) lookups, so the same Message-ID delivered to
// two different principals must never fold across accounts -- each
// principal's delivery is independent and gets its own row.
func testDeliveryDedupCrossPrincipalTwoRows(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	ctx := context.Background()

	dir := directory.New(f.ha.Store.Meta(), f.ha.Logger, f.ha.Clock, rand.Reader)
	if _, err := dir.CreatePrincipal(ctx, "carol@example.test", "correct-horse-staple-battery"); err != nil {
		t.Fatalf("CreatePrincipal(carol): %v", err)
	}

	deliverTo := func(t *testing.T, rcpt string) {
		t.Helper()
		cli, closeFn := f.dial(t)
		defer closeFn()
		mustOK(t, cli, 220)
		cli.send(t, "EHLO client.example.test")
		mustOK(t, cli, 250)
		cli.send(t, "MAIL FROM:<bob@sender.test>")
		mustOK(t, cli, 250)
		cli.send(t, fmt.Sprintf("RCPT TO:<%s>", rcpt))
		mustOK(t, cli, 250)
		cli.send(t, "DATA")
		mustOK(t, cli, 354)
		body := fmt.Sprintf("From: bob@sender.test\r\nTo: %s\r\n"+
			"Message-ID: <cross-principal-496@sender.test>\r\n"+
			"Subject: cross principal probe\r\n\r\nBody text.\r\n.\r\n", rcpt)
		cli.sendRaw(t, []byte(body))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	}

	deliverTo(t, "alice@example.test")
	deliverTo(t, "carol@example.test")

	hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: "cross-principal-496@sender.test", Limit: 10})
	if err != nil {
		t.Fatalf("SearchAdminMessages: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("SearchAdminMessages(cross-principal-496) hits = %d, want 2 (one row per principal, no cross-principal fold)", len(hits))
	}
	principals := map[store.PrincipalID]bool{}
	for _, h := range hits {
		principals[h.PrincipalID] = true
	}
	if len(principals) != 2 {
		t.Fatalf("hits span %d distinct principal(s), want 2: %+v", len(principals), hits)
	}
}
