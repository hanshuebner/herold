package emailsubmission

// sentcopy_seen_test.go verifies the acceptance the herold/herold issue
// #316 ticket names for the native submission path: a message the
// principal sends is stored as already read. The suite's compose flow
// (web/apps/suite/src/lib/compose/compose.svelte.ts) drives this via the
// standard RFC 8621 SS7.5 onSuccessUpdateEmail patch on EmailSubmission/set
// -- move Drafts->Sent, clear $draft, set $seen -- so this test drives the
// same patch through setHandler.executeAs and asserts the resulting Sent
// copy carries $seen.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// testEmailSubmissionSentCopySeen is the backend-agnostic body of
// TestEmailSubmission_Set_SentCopySeen_*.
func testEmailSubmissionSentCopySeen(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	h, p, draftsMB, _ := newSetupFromStore(t, st)

	sentMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Sent", Attributes: store.MailboxAttrSent,
	})
	if err != nil {
		t.Fatalf("InsertMailbox Sent: %v", err)
	}

	// A draft, unseen and carrying $draft -- the state compose.svelte.ts's
	// autosave leaves a reply/new-message draft in before Send.
	draftBody := "From: alice@example.test\r\nTo: bob@example.test\r\n" +
		"Subject: Hello\r\n\r\nhi\r\n"
	draftRef, err := st.Blobs().Put(ctx, strings.NewReader(draftBody))
	if err != nil {
		t.Fatalf("Blobs.Put draft: %v", err)
	}
	draftUID, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Blob:        draftRef,
		Size:        draftRef.Size,
		Envelope: store.Envelope{
			Subject: "Hello",
			From:    "alice@example.test",
			To:      "bob@example.test",
		},
	}, []store.MessageMailbox{{MailboxID: draftsMB.ID, Flags: store.MessageFlagDraft}})
	if err != nil {
		t.Fatalf("InsertMessage draft: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, draftsMB.ID, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var draftMsgID store.MessageID
	for _, m := range msgs {
		if m.UID == draftUID {
			draftMsgID = m.ID
		}
	}
	if draftMsgID == 0 {
		t.Fatalf("draft message ID not found")
	}

	// Precondition: unseen.
	before, err := st.Meta().GetMessage(ctx, draftMsgID)
	if err != nil {
		t.Fatalf("GetMessage (precondition): %v", err)
	}
	if before.Flags&store.MessageFlagSeen != 0 {
		t.Fatal("precondition: draft should start unseen")
	}

	// Submit, with the same onSuccessUpdateEmail patch compose.svelte.ts
	// sends on Send: mailboxIds Drafts->null, Sent->true; keywords $draft
	// ->null, $seen->true.
	args, marshalErr := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(draftMsgID),
			},
		},
		"onSuccessUpdateEmail": map[string]any{
			"#k1": map[string]any{
				fmt.Sprintf("mailboxIds/%d", draftsMB.ID): nil,
				fmt.Sprintf("mailboxIds/%d", sentMB.ID):   true,
				"keywords/$draft":                         nil,
				"keywords/$seen":                          true,
			},
		},
	})
	if marshalErr != nil {
		t.Fatalf("marshal args: %v", marshalErr)
	}
	_, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}

	after, err := st.Meta().GetMessage(ctx, draftMsgID)
	if err != nil {
		t.Fatalf("GetMessage (after): %v", err)
	}
	if after.MailboxID != sentMB.ID {
		t.Errorf("message mailbox = %d, want Sent (%d)", after.MailboxID, sentMB.ID)
	}
	if after.Flags&store.MessageFlagDraft != 0 {
		t.Error("Sent copy still carries $draft")
	}
	if after.Flags&store.MessageFlagSeen == 0 {
		t.Error("Sent copy is not $seen (re #316): a message the principal sends must be stored as already read")
	}
}

// TestEmailSubmission_Set_SentCopySeen_SQLite runs the acceptance test
// against the SQLite backend.
func TestEmailSubmission_Set_SentCopySeen_SQLite(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testEmailSubmissionSentCopySeen(t, st)
}

// TestEmailSubmission_Set_SentCopySeen_Postgres runs the same test against
// the Postgres backend. Skips when HEROLD_PG_DSN is not set.
func TestEmailSubmission_Set_SentCopySeen_Postgres(t *testing.T) {
	st := openPostgresStore(t)
	testEmailSubmissionSentCopySeen(t, st)
}
