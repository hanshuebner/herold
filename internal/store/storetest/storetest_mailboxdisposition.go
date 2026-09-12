package storetest

// storetest_mailboxdisposition.go -- compliance cases for the category
// disposition and priority columns on mailboxes (issue #333, ADR-0004,
// docs/design/web/requirements/05-categorisation.md REQ-CAT-01..11).
//
// Covers: round trip of both fields through InsertMailbox / GetMailboxByID
// / SetMailboxDisposition; dense renumbering by ReorderMailboxPriority
// after an insert into the middle of the ranked list, a move to the end,
// and an unrank; and CountPinnedMailboxes for the five-pinned rule
// (REQ-CAT-11), enforced by the JMAP layer, not the store.

import (
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

func intPtr(v int) *int { return &v }

func testMailboxDispositionAndPriorityRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mb-disposition@example.com")

	// A plain InsertMailbox (no Disposition/Priority given) defaults to
	// "none" / unranked.
	plain := mustInsertMailbox(t, s, p.ID, "Plain")
	if plain.Disposition != store.MailboxDispositionNone {
		t.Fatalf("plain mailbox Disposition = %q, want %q", plain.Disposition, store.MailboxDispositionNone)
	}
	if plain.Priority != nil {
		t.Fatalf("plain mailbox Priority = %v, want nil", *plain.Priority)
	}
	gotPlain, err := s.Meta().GetMailboxByID(ctx, plain.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID(plain): %v", err)
	}
	if gotPlain.Disposition != store.MailboxDispositionNone || gotPlain.Priority != nil {
		t.Fatalf("GetMailboxByID(plain) = disposition %q priority %v, want none/nil",
			gotPlain.Disposition, gotPlain.Priority)
	}

	// InsertMailbox persists an explicit Disposition + Priority.
	cat, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Newsletters",
		Disposition: store.MailboxDispositionPinned, Priority: intPtr(3),
	})
	if err != nil {
		t.Fatalf("InsertMailbox(category): %v", err)
	}
	got, err := s.Meta().GetMailboxByID(ctx, cat.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID(category): %v", err)
	}
	if got.Disposition != store.MailboxDispositionPinned {
		t.Fatalf("Disposition after insert = %q, want %q", got.Disposition, store.MailboxDispositionPinned)
	}
	if got.Priority == nil || *got.Priority != 3 {
		t.Fatalf("Priority after insert = %v, want 3", got.Priority)
	}

	// InsertMailbox rejects an unrecognised disposition.
	if _, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Bad", Disposition: "not-a-disposition",
	}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertMailbox(bad disposition) = %v, want ErrInvalidArgument", err)
	}

	// SetMailboxDisposition updates an existing mailbox and round-trips.
	for _, d := range []store.MailboxDisposition{
		store.MailboxDispositionBundled, store.MailboxDispositionDaily,
		store.MailboxDispositionWeekly, store.MailboxDispositionFiled,
		store.MailboxDispositionNone,
	} {
		if err := s.Meta().SetMailboxDisposition(ctx, cat.ID, d); err != nil {
			t.Fatalf("SetMailboxDisposition(%s): %v", d, err)
		}
		got, err := s.Meta().GetMailboxByID(ctx, cat.ID)
		if err != nil {
			t.Fatalf("GetMailboxByID after SetMailboxDisposition(%s): %v", d, err)
		}
		if got.Disposition != d {
			t.Fatalf("Disposition after Set(%s) = %q", d, got.Disposition)
		}
		// Priority is untouched by SetMailboxDisposition.
		if got.Priority == nil || *got.Priority != 3 {
			t.Fatalf("Priority after SetMailboxDisposition(%s) = %v, want unchanged 3", d, got.Priority)
		}
	}

	// SetMailboxDisposition rejects an unrecognised value and reports a
	// missing mailbox as ErrNotFound.
	if err := s.Meta().SetMailboxDisposition(ctx, cat.ID, "bogus"); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("SetMailboxDisposition(bogus) = %v, want ErrInvalidArgument", err)
	}
	if err := s.Meta().SetMailboxDisposition(ctx, store.MailboxID(999999), store.MailboxDispositionFiled); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetMailboxDisposition(missing) = %v, want ErrNotFound", err)
	}
}

// mailboxPriority returns the Priority of mailboxID, or nil if unranked.
// Fails the test if the mailbox cannot be read.
func mailboxPriority(t *testing.T, s store.Store, id store.MailboxID) *int {
	t.Helper()
	mb, err := s.Meta().GetMailboxByID(ctxT(t), id)
	if err != nil {
		t.Fatalf("GetMailboxByID(%d): %v", id, err)
	}
	return mb.Priority
}

