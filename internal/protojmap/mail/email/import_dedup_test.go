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

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
