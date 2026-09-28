package email_test

// import_dedup_test.go covers re #496 on the JMAP Email/import path: like
// SMTP delivery (internal/protosmtp/deliver_dedup_msgid_test.go),
// Email/import inserted unconditionally, so importing a blob under a
// Message-ID the IMAP mirror already holds for the same principal created a
// second live row instead of folding onto the existing one.
//
// The mirror row and the imported blob carry genuinely different bytes
// (distinct bodies), pinning that the dedup guard matches by normalised
// Message-ID header alone -- mirroring imapimport/sync.go's ingestMessage
// (REQ-IMAP-IMP-30) -- rather than requiring the imported blob to be an
// exact match of the existing row's stored content.
//
// TestEmail_Import_DedupNoMessageID_BlobHashFallback_* covers work item
// 4's third scenario: a blob with no Message-ID header at all falls back
// to matching by blob hash (REQ-IMAP-IMP-30's own no-Message-ID
// fallback).
//
// TestEmail_Import_DedupMessageID_CrossPrincipal_* pins the dedup
// lookup's principal scope: a different principal already holding a
// message under the same Message-ID must never cause this principal's
// own import to fold onto that other row.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

func TestEmail_Import_DedupMessageID_MirrorFirst_SQLite(t *testing.T) {
	testEmailImportDedupMirrorFirst(t, setupFixture)
}

func TestEmail_Import_DedupMessageID_MirrorFirst_Postgres(t *testing.T) {
	testEmailImportDedupMirrorFirst(t, setupFixturePostgres)
}

