package orphanblobs_test

// orphanblobs_test.go covers the #487 recovery path: List finds a blob no
// live message references, reports its duplicate/thread-reference status,
// and Restore re-inserts it into Archive through the normal insert path
// (threaded, deduplicated, refused on a live Message-ID clash).
//
// Both tests run on sqlite always and postgres when HEROLD_PG_DSN is set
// (STANDARDS.md §8.6), since the recovery path is entirely store-driven.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/orphanblobs"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// backend names one store to run a test against.
type backend struct {
	name string
	open func(t *testing.T) store.Store
}

// backends returns sqlite always, plus postgres when HEROLD_PG_DSN is set
// (mirroring internal/imapimport/sync_test.go's ownSentDedupBackends
// pattern).
func backends(t *testing.T) []backend {
	t.Helper()
	out := []backend{{
		name: "sqlite",
		open: func(t *testing.T) store.Store {
			dir := t.TempDir()
			st, err := storesqlite.Open(context.Background(), filepath.Join(dir, "store.db"), nil, clock.NewReal())
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	}}
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		return out
	}
	out = append(out, backend{
		name: "postgres",
		open: func(t *testing.T) store.Store {
			dir := t.TempDir()
			st, err := storepg.Open(context.Background(), dsn, filepath.Join(dir, "blobs"), nil, clock.NewReal())
			if err != nil {
				t.Fatalf("storepg.Open: %v", err)
			}
			if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
				if err := tr.TruncateAll(context.Background()); err != nil {
					t.Fatalf("TruncateAll: %v", err)
				}
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	})
	return out
}

// insertMessage builds a minimal RFC 822 message, inserts it into mailboxID
// for pid, and returns its assigned MessageID and blob hash.
func insertMessage(t *testing.T, st store.Store, pid store.PrincipalID, mailboxID store.MailboxID, msgID, inReplyTo, subject, from string, date time.Time) (store.MessageID, string) {
	t.Helper()
	ctx := context.Background()
	var raw string
	raw = fmt.Sprintf("Message-ID: <%s>\r\n", msgID)
	if inReplyTo != "" {
		raw += fmt.Sprintf("In-Reply-To: <%s>\r\n", inReplyTo)
	}
	raw += fmt.Sprintf("Subject: %s\r\n", subject)
	raw += fmt.Sprintf("From: %s\r\n", from)
	raw += fmt.Sprintf("Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	raw += "\r\n body\r\n"

	blobRef, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	storeMsg := store.Message{
		PrincipalID:  pid,
		Size:         int64(len(raw)),
		Blob:         blobRef,
		InternalDate: date,
		ReceivedAt:   date,
		Envelope: store.Envelope{
			Subject:    subject,
			From:       from,
			MessageID:  "<" + msgID + ">",
			InReplyTo:  inReplyToEnvelope(inReplyTo),
			References: inReplyToEnvelope(inReplyTo),
			Date:       date,
		},
	}
	uid, _, err := st.Meta().InsertMessage(ctx, storeMsg, []store.MessageMailbox{{MailboxID: mailboxID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	id, err := st.Meta().GetMessageIDByMailboxUID(ctx, mailboxID, uid)
	if err != nil {
		t.Fatalf("GetMessageIDByMailboxUID: %v", err)
	}
	return id, blobRef.Hash
}

func inReplyToEnvelope(id string) string {
	if id == "" {
		return ""
	}
	return "<" + id + ">"
}

func setUpPrincipal(t *testing.T, st store.Store, email string) (store.PrincipalID, store.MailboxID) {
	t.Helper()
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: email, DisplayName: email, QuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (INBOX): %v", err)
	}
	return p.ID, inbox.ID
}

// TestList_DuplicateAndThreadFlags reproduces the #487 orphan shapes: a
// deleted ancestor whose blob survives, with a live message sharing its
// Message-ID (the mirror's own dedup, not a loss) and a live reply
// referencing it (the thread the orphan belongs to survived).
func TestList_DuplicateAndThreadFlags(t *testing.T) {
	for _, be := range backends(t) {
		t.Run(be.name, func(t *testing.T) {
			st := be.open(t)
			ctx := context.Background()
			pid, inbox := setUpPrincipal(t, st, "list1@example.test")

			d := time.Date(2026, 8, 14, 9, 41, 21, 0, time.UTC)
			ancestorID, ancestorHash := insertMessage(t, st, pid, inbox, "orig@test", "", "Terminvorschlag", "melina@example.test", d)
			_, _ = insertMessage(t, st, pid, inbox, "reply@test", "orig@test", "Re: Terminvorschlag", "hans@example.test", d.Add(3*time.Minute))
			// A live duplicate of the ancestor's own Message-ID (the
			// mirror's sent-copy dedup shape from the ticket).
			dupID, _ := insertMessage(t, st, pid, inbox, "orig@test", "", "Terminvorschlag (dup)", "melina@example.test", d)

			// Delete the ancestor: its blob survives, its row does not.
			if err := st.Meta().RemoveMessageFromMailbox(ctx, ancestorID, inbox); err != nil {
				t.Fatalf("RemoveMessageFromMailbox: %v", err)
			}
			if _, err := st.Meta().GetMessage(ctx, ancestorID); err == nil {
				t.Fatalf("ancestor message survived removal from its only mailbox")
			}

			orphans, err := orphanblobs.List(ctx, st)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var found *orphanblobs.OrphanBlob
			for i := range orphans {
				if orphans[i].Hash == ancestorHash {
					found = &orphans[i]
				}
			}
			if found == nil {
				t.Fatalf("List did not report the ancestor's orphaned blob; got %d orphans", len(orphans))
			}
			if found.MessageID != "<orig@test>" {
				t.Errorf("MessageID = %q; want <orig@test>", found.MessageID)
			}
			if found.Subject != "Terminvorschlag" {
				t.Errorf("Subject = %q; want Terminvorschlag", found.Subject)
			}
			if found.DuplicateOfLiveMessageID != dupID {
				t.Errorf("DuplicateOfLiveMessageID = %d; want %d (the live duplicate)", found.DuplicateOfLiveMessageID, dupID)
			}
			if !found.ReferencedByLiveThread {
				t.Errorf("ReferencedByLiveThread = false; want true (the live reply references it)")
			}
		})
	}
}

// TestRestore_ThreadsIntoExistingReplyAndRefusesReRestore is the #486
// reproduction: restoring a deleted ancestor's blob re-inserts it into
// Archive, joins the reply's existing thread via the store's late-ancestor
// merge (#485), and refuses a second restore of the same blob as a
// duplicate of the message it just created.
func TestRestore_ThreadsIntoExistingReplyAndRefusesReRestore(t *testing.T) {
	for _, be := range backends(t) {
		t.Run(be.name, func(t *testing.T) {
			st := be.open(t)
			ctx := context.Background()
			pid, inbox := setUpPrincipal(t, st, "restore1@example.test")

			d := time.Date(2026, 8, 14, 9, 41, 21, 0, time.UTC)
			ancestorID, ancestorHash := insertMessage(t, st, pid, inbox, "anc@test", "", "Original", "melina@example.test", d)
			replyID, _ := insertMessage(t, st, pid, inbox, "rep@test", "anc@test", "Re: Original", "hans@example.test", d.Add(3*time.Minute))

			reply, err := st.Meta().GetMessage(ctx, replyID)
			if err != nil {
				t.Fatalf("GetMessage (reply, before): %v", err)
			}

			if err := st.Meta().RemoveMessageFromMailbox(ctx, ancestorID, inbox); err != nil {
				t.Fatalf("RemoveMessageFromMailbox: %v", err)
			}

			res, err := orphanblobs.Restore(ctx, st, pid, ancestorHash, 0)
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if res.Refused {
				t.Fatalf("Restore refused: %s", res.Reason)
			}
			if res.MessageID == 0 {
				t.Fatalf("Restore did not return a MessageID")
			}

			restored, err := st.Meta().GetMessage(ctx, res.MessageID)
			if err != nil {
				t.Fatalf("GetMessage (restored): %v", err)
			}
			if restored.Envelope.Subject != "Original" {
				t.Errorf("restored Subject = %q; want Original", restored.Envelope.Subject)
			}
			if restored.IngestSource != store.IngestSourceDiagRestore {
				t.Errorf("restored IngestSource = %q; want %q", restored.IngestSource, store.IngestSourceDiagRestore)
			}
			var archiveMB store.MailboxID
			var seen bool
			for _, mm := range restored.Mailboxes {
				mb, gerr := st.Meta().GetMailboxByID(ctx, mm.MailboxID)
				if gerr == nil && mb.Attributes&store.MailboxAttrArchive != 0 {
					archiveMB = mm.MailboxID
					seen = mm.Flags&store.MessageFlagSeen != 0
				}
			}
			if archiveMB == 0 {
				t.Errorf("restored message carries no Archive membership: %+v", restored.Mailboxes)
			}
			if !seen {
				t.Errorf("restored message's Archive membership is not $seen")
			}

			replyAfter, err := st.Meta().GetMessage(ctx, replyID)
			if err != nil {
				t.Fatalf("GetMessage (reply, after): %v", err)
			}
			// ThreadID == 0 means "this message roots its own thread"
			// (its own MessageID is the effective key), the same
			// convention ThreadHasArchiveMember/ThreadHasPrincipalSentMember
			// apply (imapimport/sync.go) -- so the merge is verified by
			// comparing effective keys, not raw ThreadID values.
			effectiveKey := func(m store.Message) store.MessageID {
				if m.ThreadID != 0 {
					return store.MessageID(m.ThreadID)
				}
				return m.ID
			}
			if effectiveKey(restored) != effectiveKey(replyAfter) {
				t.Errorf("restored effective thread key=%d replyAfter effective thread key=%d (reply.ThreadID was %d before restore); want the late-ancestor merge to join them",
					effectiveKey(restored), effectiveKey(replyAfter), reply.ThreadID)
			}

			// Restoring the same blob again must be refused: a live
			// message (the one just created) now carries its Message-ID.
			res2, err := orphanblobs.Restore(ctx, st, pid, ancestorHash, 0)
			if err != nil {
				t.Fatalf("Restore (second): %v", err)
			}
			if !res2.Refused {
				t.Fatalf("second Restore of the same blob was not refused; want a duplicate refusal")
			}
		})
	}
}

// TestRestore_WithLabel verifies the optional --label mailbox is added
// alongside Archive.
func TestRestore_WithLabel(t *testing.T) {
	for _, be := range backends(t) {
		t.Run(be.name, func(t *testing.T) {
			st := be.open(t)
			ctx := context.Background()
			pid, inbox := setUpPrincipal(t, st, "restore2@example.test")

			d := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
			msgID, hash := insertMessage(t, st, pid, inbox, "lbl@test", "", "Labelled", "someone@example.test", d)
			label, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "vorsitz@classic-computing.de"})
			if err != nil {
				t.Fatalf("InsertMailbox (label): %v", err)
			}

			if err := st.Meta().RemoveMessageFromMailbox(ctx, msgID, inbox); err != nil {
				t.Fatalf("RemoveMessageFromMailbox: %v", err)
			}

			res, err := orphanblobs.Restore(ctx, st, pid, hash, label.ID)
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if res.Refused {
				t.Fatalf("Restore refused: %s", res.Reason)
			}
			restored, err := st.Meta().GetMessage(ctx, res.MessageID)
			if err != nil {
				t.Fatalf("GetMessage: %v", err)
			}
			hasLabel := false
			for _, mm := range restored.Mailboxes {
				if mm.MailboxID == label.ID {
					hasLabel = true
					if mm.Flags&store.MessageFlagSeen == 0 {
						t.Errorf("label membership is not $seen")
					}
				}
			}
			if !hasLabel {
				t.Errorf("restored message carries no membership in the requested label mailbox %d: %+v", label.ID, restored.Mailboxes)
			}
			if len(restored.Mailboxes) != 2 {
				t.Errorf("restored.Mailboxes = %+v; want exactly [Archive, label]", restored.Mailboxes)
			}
		})
	}
}
