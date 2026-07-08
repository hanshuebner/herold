package storesqlite_test

// TestConcurrentWriteNoBUSY is a regression guard for the SQLITE_BUSY
// upgrade-deadlock that surfaced in CI as a flaky TestExternalIdentity_E2E
// failure (herold/herold CI run #562, job "test (arm64 / postgres)", sqlite
// sub-case). Root cause: InsertEmailSubmission runs inside runTx, which
// issued a deferred BEGIN. The SELECT inside the transaction started a WAL
// read snapshot; when the subsequent INSERT tried to upgrade to the write
// lock while a concurrent non-writerMu writer (e.g. IncRefBlob) held it,
// SQLite returned SQLITE_BUSY immediately — the busy handler (and therefore
// busy_timeout) is not invoked for lock upgrades inside a deferred
// transaction in WAL mode.
//
// The fix adds _txlock=immediate to buildDSN so every BeginTx issues BEGIN
// IMMEDIATE. The write lock is acquired at transaction start, busy_timeout
// governs contention, and the upgrade path is eliminated.
//
// This test reliably produced SQLITE_BUSY errors before the fix and must
// produce zero errors after.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

func TestConcurrentWriteNoBUSY(t *testing.T) {
	s, cleanup := openStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a principal — required by the jmap_email_submissions FK.
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "busy-test@example.test",
		DisplayName:    "Busy Test",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	// Pre-register a blob so IncRefBlob hits the UPDATE path (which writes
	// without taking writerMu, exercising the concurrent-writer scenario).
	const blobHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.Meta().IncRefBlob(ctx, blobHash, 1024); err != nil {
		t.Fatalf("setup IncRefBlob: %v", err)
	}

	const (
		submissionWorkers = 10
		blobWorkers       = 10
		itersPerWorker    = 30
	)

	var (
		wg       sync.WaitGroup
		busyErrs atomic.Int64
	)

	isBUSY := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "SQLITE_BUSY")
	}

	// Goroutines that call InsertEmailSubmission — each call goes through
	// runTx (writerMu + BeginTx) and does SELECT then INSERT.
	for w := range submissionWorkers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := range itersPerWorker {
				id := fmt.Sprintf("sub-w%d-j%d", w, j)
				row := store.EmailSubmissionRow{
					ID:          id,
					EnvelopeID:  store.EnvelopeID(id),
					PrincipalID: p.ID,
					IdentityID:  "ident1",
					UndoStatus:  "pending",
				}
				if err := s.Meta().InsertEmailSubmission(ctx, row); isBUSY(err) {
					busyErrs.Add(1)
				}
			}
		}(w)
	}

	// Goroutines that call IncRefBlob — these open their own BeginTx
	// without taking writerMu, so they can hold the write lock concurrently
	// with a runTx SELECT, which is the exact race that produced BUSY.
	for range blobWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range itersPerWorker {
				// IncRefBlob alternates UPDATE and INSERT internally;
				// both paths write without writerMu.
				_ = s.Meta().IncRefBlob(ctx, blobHash, 1024)
			}
		}()
	}

	wg.Wait()

	if n := busyErrs.Load(); n > 0 {
		t.Errorf("got %d SQLITE_BUSY errors under concurrent InsertEmailSubmission + IncRefBlob load; want 0 (BEGIN IMMEDIATE must prevent upgrade deadlocks)", n)
	}
}
