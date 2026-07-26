package storepg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// TestMigration0100RetryableBackfill is storesqlite's
// TestMigration0100RetryableBackfill counterpart for Postgres: a
// message written before migration 0100 ran (only failed_image_count
// set, the new retryable_failed_image_count / failed_image_reason
// columns at their table defaults 0/”) must read
// retryableFailedImageCount == failedImageCount and a null
// failedImageReason after the migration's UPDATE runs (issue #271) --
// preserving today's "offer retry whenever failedImageCount > 0"
// behaviour for legacy rows instead of silently making them look
// permanently failed.
func TestMigration0100RetryableBackfill(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres integration test")
	}

	// Guard against Migration0100BackfillSQL (a hand-copied constant,
	// re-exported separately because the ALTER TABLE statements that
	// precede it in the real file cannot be re-run against an
	// already-migrated DB) drifting from the production migration text.
	if !strings.Contains(storepg.Migration0100SQL, storepg.Migration0100BackfillSQL) {
		t.Fatalf("Migration0100BackfillSQL is not a substring of the real 0100 migration body -- update the exported constant")
	}

	ctx := context.Background()
	s, cleanup := openStore(t, dsn)
	defer cleanup()
	pg, ok := s.(*storepg.Store)
	if !ok {
		t.Fatalf("openStore returned %T, want *storepg.Store", s)
	}

	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "legacy-failedimages@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	// Simulate a pre-#271 write: insert a raw messages row carrying only
	// failed_image_count, leaving retryable_failed_image_count and
	// failed_image_reason at the column defaults migration 0100's ALTER
	// TABLE would have assigned every existing row (0 and '').
	pool := pg.Pool()
	var rawID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO messages (principal_id, blob_hash, blob_size,
		  internal_date_us, received_at_us, size, failed_image_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		int64(p.ID), "deadbeef", 10, 1, 1, 10, 3).Scan(&rawID); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	mid := store.MessageID(rawID)

	// Before the back-fill: exactly what an unmigrated read would show.
	before, err := s.Meta().GetMessage(ctx, mid)
	if err != nil {
		t.Fatalf("GetMessage before backfill: %v", err)
	}
	if before.FailedImageCount != 3 {
		t.Fatalf("FailedImageCount = %d, want 3", before.FailedImageCount)
	}
	if before.RetryableFailedImageCount != 0 || before.FailedImageReason != "" {
		t.Fatalf("pre-backfill row should start at defaults, got retryable=%d reason=%q",
			before.RetryableFailedImageCount, before.FailedImageReason)
	}

	// Apply the migration's back-fill UPDATE, as would happen on
	// upgrade for a row that already existed when 0100 ran.
	if _, err := pool.Exec(ctx, storepg.Migration0100BackfillSQL); err != nil {
		t.Fatalf("apply backfill: %v", err)
	}

	after, err := s.Meta().GetMessage(ctx, mid)
	if err != nil {
		t.Fatalf("GetMessage after backfill: %v", err)
	}
	if after.RetryableFailedImageCount != after.FailedImageCount {
		t.Errorf("RetryableFailedImageCount = %d, want %d (== FailedImageCount, preserving legacy retry behaviour)",
			after.RetryableFailedImageCount, after.FailedImageCount)
	}
	if after.FailedImageReason != "" {
		t.Errorf("FailedImageReason = %q, want empty (no permanent category can be claimed for a legacy row)", after.FailedImageReason)
	}
}