func wantPriority(t *testing.T, s store.Store, id store.MailboxID, want *int) {
	t.Helper()
	got := mailboxPriority(t, s, id)
	switch {
	case want == nil && got != nil:
		t.Fatalf("mailbox %d Priority = %d, want unranked", id, *got)
	case want != nil && got == nil:
		t.Fatalf("mailbox %d Priority = unranked, want %d", id, *want)
	case want != nil && got != nil && *want != *got:
		t.Fatalf("mailbox %d Priority = %d, want %d", id, *got, *want)
	}
}

func testReorderMailboxPriorityDenseRenumbering(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mb-reorder@example.com")
	a := mustInsertMailbox(t, s, p.ID, "A")
	b := mustInsertMailbox(t, s, p.ID, "B")
	c := mustInsertMailbox(t, s, p.ID, "C")

	// Rank A first: the ranked set is just [A: 0].
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, a.ID, intPtr(0)); err != nil {
		t.Fatalf("ReorderMailboxPriority(A, 0): %v", err)
	}
	wantPriority(t, s, a.ID, intPtr(0))

	// Insert B before A: [B: 0, A: 1].
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, b.ID, intPtr(0)); err != nil {
		t.Fatalf("ReorderMailboxPriority(B, 0): %v", err)
	}
	wantPriority(t, s, b.ID, intPtr(0))
	wantPriority(t, s, a.ID, intPtr(1))

	// Insert C into the middle: [B: 0, C: 1, A: 2].
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, c.ID, intPtr(1)); err != nil {
		t.Fatalf("ReorderMailboxPriority(C, 1): %v", err)
	}
	wantPriority(t, s, b.ID, intPtr(0))
	wantPriority(t, s, c.ID, intPtr(1))
	wantPriority(t, s, a.ID, intPtr(2))

	// Move B to the end: [C: 0, A: 1, B: 2].
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, b.ID, intPtr(2)); err != nil {
		t.Fatalf("ReorderMailboxPriority(B, end): %v", err)
	}
	wantPriority(t, s, c.ID, intPtr(0))
	wantPriority(t, s, a.ID, intPtr(1))
	wantPriority(t, s, b.ID, intPtr(2))

	// Unrank A (the middle of the current [C, A, B] order): the
	// remaining ranked set is renumbered densely to [C: 0, B: 1].
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, a.ID, nil); err != nil {
		t.Fatalf("ReorderMailboxPriority(A, unrank): %v", err)
	}
	wantPriority(t, s, a.ID, nil)
	wantPriority(t, s, c.ID, intPtr(0))
	wantPriority(t, s, b.ID, intPtr(1))

	// Unranking an already-unranked mailbox is a no-op.
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, a.ID, nil); err != nil {
		t.Fatalf("ReorderMailboxPriority(A, unrank again): %v", err)
	}
	wantPriority(t, s, a.ID, nil)
	wantPriority(t, s, c.ID, intPtr(0))
	wantPriority(t, s, b.ID, intPtr(1))

	// A newRank beyond the end of the ranked list clamps to the end
	// instead of leaving a gap.
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, a.ID, intPtr(99)); err != nil {
		t.Fatalf("ReorderMailboxPriority(A, out of range): %v", err)
	}
	wantPriority(t, s, c.ID, intPtr(0))
	wantPriority(t, s, b.ID, intPtr(1))
	wantPriority(t, s, a.ID, intPtr(2))

	// A missing mailbox reports ErrNotFound, as does a mailbox that
	// belongs to a different principal.
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, store.MailboxID(999999), intPtr(0)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ReorderMailboxPriority(missing) = %v, want ErrNotFound", err)
	}
	other := mustInsertPrincipal(t, s, "mb-reorder-other@example.com")
	if err := s.Meta().ReorderMailboxPriority(ctx, other.ID, a.ID, intPtr(0)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ReorderMailboxPriority(wrong principal) = %v, want ErrNotFound", err)
	}
}

