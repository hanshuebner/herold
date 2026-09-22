package protojmap_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func openChangesFeedStore(t *testing.T) (store.Store, store.PrincipalID) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return st, p.ID
}

// TestWalkChangesBySeq_PagesAndAdvancesCutoff folds a maxChanges-capped
// walk over a change set larger than the cap (re #475): every call must
// see cutoff move past the previous call's cutoff, and the union of
// folded ids across the loop must equal the full set with no id
// reported twice.
func TestWalkChangesBySeq_PagesAndAdvancesCutoff(t *testing.T) {
	ctx := context.Background()
	st, pid := openChangesFeedStore(t)

	const n = 9
	want := map[uint64]bool{}
	for i := 0; i < n; i++ {
		mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: pid,
			Name:        fmt.Sprintf("Box%d", i),
		})
		if err != nil {
			t.Fatalf("InsertMailbox: %v", err)
		}
		want[uint64(mb.ID)] = true
	}

	const maxChanges = 2
	var since store.ChangeSeq
	seen := map[uint64]bool{}
	for calls := 0; ; calls++ {
		if calls > n+2 {
			t.Fatalf("did not converge after %d calls; seen=%d of %d", calls, len(seen), n)
		}
		created, updated, destroyed, cutoff, hasMore, err := protojmap.WalkChangesBySeq(
			ctx, st.Meta(), pid, store.EntityKindMailbox, since, maxChanges)
		if err != nil {
			t.Fatalf("WalkChangesBySeq: %v", err)
		}
		total := len(created) + len(updated) + len(destroyed)
		if total > maxChanges {
			t.Fatalf("call %d returned %d ids, exceeds maxChanges=%d", calls, total, maxChanges)
		}
		if hasMore && cutoff == since {
			t.Fatalf("call %d: cutoff %d == since with hasMore=true; the loop cannot progress", calls, since)
		}
		for id := range created {
			if seen[id] {
				t.Fatalf("id %d reported twice across the fold", id)
			}
			seen[id] = true
		}
		since = cutoff
		if !hasMore {
			break
		}
	}
	if len(seen) != n {
		t.Fatalf("saw %d ids across the fold, want %d", len(seen), n)
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("id %d never reported by any call", id)
		}
	}
}

// TestWalkChangesBySeq_CreateThenUpdateAcrossTheCut covers RFC 8620
// 5.2's created/updated/destroyed folding rule when a create and a
// later update for the SAME id land in different trimmed calls: the
// create must be reported once (as "created", never duplicated or
// promoted to "updated" within the call that already folded the
// create), and a later call must still deliver the update once the
// create has already been drained.
func TestWalkChangesBySeq_CreateThenUpdateAcrossTheCut(t *testing.T) {
	ctx := context.Background()
	st, pid := openChangesFeedStore(t)

	x, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "X"})
	if err != nil {
		t.Fatalf("InsertMailbox X: %v", err)
	}
	y, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "Y"})
	if err != nil {
		t.Fatalf("InsertMailbox Y: %v", err)
	}
	if err := st.Meta().RenameMailbox(ctx, x.ID, "X-renamed"); err != nil {
		t.Fatalf("RenameMailbox X: %v", err)
	}

	// maxChanges=1 forces three calls: create(X), create(Y), then the
	// update for X that a within-one-call fold would have collapsed
	// into "created" (RFC 8620 5.2) had it not been split across the
	// cut by the budget.
	const maxChanges = 1
	var since store.ChangeSeq
	var sawXCreated, sawYCreated, sawXUpdated bool
	for calls := 0; ; calls++ {
		if calls > 5 {
			t.Fatalf("did not converge after %d calls", calls)
		}
		created, updated, _, cutoff, hasMore, err := protojmap.WalkChangesBySeq(
			ctx, st.Meta(), pid, store.EntityKindMailbox, since, maxChanges)
		if err != nil {
			t.Fatalf("WalkChangesBySeq: %v", err)
		}
		if hasMore && cutoff == since {
			t.Fatalf("call %d: cutoff did not advance past sinceState with hasMore=true", calls)
		}
		if _, ok := created[uint64(x.ID)]; ok {
			if sawXCreated {
				t.Fatalf("X reported created twice")
			}
			sawXCreated = true
		}
		if _, ok := created[uint64(y.ID)]; ok {
			if sawYCreated {
				t.Fatalf("Y reported created twice")
			}
			sawYCreated = true
			if !sawXCreated {
				t.Fatalf("Y's create observed before X's create; the feed is not seq-ordered")
			}
		}
		if _, ok := updated[uint64(x.ID)]; ok {
			if !sawXCreated {
				t.Fatalf("X reported updated before its create was ever reported")
			}
			if sawXUpdated {
				t.Fatalf("X reported updated twice")
			}
			sawXUpdated = true
		}
		since = cutoff
		if !hasMore {
			break
		}
	}
	if !sawXCreated || !sawYCreated || !sawXUpdated {
		t.Fatalf("incomplete fold: sawXCreated=%v sawYCreated=%v sawXUpdated=%v", sawXCreated, sawYCreated, sawXUpdated)
	}
}

// TestWalkChangesByOrdinal_PagesAndAdvancesCutoff is
// TestWalkChangesBySeq_PagesAndAdvancesCutoff's counterpart for the
// jmap_states-counter datatypes (Contact, AddressBook, Calendar,
// CalendarEvent, SeenAddress), using AddressBook as a representative
// EntityKind since it shares the same walk shape.
func TestWalkChangesByOrdinal_PagesAndAdvancesCutoff(t *testing.T) {
	ctx := context.Background()
	st, pid := openChangesFeedStore(t)

	const n = 9
	want := map[uint64]bool{}
	for i := 0; i < n; i++ {
		abID, err := st.Meta().InsertAddressBook(ctx, store.AddressBook{PrincipalID: pid, Name: fmt.Sprintf("AB%d", i)})
		if err != nil {
			t.Fatalf("InsertAddressBook: %v", err)
		}
		want[uint64(abID)] = true
	}

	const maxChanges = 2
	var since int64
	seen := map[uint64]bool{}
	for calls := 0; ; calls++ {
		if calls > n+2 {
			t.Fatalf("did not converge after %d calls; seen=%d of %d", calls, len(seen), n)
		}
		created, updated, destroyed, cutoff, hasMore, err := protojmap.WalkChangesByOrdinal(
			ctx, st.Meta(), pid, store.EntityKindAddressBook, since, maxChanges, false)
		if err != nil {
			t.Fatalf("WalkChangesByOrdinal: %v", err)
		}
		total := len(created) + len(updated) + len(destroyed)
		if total > maxChanges {
			t.Fatalf("call %d returned %d ids, exceeds maxChanges=%d", calls, total, maxChanges)
		}
		if hasMore && cutoff == since {
			t.Fatalf("call %d: cutoff %d == since with hasMore=true; the loop cannot progress", calls, since)
		}
		for id := range created {
			if seen[id] {
				t.Fatalf("id %d reported twice across the fold", id)
			}
			seen[id] = true
		}
		since = cutoff
		if !hasMore {
			break
		}
	}
	if len(seen) != n {
		t.Fatalf("saw %d ids across the fold, want %d", len(seen), n)
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("id %d never reported by any call", id)
		}
	}
}
