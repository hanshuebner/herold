package dupmessages_test

// dupmessages_test.go covers the #496 recovery path: List finds every
// Message-ID a principal holds more than one live row for, and Merge folds
// the newer row(s) onto the oldest, unioning mailbox memberships and
// keywords and OR-ing in $seen, then removes the folded rows through the
// normal destroy path.
//
// Both tests run on sqlite always and postgres when HEROLD_PG_DSN is set
// (STANDARDS.md §8.6), mirroring internal/diag/orphanblobs's backend
// pattern.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/dupmessages"
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

func TestListAndMerge(t *testing.T) {
	for _, b := range backends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			testListAndMerge(t, b.open(t))
		})
	}
}

func testListAndMerge(t *testing.T, st store.Store) {
	ctx := context.Background()

	pid, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "hans@dup.test",
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
	provenance, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid.ID, Name: "classic-computing.de",
	})
	if err != nil {
		t.Fatalf("InsertMailbox(provenance): %v", err)
	}

	putMsg := func(mb store.MailboxID, envMsgID, body string, when time.Time, flags store.MessageFlags, kw []string) store.MessageID {
		ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:  pid.ID,
			Blob:         ref,
			Size:         ref.Size,
			InternalDate: when,
			ReceivedAt:   when,
			Envelope:     store.Envelope{MessageID: envMsgID, Subject: "dedup probe"},
		}, []store.MessageMailbox{{MailboxID: mb, Flags: flags, Keywords: kw}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		msgs, err := st.Meta().ListMessages(ctx, mb, store.MessageFilter{Limit: 1000})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		return msgs[len(msgs)-1].ID
	}

	t0 := time.Date(2026, 9, 26, 18, 0, 7, 0, time.UTC)
	t1 := t0.Add(16 * time.Second)

	// Thread 3994's pair, re-created: the mirror-imported copy lands in
	// Inbox + the account's provenance label, unread; the SMTP copy
	// arrives 16s later, in Inbox only, already seen.
	mirrorID := putMsg(inbox.ID, "mirror-dup-496@example.test", "mirror body", t0, 0, nil)
	if _, _, err := st.Meta().AddMessageToMailbox(ctx, mirrorID, provenance.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(provenance): %v", err)
	}
	smtpID := putMsg(inbox.ID, "mirror-dup-496@example.test", "smtp body (different bytes)", t1, store.MessageFlagSeen, []string{"$category-promotions"})

	groups, err := dupmessages.List(ctx, st, pid.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("List = %d groups, want 1: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.MessageID != "mirror-dup-496@example.test" {
		t.Fatalf("group MessageID = %q", g.MessageID)
	}
	if len(g.Messages) != 2 || g.Messages[0].ID != mirrorID || g.Messages[1].ID != smtpID {
		t.Fatalf("group Messages = %v, want [%d, %d] oldest-first", g.Messages, mirrorID, smtpID)
	}

	res, err := dupmessages.Merge(ctx, st, g)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.KeptMessageID != mirrorID {
		t.Fatalf("KeptMessageID = %d, want %d (the older row)", res.KeptMessageID, mirrorID)
	}
	if len(res.Removed) != 1 || res.Removed[0].MessageID != smtpID {
		t.Fatalf("Removed = %+v, want exactly the SMTP row %d", res.Removed, smtpID)
	}

	// The removed row is gone.
	if _, err := st.Meta().GetMessage(ctx, smtpID); err == nil {
		t.Fatalf("GetMessage(%d) succeeded after Merge; the duplicate should be destroyed", smtpID)
	}

	// The survivor gained the SMTP copy's $seen flag and category keyword
	// in Inbox, and kept its own provenance-label membership.
	kept, err := st.Meta().GetMessage(ctx, mirrorID)
	if err != nil {
		t.Fatalf("GetMessage(kept): %v", err)
	}
	byMailbox := make(map[store.MailboxID]store.MessageMailbox, len(kept.Mailboxes))
	for _, mm := range kept.Mailboxes {
		byMailbox[mm.MailboxID] = mm
	}
	inboxMM, ok := byMailbox[inbox.ID]
	if !ok {
		t.Fatalf("kept row lost its Inbox membership: %+v", kept.Mailboxes)
	}
	if inboxMM.Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("kept row Inbox membership not marked $seen after merging a seen duplicate: flags=%v", inboxMM.Flags)
	}
	foundCategory := false
	for _, kw := range inboxMM.Keywords {
		if kw == "$category-promotions" {
			foundCategory = true
		}
	}
	if !foundCategory {
		t.Fatalf("kept row Inbox keywords = %v, want $category-promotions unioned in", inboxMM.Keywords)
	}
	if _, ok := byMailbox[provenance.ID]; !ok {
		t.Fatalf("kept row lost its provenance-label membership: %+v", kept.Mailboxes)
	}

	// List now reports no more duplicates for this principal.
	groupsAfter, err := dupmessages.List(ctx, st, pid.ID)
	if err != nil {
		t.Fatalf("List (after merge): %v", err)
	}
	if len(groupsAfter) != 0 {
		t.Fatalf("List (after merge) = %+v, want no groups", groupsAfter)
	}
}