func testEmailImportDedupMirrorFirst(t *testing.T, setup func(t *testing.T) *fixture) {
	f := setup(t)
	ctx := context.Background()

	mirrorBody := "Received: from mail.classic-computing.de (mail.classic-computing.de [198.51.100.7])\r\n" +
		"\tby mx.classic-computing.de with ESMTPS id abc123; Sat, 26 Sep 2026 18:00:07 +0000\r\n" +
		"From: a@example.test\r\nTo: b@example.test\r\n" +
		"Message-ID: <mirror-import-496@example.test>\r\n" +
		"Subject: dedup probe\r\n\r\nBody text.\r\n"
	mirrorRef := f.putBlob(t, mirrorBody)

	// Seed the "mirror" copy: an IMAP-import-style row carrying the
	// upstream account's own raw bytes -- its own Received: chain --
	// unread in Inbox, exactly like thread 3994's imap-import half (re
	// #496).
	now := f.srv.Clock.Now()
	mirror := store.Message{
		PrincipalID:  f.pid,
		Size:         mirrorRef.Size,
		Blob:         mirrorRef,
		InternalDate: now,
		ReceivedAt:   now,
		Envelope: store.Envelope{
			Subject:   "dedup probe",
			From:      "a@example.test",
			To:        "b@example.test",
			MessageID: "mirror-import-496@example.test",
		},
		IngestSource:    store.IngestSourceIMAPImport,
		IngestSourceRef: "classic-computing.de",
	}
	if _, _, err := f.srv.Store.Meta().InsertMessage(ctx, mirror, []store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage (mirror): %v", err)
	}
	mirrorID := mostRecentMessageID(t, f)

	// A client imports a blob with genuinely different bytes (no Received:
	// chain at all) but the same Message-ID -- exactly like a second,
	// independent ingest of mail the mirror already holds arriving with
	// its own transport trace.
	importedBody := "From: a@example.test\r\nTo: b@example.test\r\n" +
		"Message-ID: <mirror-import-496@example.test>\r\n" +
		"Subject: dedup probe\r\n\r\nBody text.\r\n"
	importedRef := f.putBlob(t, importedBody)
	if importedRef.Hash == mirrorRef.Hash {
		t.Fatalf("test fixture bug: mirror and imported blobs must differ")
	}
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"new1": map[string]any{
				"blobId":     importedRef.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	mid, _ := resp.Created["new1"]["id"].(string)
	if mid != fmt.Sprintf("%d", mirrorID) {
		t.Fatalf("Email/import returned id %q, want the mirror row's id %d (dedup should fold onto it)", mid, mirrorID)
	}

	msgs, err := f.srv.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("after mirror+Email/import redelivery: want 1 row in INBOX, got %d", len(msgs))
	}
	stored, err := f.srv.Store.Meta().GetMessage(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.IngestSource != store.IngestSourceIMAPImport {
		t.Fatalf("dedup inserted a fresh row instead of folding onto the mirror row: IngestSource = %q, want %q",
			stored.IngestSource, store.IngestSourceIMAPImport)
	}
}

func TestEmail_Import_DedupNoMessageID_BlobHashFallback_SQLite(t *testing.T) {
	testEmailImportDedupNoMessageIDBlobHashFallback(t, setupFixture)
}

func TestEmail_Import_DedupNoMessageID_BlobHashFallback_Postgres(t *testing.T) {
	testEmailImportDedupNoMessageIDBlobHashFallback(t, setupFixturePostgres)
}

// testEmailImportDedupNoMessageIDBlobHashFallback covers the no-Message-ID
// branch of importOne's dedup: without a Message-ID header to key on, the
// lookup falls back to matching the stored blob hash, exactly as
// imapimport/sync.go's ingestMessage does for the same case
// (REQ-IMAP-IMP-30).
func testEmailImportDedupNoMessageIDBlobHashFallback(t *testing.T, setup func(t *testing.T) *fixture) {
	f := setup(t)
	ctx := context.Background()

	// Deliberately no Message-ID header.
	body := "From: a@example.test\r\nTo: b@example.test\r\n" +
		"Subject: no message-id dedup probe\r\n\r\nBody text.\r\n"
	ref := f.putBlob(t, body)

	// Seed a pre-existing row carrying this exact no-Message-ID content,
	// unread in Inbox -- e.g. an IMAP-mirrored message whose source never
	// set a Message-ID header.
	now := f.srv.Clock.Now()
	existing := store.Message{
		PrincipalID:  f.pid,
		Size:         ref.Size,
		Blob:         ref,
		InternalDate: now,
		ReceivedAt:   now,
		Envelope: store.Envelope{
			Subject: "no message-id dedup probe",
			From:    "a@example.test",
			To:      "b@example.test",
		},
		IngestSource:    store.IngestSourceIMAPImport,
		IngestSourceRef: "classic-computing.de",
	}
	if _, _, err := f.srv.Store.Meta().InsertMessage(ctx, existing, []store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage (existing): %v", err)
	}
	existingID := mostRecentMessageID(t, f)

	// A client imports the exact same bytes (also with no Message-ID) --
	// must fold onto the existing row via the blob-hash fallback rather
	// than insert a second one.
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"new1": map[string]any{
				"blobId":     ref.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	mid, _ := resp.Created["new1"]["id"].(string)
	if mid != fmt.Sprintf("%d", existingID) {
		t.Fatalf("Email/import returned id %q, want the existing row's id %d (blob-hash fallback should fold onto it)", mid, existingID)
	}

	msgs, err := f.srv.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("after re-importing a no-Message-ID blob with identical content: want 1 row in INBOX, got %d", len(msgs))
	}
	stored, err := f.srv.Store.Meta().GetMessage(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.IngestSource != store.IngestSourceIMAPImport {
		t.Fatalf("dedup inserted a fresh row instead of folding onto the existing row: IngestSource = %q, want %q",
			stored.IngestSource, store.IngestSourceIMAPImport)
	}
}

func TestEmail_Import_DedupMessageID_CrossPrincipal_SQLite(t *testing.T) {
	testEmailImportDedupCrossPrincipal(t, setupFixture)
}

func TestEmail_Import_DedupMessageID_CrossPrincipal_Postgres(t *testing.T) {
	testEmailImportDedupCrossPrincipal(t, setupFixturePostgres)
}

// testEmailImportDedupCrossPrincipal pins the dedup lookup's principal
// scope: GetMessageByMessageIDHeader/GetMessageByBlobHash are both
// (principalID, key) lookups, so a different principal already holding a
// message under this Message-ID must never cause this principal's own
// import to fold onto that other row.
func testEmailImportDedupCrossPrincipal(t *testing.T, setup func(t *testing.T) *fixture) {
	f := setup(t)
	ctx := context.Background()

	otherEmail := fmt.Sprintf("other-%d@example.test", time.Now().UnixNano())
	other, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: otherEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal (other): %v", err)
	}
	otherInbox, err := f.srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: other.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (other): %v", err)
	}
	const sharedMsgID = "cross-principal-import-496@example.test"
	otherRef := f.putBlob(t, "From: x@example.test\r\nTo: "+otherEmail+"\r\n"+
		"Message-ID: <"+sharedMsgID+">\r\nSubject: other principal's copy\r\n\r\nBody.\r\n")
	if _, _, err := f.srv.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  other.ID,
		Blob:         otherRef,
		Size:         otherRef.Size,
		InternalDate: f.srv.Clock.Now(),
		ReceivedAt:   f.srv.Clock.Now(),
		Envelope: store.Envelope{
			MessageID: sharedMsgID,
			Subject:   "other principal's copy",
		},
		IngestSource: store.IngestSourceIMAPImport,
	}, []store.MessageMailbox{{MailboxID: otherInbox.ID}}); err != nil {
		t.Fatalf("InsertMessage (other): %v", err)
	}
	otherMsg, err := f.srv.Store.Meta().GetMessageByMessageIDHeader(ctx, other.ID, sharedMsgID)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (other, before): %v", err)
	}

	// f.pid imports a blob under the SAME Message-ID: must create its own
	// row for f.pid, never fold onto the other principal's row.
	myRef := f.putBlob(t, "From: a@example.test\r\nTo: b@example.test\r\n"+
		"Message-ID: <"+sharedMsgID+">\r\nSubject: my own copy\r\n\r\nBody.\r\n")
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"new1": map[string]any{
				"blobId":     myRef.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	mid, _ := resp.Created["new1"]["id"].(string)
	midU, err := strconv.ParseUint(mid, 10, 64)
	if err != nil {
		t.Fatalf("parse created id %q: %v", mid, err)
	}
	if store.MessageID(midU) == otherMsg.ID {
		t.Fatalf("cross-principal dedup incorrectly folded onto the other principal's row (id=%d)", otherMsg.ID)
	}
	mine, err := f.srv.Store.Meta().GetMessage(ctx, store.MessageID(midU))
	if err != nil {
		t.Fatalf("GetMessage (mine): %v", err)
	}
	if mine.PrincipalID != f.pid {
		t.Fatalf("imported row belongs to principal %d, want %d (f.pid)", mine.PrincipalID, f.pid)
	}
	if mine.IngestSource != store.IngestSourceJMAPImport {
		t.Fatalf("IngestSource = %q, want %q (a fresh insert, not a fold onto another principal's row)",
			mine.IngestSource, store.IngestSourceJMAPImport)
	}

	// The other principal's row must be untouched: still exactly one
	// membership, still IngestSourceIMAPImport.
	otherAfter, err := f.srv.Store.Meta().GetMessage(ctx, otherMsg.ID)
	if err != nil {
		t.Fatalf("GetMessage (other, after): %v", err)
	}
	if len(otherAfter.Mailboxes) != 1 {
		t.Fatalf("other principal's row gained/lost memberships: %+v", otherAfter.Mailboxes)
	}
	if otherAfter.IngestSource != store.IngestSourceIMAPImport {
		t.Fatalf("other principal's row IngestSource changed to %q", otherAfter.IngestSource)
	}
}
