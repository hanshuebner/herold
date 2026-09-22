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

// TestLoadMessageForPrincipal_VisibleThroughNonCanonicalMembership covers
// #472: loadMessageForPrincipal must decide visibility from every
// membership the message currently holds, not just the one
// GetMessage's convenience MailboxID field names (whichever membership
// has the lowest mailbox id). A message owned by one principal can
// carry two current memberships with different ACL exposure to a
// second principal -- e.g. after a multi-step sub-account promotion,
// or simply a message filed under both a private and a shared folder.
// The lower-id mailbox here is NOT shared; the higher-id one is. A
// caller with Lookup rights only on the shared mailbox must still see
// the message.
func TestLoadMessageForPrincipal_VisibleThroughNonCanonicalMembership(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.Open(context.Background(), dbPath, nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testLoadMessageForPrincipalVisibleThroughNonCanonicalMembership(t, st, clk)
}

// TestLoadMessageForPrincipal_VisibleThroughNonCanonicalMembership_Postgres
// is the Postgres leg: loadMessageForPrincipal is backend-agnostic Go
// code layered over store.Metadata, so both backends need direct
// coverage. Skips when HEROLD_PG_DSN is not set.
func TestLoadMessageForPrincipal_VisibleThroughNonCanonicalMembership_Postgres(t *testing.T) {
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
	testLoadMessageForPrincipalVisibleThroughNonCanonicalMembership(t, st, clk)
}

func testLoadMessageForPrincipalVisibleThroughNonCanonicalMembership(t *testing.T, st store.Store, clk clock.Clock) {
	t.Helper()
	ctx := context.Background()

	ownerEmail := fmt.Sprintf("owner-%d@example.test", time.Now().UnixNano())
	owner, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: ownerEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal owner: %v", err)
	}
	callerEmail := fmt.Sprintf("caller-%d@example.test", time.Now().UnixNano())
	caller, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: callerEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal caller: %v", err)
	}

	// Private is created first (lower id, not shared); Shared is
	// created second (higher id) and gets an ACL grant to caller.
	private, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: owner.ID, Name: "Private"})
	if err != nil {
		t.Fatalf("InsertMailbox Private: %v", err)
	}
	shared, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: owner.ID, Name: "Shared"})
	if err != nil {
		t.Fatalf("InsertMailbox Shared: %v", err)
	}
	if shared.ID <= private.ID {
		t.Fatalf("test precondition broken: want shared.ID (%d) > private.ID (%d)", shared.ID, private.ID)
	}
	if err := st.Meta().SetMailboxACL(ctx, shared.ID, &caller.ID, store.ACLRightLookup, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL: %v", err)
	}

	now := clk.Now().UTC()
	msg := store.Message{
		InternalDate: now,
		ReceivedAt:   now,
		Envelope:     store.Envelope{Subject: "cross-membership-visibility", Date: now},
	}
	if _, _, err := st.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: private.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgID := mostRecentEmailID(t, st, owner.ID)
	if _, _, err := st.Meta().AddMessageToMailbox(ctx, msgID, shared.ID); err != nil {
		t.Fatalf("AddMessageToMailbox shared: %v", err)
	}

	got, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.MailboxID != private.ID {
		t.Fatalf("test setup: convenience MailboxID = %d, want %d (Private, the lowest id) -- the case this test pins", got.MailboxID, private.ID)
	}

	if _, err := loadMessageForPrincipal(ctx, st.Meta(), caller.ID, msgID); err != nil {
		t.Fatalf("loadMessageForPrincipal(caller) = %v, want visible via the Shared membership's ACL grant", err)
	}

	// A third principal with no grant on either membership must still
	// be denied.
	strangerEmail := fmt.Sprintf("stranger-%d@example.test", time.Now().UnixNano())
	stranger, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: strangerEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal stranger: %v", err)
	}
	if _, err := loadMessageForPrincipal(ctx, st.Meta(), stranger.ID, msgID); err != errMessageMissing {
		t.Fatalf("loadMessageForPrincipal(stranger) = %v, want errMessageMissing", err)
	}
}
