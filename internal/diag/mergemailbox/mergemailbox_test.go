package mergemailbox_test

// mergemailbox_test.go covers the #509 repair path: Analyze/Merge fold a
// duplicate junk-role mailbox into the account's surviving one, moving
// non-overlapping memberships, dropping overlapping ones, rewriting every
// other table that references the source mailbox id (wake_mailbox_id in
// particular), and refusing when the source has children or ACL grants.
//
// Both tests run on sqlite always and postgres when HEROLD_PG_DSN is set
// (STANDARDS.md S8), mirroring internal/diag/dupmessages's backend pattern.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/mergemailbox"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

type backend struct {
	name string
	open func(t *testing.T) store.Store
}

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

// fixture holds the mailboxes and messages a merge test needs: a
// principal with Inbox, a "Spam" mailbox (the duplicate, merged away) and
// a "Junk" mailbox (the survivor), both carrying \Junk, plus three
// messages exercising the three merge cases.
type fixture struct {
	pid              store.PrincipalID
	inbox, spam, jnk store.Mailbox
	// onlyInSpam is a member of Spam only -- Merge must move it to Junk.
	onlyInSpam store.MessageID
	// inBoth is a member of both Spam and Junk -- Merge must drop the
	// Spam membership without duplicating the Junk one.
	inBoth store.MessageID
	// snoozedWithSpamWake lives in Inbox, snoozed with Spam as its wake
	// destination -- Merge must repoint wake_mailbox_id to Junk.
	snoozedWithSpamWake store.MessageID
}

func setupFixture(t *testing.T, st store.Store) fixture {
	t.Helper()
	ctx := context.Background()

	pid, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "hans@merge509.test",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(INBOX): %v", err)
	}
	spam, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid.ID, Name: "Spam", Attributes: store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Spam): %v", err)
	}
	jnk, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid.ID, Name: "Junk", Attributes: store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Junk): %v", err)
	}

	putMsg := func(mb store.MailboxID, envMsgID, body string) store.MessageID {
		ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		when := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:  pid.ID,
			Blob:         ref,
			Size:         ref.Size,
			InternalDate: when,
			ReceivedAt:   when,
			Envelope:     store.Envelope{MessageID: envMsgID, Subject: "merge-509 probe"},
		}, []store.MessageMailbox{{MailboxID: mb}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		msgs, err := st.Meta().ListMessages(ctx, mb, store.MessageFilter{Limit: 1000})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		return msgs[len(msgs)-1].ID
	}

	onlyInSpam := putMsg(spam.ID, "only-in-spam-509@example.test", "only in spam")

	inBoth := putMsg(spam.ID, "in-both-509@example.test", "in both")
	if _, _, err := st.Meta().AddMessageToMailbox(ctx, inBoth, jnk.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(inBoth, Junk): %v", err)
	}

	snoozedWithSpamWake := putMsg(inbox.ID, "snoozed-wake-509@example.test", "snoozed, wakes to spam")
	wake := spam.ID
	future := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if _, err := st.Meta().SetSnooze(ctx, snoozedWithSpamWake, inbox.ID, &future, &wake); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}

	return fixture{
		pid: pid.ID, inbox: inbox, spam: spam, jnk: jnk,
		onlyInSpam: onlyInSpam, inBoth: inBoth, snoozedWithSpamWake: snoozedWithSpamWake,
	}
}

func TestAnalyzeThenMerge(t *testing.T) {
	for _, b := range backends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			testAnalyzeThenMerge(t, b.open(t))
		})
	}
}

