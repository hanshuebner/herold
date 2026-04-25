package storepg_test

// uidvalidity_test.go pins the deterministic-RandSource contract for
// IMAP UIDVALIDITY against the Postgres backend. STANDARDS §8.5
// rationale documented in the SQLite sibling. Skipped when
// HEROLD_PG_DSN is unset.

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

func TestOpenWithRand_DeterministicUIDValidity(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set")
	}
	ctx := context.Background()
	fixedClk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))

	open := func() store.Store {
		blobDir := t.TempDir()
		rs := bytes.NewReader([]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42})
		s, err := storepg.OpenWithRand(ctx, dsn, filepath.Join(blobDir, "blobs"), nil, fixedClk, rs)
		if err != nil {
			t.Fatalf("OpenWithRand: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		// Wipe tables so the principal/mailbox inserts don't collide.
		_ = truncateTables(s)
		return s
	}

	pid := func(s store.Store) store.PrincipalID {
		p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: "alice@example.test",
			DisplayName:    "Alice",
		})
		if err != nil {
			t.Fatalf("InsertPrincipal: %v", err)
		}
		return p.ID
	}

	s1 := open()
	mb1, err := s1.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid(s1),
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox #1: %v", err)
	}

	s2 := open()
	mb2, err := s2.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid(s2),
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox #2: %v", err)
	}

	if mb1.UIDValidity != mb2.UIDValidity {
		t.Fatalf("UIDVALIDITY not deterministic: mb1=%d mb2=%d", mb1.UIDValidity, mb2.UIDValidity)
	}
	if mb1.UIDValidity == 0 {
		t.Fatalf("UIDVALIDITY must be non-zero")
	}
}
