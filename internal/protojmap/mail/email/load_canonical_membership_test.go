package email

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// TestListAccountMessages_CanonicalMembership_LowestMailboxID pins the
// re #402 verifier-round tie-break: once listAccountMessages merges a
// message's per-mailbox rows, the returned store.Message's convenience
// fields (MailboxID / UID / Flags / Keywords) come from the membership
// with the lowest MailboxID -- the same ORDER BY mailbox_id tie-break
// storesqlite/storepg loadMailboxes applies for an unscoped
// GetMessage(mailboxID==0). Before this fix the merged row kept
// whichever membership ListMailboxes (ORDER BY name) visited first,
// which can be a different mailbox than GetMessage's tie-break -- this
// test inserts the mailboxes so that mismatch is forced (the
// alphabetically-first mailbox gets the higher MailboxID).
func TestListAccountMessages_CanonicalMembership_LowestMailboxID(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.Open(context.Background(), dbPath, nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testListAccountMessagesCanonicalMembershipLowestMailboxID(t, st, clk)
}

// TestListAccountMessages_CanonicalMembership_LowestMailboxID_Postgres
// is the Postgres leg: listAccountMessages is backend-agnostic Go code
// layered over store.Metadata, so both backends need direct coverage.
// Skips when HEROLD_PG_DSN is not set.
func TestListAccountMessages_CanonicalMembership_LowestMailboxID_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testListAccountMessagesCanonicalMembershipLowestMailboxID(t, st, clk)
}

func testListAccountMessagesCanonicalMembershipLowestMailboxID(t *testing.T, st store.Store, clk clock.Clock) {
	t.Helper()
	ctx := context.Background()

	canonicalEmail := fmt.Sprintf("canon-%d@example.test", time.Now().UnixNano())
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: canonicalEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	// Insert "Zeta" first so it receives the lower MailboxID, then
	// "Alpha" second so it receives the higher MailboxID but sorts
	// before Zeta in ListMailboxes' ORDER BY name.
	zeta, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Zeta"})
	if err != nil {
		t.Fatalf("InsertMailbox Zeta: %v", err)
	}
	alpha, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Alpha"})
	if err != nil {
		t.Fatalf("InsertMailbox Alpha: %v", err)
	}
	if alpha.ID <= zeta.ID {
		t.Fatalf("test precondition broken: want alpha.ID (%d) > zeta.ID (%d)", alpha.ID, zeta.ID)
	}

	now := clk.Now().UTC()
	msg := store.Message{
		InternalDate: now,
		ReceivedAt:   now,
		Envelope:     store.Envelope{Subject: "canon", Date: now},
	}
	zetaUID, _, err := st.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: zeta.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgID := mostRecentEmailID(t, st, p.ID)
	alphaUID, _, err := st.Meta().AddMessageToMailbox(ctx, msgID, alpha.ID)
	if err != nil {
		t.Fatalf("AddMessageToMailbox alpha: %v", err)
	}
	// Set $seen only on the canonical (lowest-MailboxID) membership, so
	// the assertion below also pins that Flags follows the same
	// tie-break as MailboxID/UID.
	if _, err := st.Meta().UpdateMessageFlags(ctx, msgID, zeta.ID, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags zeta: %v", err)
	}

	out, err := listAccountMessages(ctx, st.Meta(), p.ID, p.ID)
	if err != nil {
		t.Fatalf("listAccountMessages: %v", err)
	}
	var got *store.Message
	for i := range out {
		if out[i].ID == msgID {
			got = &out[i]
		}
	}
	if got == nil {
		t.Fatalf("listAccountMessages did not return message %d: %+v", msgID, out)
	}
	if len(got.Mailboxes) != 2 {
		t.Fatalf("merged Mailboxes len = %d, want 2: %+v", len(got.Mailboxes), got.Mailboxes)
	}
	if got.MailboxID != zeta.ID {
		t.Fatalf("merged MailboxID = %d, want canonical (lowest) zeta.ID %d", got.MailboxID, zeta.ID)
	}
	if got.UID != zetaUID {
		t.Fatalf("merged UID = %d, want canonical zeta UID %d (alpha UID was %d)", got.UID, zetaUID, alphaUID)
	}
	if got.Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("merged Flags = %v, want $seen set (only present on the canonical zeta membership)", got.Flags)
	}
}

// mostRecentEmailID returns the latest EntityKindEmail/Created
// MessageID for the principal, mirroring email_test.go's
// mostRecentMessageID (unavailable here: that helper lives in the
// email_test external test package).
func mostRecentEmailID(t *testing.T, st store.Store, pid store.PrincipalID) store.MessageID {
	t.Helper()
	feed, err := st.Meta().ReadChangeFeed(context.Background(), pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var last store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			last = store.MessageID(e.EntityID)
		}
	}
	if last == 0 {
		t.Fatalf("no email created in feed")
	}
	return last
}
