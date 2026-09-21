package storesqlite_test

// TestEnsureCategoryLabelMailboxes_SteadyStateSkipsWriteTx is an
// observational regression guard for issue #406: once every derived
// category already has a backing label mailbox, a further
// EnsureCategoryLabelMailboxes call for the same set must not open a
// write transaction.
//
// The test proves this by contention rather than by reading the
// source: a competing connection holds SQLite's single write lock
// (BEGIN IMMEDIATE, acquired at transaction start via the
// _txlock=immediate DSN parameter every database/sql transaction
// opens with here -- see concurrent_writes_test.go) for two seconds.
// A steady-state call -- every name in cats already backed -- runs
// its existence check as a plain read, which WAL mode lets proceed
// concurrently with the held write lock, so it must return almost
// immediately. A control call that is missing one category name does
// need the write lock and therefore blocks until the competing
// transaction releases it, which confirms the held lock is genuine
// contention and not a no-op.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// acquireLockFor starts a goroutine that holds SQLite's write lock
// (via BEGIN IMMEDIATE) on db from the moment BeginTx returns until
// releaseAt, then rolls back. It returns once the lock is confirmed
// held (or fails the test on error), and a channel that receives the
// eventual Rollback result.
func acquireLockFor(t *testing.T, db *sql.DB, releaseAt time.Time) <-chan error {
	t.Helper()
	acquired := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			close(acquired)
			done <- err
			return
		}
		close(acquired)
		time.Sleep(time.Until(releaseAt))
		done <- tx.Rollback()
	}()
	<-acquired
	return done
}

func TestEnsureCategoryLabelMailboxes_SteadyStateSkipsWriteTx(t *testing.T) {
	s, cleanup := openStore(t)
	defer cleanup()
	ctx := context.Background()

	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "writetx-406@example.test",
		DisplayName:    "WriteTx 406",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	cats := []string{"primary", "social"}
	if err := s.Meta().EnsureCategoryLabelMailboxes(ctx, p.ID, cats); err != nil {
		t.Fatalf("seed EnsureCategoryLabelMailboxes: %v", err)
	}

	raw, ok := s.(*storesqlite.Store)
	if !ok {
		t.Fatalf("openStore did not return *storesqlite.Store")
	}
	db := raw.DB()

	const holdFor = 2 * time.Second

	// Steady state: hold the write lock elsewhere, then call
	// EnsureCategoryLabelMailboxes with the same, already-fully-backed
	// set. It must return long before the lock is released.
	releaseAt := time.Now().Add(holdFor)
	lockDone := acquireLockFor(t, db, releaseAt)

	start := time.Now()
	callErr := s.Meta().EnsureCategoryLabelMailboxes(ctx, p.ID, cats)
	elapsed := time.Since(start)
	if callErr != nil {
		t.Fatalf("steady-state EnsureCategoryLabelMailboxes: %v", callErr)
	}
	if elapsed >= holdFor/2 {
		t.Errorf("steady-state call took %v while a competing write lock was held for %v; want it to return immediately via the read-only fast path", elapsed, holdFor)
	}
	t.Logf("steady-state call (no missing mailbox) returned in %v while a %v write lock was held elsewhere", elapsed, holdFor)
	if err := <-lockDone; err != nil {
		t.Fatalf("lock holder: %v", err)
	}

	// Control: a category set missing one name must contend for the
	// write lock and therefore block until it is released, proving
	// the harness's lock is real contention.
	releaseAt2 := time.Now().Add(holdFor)
	lockDone2 := acquireLockFor(t, db, releaseAt2)

	start2 := time.Now()
	if err := s.Meta().EnsureCategoryLabelMailboxes(ctx, p.ID, append(cats, "promotions")); err != nil {
		t.Fatalf("control EnsureCategoryLabelMailboxes: %v", err)
	}
	elapsed2 := time.Since(start2)
	if err := <-lockDone2; err != nil {
		t.Fatalf("control lock holder: %v", err)
	}
	if elapsed2 < holdFor/2 {
		t.Errorf("control call (missing category) returned in %v while a %v write lock was held; want it to block for the write transaction, which would prove the harness's lock is real contention", elapsed2, holdFor)
	}
	t.Logf("control call (missing category) returned in %v while a %v write lock was held elsewhere", elapsed2, holdFor)
}