// testDeleteMailboxRenumbersRankedSurvivors covers the DeleteMailbox
// deviation found in the #333 verification pass: deleting a ranked
// label must renumber the principal's remaining ranked labels densely
// in the same transaction, reusing the renumbering ReorderMailboxPriority
// performs on an unrank, and append a change-feed entry for every
// survivor whose priority shifted so Mailbox/changes reports it.
func testDeleteMailboxRenumbersRankedSurvivors(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mb-delete-renumber@example.com")
	a := mustInsertMailbox(t, s, p.ID, "A")
	b := mustInsertMailbox(t, s, p.ID, "B")
	c := mustInsertMailbox(t, s, p.ID, "C")

	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, a.ID, intPtr(0)); err != nil {
		t.Fatalf("ReorderMailboxPriority(A, 0): %v", err)
	}
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, b.ID, intPtr(1)); err != nil {
		t.Fatalf("ReorderMailboxPriority(B, 1): %v", err)
	}
	if err := s.Meta().ReorderMailboxPriority(ctx, p.ID, c.ID, intPtr(2)); err != nil {
		t.Fatalf("ReorderMailboxPriority(C, 2): %v", err)
	}
	wantPriority(t, s, a.ID, intPtr(0))
	wantPriority(t, s, b.ID, intPtr(1))
	wantPriority(t, s, c.ID, intPtr(2))

	feedBefore, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed before delete: %v", err)
	}
	cursor := feedBefore[len(feedBefore)-1].Seq

	// Delete the middle rank: B (priority 1). C, the sole survivor
	// after A, must shift from priority 2 down to 1; A is untouched.
	if err := s.Meta().DeleteMailbox(ctx, b.ID); err != nil {
		t.Fatalf("DeleteMailbox(B): %v", err)
	}

	wantPriority(t, s, a.ID, intPtr(0))
	wantPriority(t, s, c.ID, intPtr(1))
	if _, err := s.Meta().GetMailboxByID(ctx, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID(B) after delete = %v, want ErrNotFound", err)
	}

	feedAfter, err := s.Meta().ReadChangeFeed(ctx, p.ID, cursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed after delete: %v", err)
	}
	var sawBDestroyed, sawCUpdated bool
	for _, ch := range feedAfter {
		if ch.Kind != store.EntityKindMailbox {
			continue
		}
		switch {
		case ch.EntityID == uint64(b.ID) && ch.Op == store.ChangeOpDestroyed:
			sawBDestroyed = true
		case ch.EntityID == uint64(c.ID) && ch.Op == store.ChangeOpUpdated:
			sawCUpdated = true
		}
	}
	if !sawBDestroyed {
		t.Errorf("change feed after DeleteMailbox(B) does not report B destroyed: %+v", feedAfter)
	}
	if !sawCUpdated {
		t.Errorf("change feed after DeleteMailbox(B) does not report C's renumbering: %+v", feedAfter)
	}
	// A did not move (already at rank 0) and must not generate a
	// spurious update entry.
	for _, ch := range feedAfter {
		if ch.Kind == store.EntityKindMailbox && ch.EntityID == uint64(a.ID) {
			t.Errorf("change feed after DeleteMailbox(B) reports an unchanged A: %+v", ch)
		}
	}
}

func testCountPinnedMailboxes(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mb-pinned@example.com")
	a := mustInsertMailbox(t, s, p.ID, "A")
	b := mustInsertMailbox(t, s, p.ID, "B")
	c := mustInsertMailbox(t, s, p.ID, "C")

	other := mustInsertPrincipal(t, s, "mb-pinned-other@example.com")
	otherMB := mustInsertMailbox(t, s, other.ID, "Other")
	if err := s.Meta().SetMailboxDisposition(ctx, otherMB.ID, store.MailboxDispositionPinned); err != nil {
		t.Fatalf("SetMailboxDisposition(other): %v", err)
	}

	n, err := s.Meta().CountPinnedMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("CountPinnedMailboxes(none pinned): %v", err)
	}
	if n != 0 {
		t.Fatalf("CountPinnedMailboxes = %d, want 0", n)
	}

	if err := s.Meta().SetMailboxDisposition(ctx, a.ID, store.MailboxDispositionPinned); err != nil {
		t.Fatalf("SetMailboxDisposition(a): %v", err)
	}
	if err := s.Meta().SetMailboxDisposition(ctx, b.ID, store.MailboxDispositionPinned); err != nil {
		t.Fatalf("SetMailboxDisposition(b): %v", err)
	}
	if err := s.Meta().SetMailboxDisposition(ctx, c.ID, store.MailboxDispositionFiled); err != nil {
		t.Fatalf("SetMailboxDisposition(c): %v", err)
	}

	n, err = s.Meta().CountPinnedMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("CountPinnedMailboxes(two pinned): %v", err)
	}
	if n != 2 {
		t.Fatalf("CountPinnedMailboxes = %d, want 2", n)
	}

	if err := s.Meta().SetMailboxDisposition(ctx, a.ID, store.MailboxDispositionNone); err != nil {
		t.Fatalf("SetMailboxDisposition(a, none): %v", err)
	}
	n, err = s.Meta().CountPinnedMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("CountPinnedMailboxes(one pinned): %v", err)
	}
	if n != 1 {
		t.Fatalf("CountPinnedMailboxes = %d, want 1", n)
	}

	// The other principal's pinned mailbox never counts against p.
	otherN, err := s.Meta().CountPinnedMailboxes(ctx, other.ID)
	if err != nil {
		t.Fatalf("CountPinnedMailboxes(other): %v", err)
	}
	if otherN != 1 {
		t.Fatalf("CountPinnedMailboxes(other) = %d, want 1", otherN)
	}
}
