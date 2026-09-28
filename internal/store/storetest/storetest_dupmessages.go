package storetest

// storetest_dupmessages.go covers Metadata.ListDuplicateMessageIDs, the
// read side of the `herold diag duplicate-messages list`/`merge` recovery
// path (re #496): SMTP delivery and the IMAP/JMAP importers each dedup
// independently, so a message that reaches a principal by two ingest paths
// whose stored bytes differ can still end up as two live rows sharing one
// Message-ID.

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

func testListDuplicateMessageIDs(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "alice@dup.test")
	bob := mustInsertPrincipal(t, s, "bob@dup.test")
	inboxAlice := mustInsertMailbox(t, s, alice.ID, "INBOX")
	inboxBob := mustInsertMailbox(t, s, bob.ID, "INBOX")

	insert := func(pid store.PrincipalID, mb store.MailboxID, msgID, body string, when time.Time) store.MessageID {
		ref := putBlob(t, s, body)
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:  pid,
			Blob:         ref,
			Size:         ref.Size,
			InternalDate: when,
			ReceivedAt:   when,
			Envelope:     store.Envelope{MessageID: msgID},
		}, []store.MessageMailbox{{MailboxID: mb}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		msgs, err := s.Meta().ListMessages(ctx, mb, store.MessageFilter{Limit: 1000})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		return msgs[len(msgs)-1].ID
	}

	t0 := time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC)
	t1 := t0.Add(23 * time.Second)

	// Alice: a genuine duplicate pair sharing one Message-ID, oldest
	// (mirror-style) first.
	olderID := insert(alice.ID, inboxAlice.ID, "dup-1@example.test", "body one (older)", t0)
	newerID := insert(alice.ID, inboxAlice.ID, "dup-1@example.test", "body one (newer, different content)", t1)

	// Alice: a lone Message-ID (not a duplicate) must not appear.
	insert(alice.ID, inboxAlice.ID, "solo@example.test", "solo body", t0)

	// Alice: a message with no Message-ID at all must never group with
	// anything, including another message with no Message-ID.
	insert(alice.ID, inboxAlice.ID, "", "no-msgid body A", t0)
	insert(alice.ID, inboxAlice.ID, "", "no-msgid body B", t0)

	// Bob: an unrelated principal sharing the SAME Message-ID text as
	// Alice's pair must not be pulled into Alice's group (scoped per
	// principal) and must not appear in Bob's own list either (only one
	// row for Bob).
	insert(bob.ID, inboxBob.ID, "dup-1@example.test", "bob's single copy", t0)

	groups, err := s.Meta().ListDuplicateMessageIDs(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListDuplicateMessageIDs: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("ListDuplicateMessageIDs(alice) = %d groups, want 1: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.MessageID != "dup-1@example.test" {
		t.Fatalf("group MessageID = %q, want %q", g.MessageID, "dup-1@example.test")
	}
	if len(g.Messages) != 2 {
		t.Fatalf("group Messages = %v, want 2 entries", g.Messages)
	}
	if g.Messages[0] != olderID || g.Messages[1] != newerID {
		t.Fatalf("group Messages = %v, want [%d, %d] (oldest first)", g.Messages, olderID, newerID)
	}

	bobGroups, err := s.Meta().ListDuplicateMessageIDs(ctx, bob.ID)
	if err != nil {
		t.Fatalf("ListDuplicateMessageIDs(bob): %v", err)
	}
	if len(bobGroups) != 0 {
		t.Fatalf("ListDuplicateMessageIDs(bob) = %+v, want no groups (only one copy)", bobGroups)
	}
}
