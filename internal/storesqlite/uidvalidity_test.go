package storesqlite_test

// uidvalidity_test.go pins the deterministic-RandSource contract for
// IMAP UIDVALIDITY. STANDARDS §8.5: tests are deterministic; real
// randomness is a bug. Wave-4 review flagged the previous
// math/rand.Uint32 path as both non-deterministic and a wall-clock
// derivative. The fix injects an io.Reader at Open time; production
// uses crypto/rand, tests use a deterministic reader.

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func TestOpenWithRand_DeterministicUIDValidity(t *testing.T) {
	ctx := context.Background()
	fixedClk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))

	open := func(t *testing.T) store.Store {
		t.Helper()
		dir := t.TempDir()
		// Single deterministic byte feeds the one-byte UIDVALIDITY salt.
		// Two boxes constructed with identical inputs (clock + reader)
		// must produce identical UIDVALIDITY values.
		rs := bytes.NewReader([]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42})
		s, err := storesqlite.OpenWithRand(ctx, filepath.Join(dir, "meta.db"), nil, fixedClk, rs)
		if err != nil {
			t.Fatalf("OpenWithRand: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	s1 := open(t)
	s2 := open(t)

	// Each store needs a principal before we can insert a mailbox.
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
	mb1, err := s1.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid(s1),
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox #1: %v", err)
	}
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
	// The stored value must be > 0 and reflect the timestamp seconds.
	expectSec := uint32(fixedClk.Now().Unix())
	if uint32(mb1.UIDValidity) < expectSec {
		t.Errorf("UIDVALIDITY %d below epoch-seconds floor %d", mb1.UIDValidity, expectSec)
	}
}

func TestOpen_ProductionPathReturnsNonZeroUIDValidity(t *testing.T) {
	// Sanity for the default Open() entrypoint: it must wire crypto/rand
	// without crashing and produce a non-zero, plausibly-current
	// UIDVALIDITY. We do not assert on a specific value.
	ctx := context.Background()
	dir := t.TempDir()
	s, err := storesqlite.Open(ctx, filepath.Join(dir, "meta.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	if mb.UIDValidity == 0 {
		t.Fatalf("UIDVALIDITY must be non-zero on production path")
	}
}