func testAnalyzeThenMerge(t *testing.T, st store.Store) {
	ctx := context.Background()
	f := setupFixture(t, st)

	// -- Analyze is a dry run: it must report the plan without writing
	// anything. --
	plan, err := mergemailbox.Analyze(ctx, st, f.pid, f.spam.ID, f.jnk.ID)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(plan.Moved) != 1 || plan.Moved[0] != f.onlyInSpam {
		t.Fatalf("Analyze Moved = %v, want [%d]", plan.Moved, f.onlyInSpam)
	}
	if len(plan.Dropped) != 1 || plan.Dropped[0] != f.inBoth {
		t.Fatalf("Analyze Dropped = %v, want [%d]", plan.Dropped, f.inBoth)
	}
	if plan.Refs.WakeDestinations != 1 {
		t.Fatalf("Analyze Refs.WakeDestinations = %d, want 1", plan.Refs.WakeDestinations)
	}

	// Spam must still exist and still hold its memberships -- Analyze
	// wrote nothing.
	if _, err := st.Meta().GetMailboxByID(ctx, f.spam.ID); err != nil {
		t.Fatalf("GetMailboxByID(Spam) after Analyze: %v", err)
	}
	onlyMsg, err := st.Meta().GetMessage(ctx, f.onlyInSpam)
	if err != nil {
		t.Fatalf("GetMessage(onlyInSpam) after Analyze: %v", err)
	}
	if len(onlyMsg.Mailboxes) != 1 || onlyMsg.Mailboxes[0].MailboxID != f.spam.ID {
		t.Fatalf("onlyInSpam.Mailboxes after Analyze = %v, want only Spam (dry run must not mutate)", onlyMsg.Mailboxes)
	}

	// -- Merge performs the plan Analyze reported. --
	res, err := mergemailbox.Merge(ctx, st, f.pid, f.spam.ID, f.jnk.ID)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Moved) != 1 || res.Moved[0] != f.onlyInSpam {
		t.Fatalf("Merge Moved = %v, want [%d]", res.Moved, f.onlyInSpam)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != f.inBoth {
		t.Fatalf("Merge Dropped = %v, want [%d]", res.Dropped, f.inBoth)
	}

	// onlyInSpam moved into Junk via the normal move path: new mailbox,
	// membership preserved (no duplicate).
	onlyMsg, err = st.Meta().GetMessage(ctx, f.onlyInSpam)
	if err != nil {
		t.Fatalf("GetMessage(onlyInSpam) after Merge: %v", err)
	}
	if len(onlyMsg.Mailboxes) != 1 || onlyMsg.Mailboxes[0].MailboxID != f.jnk.ID {
		t.Fatalf("onlyInSpam.Mailboxes after Merge = %v, want only Junk", onlyMsg.Mailboxes)
	}

	// inBoth kept its single Junk membership -- not duplicated, Spam
	// membership dropped.
	bothMsg, err := st.Meta().GetMessage(ctx, f.inBoth)
	if err != nil {
		t.Fatalf("GetMessage(inBoth) after Merge: %v", err)
	}
	if len(bothMsg.Mailboxes) != 1 || bothMsg.Mailboxes[0].MailboxID != f.jnk.ID {
		t.Fatalf("inBoth.Mailboxes after Merge = %v, want exactly one Junk membership", bothMsg.Mailboxes)
	}

	// The snoozed message's wake destination now points at Junk, not the
	// deleted Spam mailbox.
	snoozed, err := st.Meta().GetMessage(ctx, f.snoozedWithSpamWake)
	if err != nil {
		t.Fatalf("GetMessage(snoozedWithSpamWake) after Merge: %v", err)
	}
	var inboxMM store.MessageMailbox
	found := false
	for _, mm := range snoozed.Mailboxes {
		if mm.MailboxID == f.inbox.ID {
			inboxMM = mm
			found = true
		}
	}
	if !found {
		t.Fatalf("snoozedWithSpamWake lost its Inbox membership: %+v", snoozed.Mailboxes)
	}
	if inboxMM.WakeMailboxID == nil || *inboxMM.WakeMailboxID != f.jnk.ID {
		t.Fatalf("snoozedWithSpamWake wake_mailbox_id = %v, want %d (Junk)", inboxMM.WakeMailboxID, f.jnk.ID)
	}

	// Spam itself is gone.
	if _, err := st.Meta().GetMailboxByID(ctx, f.spam.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID(Spam) after Merge: err=%v, want ErrNotFound", err)
	}
}

func TestMergeRefusesWithChildMailbox(t *testing.T) {
	for _, b := range backends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			st := b.open(t)
			f := setupFixture(t, st)
			if _, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
				PrincipalID: f.pid, Name: "Spam/Child", ParentID: f.spam.ID,
			}); err != nil {
				t.Fatalf("InsertMailbox(child): %v", err)
			}

			if _, err := mergemailbox.Analyze(ctx, st, f.pid, f.spam.ID, f.jnk.ID); !errors.Is(err, mergemailbox.ErrHasChildren) {
				t.Fatalf("Analyze err = %v, want ErrHasChildren", err)
			}
			if _, err := mergemailbox.Merge(ctx, st, f.pid, f.spam.ID, f.jnk.ID); !errors.Is(err, mergemailbox.ErrHasChildren) {
				t.Fatalf("Merge err = %v, want ErrHasChildren", err)
			}
			// Refused: Spam must still exist.
			if _, err := st.Meta().GetMailboxByID(ctx, f.spam.ID); err != nil {
				t.Fatalf("GetMailboxByID(Spam) after refused merge: %v", err)
			}
		})
	}
}

func TestMergeRefusesWithACLGrant(t *testing.T) {
	for _, b := range backends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			st := b.open(t)
			f := setupFixture(t, st)
			other, err := st.Meta().InsertPrincipal(ctx, store.Principal{
				Kind:           store.PrincipalKindUser,
				CanonicalEmail: "other-509@merge509.test",
			})
			if err != nil {
				t.Fatalf("InsertPrincipal(other): %v", err)
			}
			if err := st.Meta().SetMailboxACL(ctx, f.spam.ID, &other.ID, store.ACLRightLookup|store.ACLRightRead, f.pid); err != nil {
				t.Fatalf("SetMailboxACL: %v", err)
			}

			if _, err := mergemailbox.Analyze(ctx, st, f.pid, f.spam.ID, f.jnk.ID); !errors.Is(err, mergemailbox.ErrHasACLGrants) {
				t.Fatalf("Analyze err = %v, want ErrHasACLGrants", err)
			}
			if _, err := mergemailbox.Merge(ctx, st, f.pid, f.spam.ID, f.jnk.ID); !errors.Is(err, mergemailbox.ErrHasACLGrants) {
				t.Fatalf("Merge err = %v, want ErrHasACLGrants", err)
			}
		})
	}
}
