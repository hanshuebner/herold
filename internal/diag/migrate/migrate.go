package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/store"
)

// MigrateOptions configures a Migrate run.
type MigrateOptions struct {
	// Logger receives per-table progress and warning events. nil
	// falls back to slog.Default.
	Logger *slog.Logger
	// Clock supplies the migration manifest timestamp. nil falls
	// back to clock.NewReal.
	Clock clock.Clock
	// Progress, when non-nil, is invoked every ~1k rows with the
	// current table name and the rolling row count for that table.
	// Used by the CLI to render a progress bar.
	Progress func(table string, rowsDone int64)
}

// Migrate copies every row + blob from src into dst. Both stores
// MUST be at the same schema version; the target MUST be empty.
// Tables are inserted in FK-respecting order
// (backup.TableNames). Blob hashes are verified during copy.
//
// The function returns a manifest summarising the rows + blobs
// transferred.
func Migrate(ctx context.Context, src, dst store.Store, opts MigrateOptions) (backup.Manifest, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}

	srcBE, err := backup.BackendFor(src)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: source: %w", err)
	}
	dstBE, err := backup.BackendFor(dst)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: target: %w", err)
	}

	srcVer, err := srcBE.SchemaVersion(ctx)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: source schema version: %w", err)
	}
	dstVer, err := dstBE.SchemaVersion(ctx)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: target schema version: %w", err)
	}
	if srcVer != dstVer {
		return backup.Manifest{}, fmt.Errorf("migrate: schema version mismatch: source %d, target %d", srcVer, dstVer)
	}

	empty, err := dstBE.IsEmpty(ctx)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: probe target: %w", err)
	}
	if !empty {
		return backup.Manifest{}, errors.New("migrate: target store is not empty")
	}

	srcSnap, err := srcBE.Snapshot(ctx)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: snapshot source: %w", err)
	}
	defer srcSnap.Close()

	sink, err := dstBE.Restore(ctx)
	if err != nil {
		return backup.Manifest{}, fmt.Errorf("migrate: open sink: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sink.Rollback(ctx)
		}
	}()

	manifest := backup.Manifest{
		SchemaVersion: srcVer,
		BackupVersion: backup.CurrentBackupVersion,
		CreatedAt:     opts.Clock.Now().UTC(),
		Backend:       srcBE.Kind(),
		Tables:        map[string]int64{},
	}

	for _, table := range backup.TableNames {
		var copied int64
		err := srcSnap.EnumerateRows(ctx, table, func(row any) error {
			if err := sink.Insert(ctx, table, row); err != nil {
				return fmt.Errorf("migrate insert %s: %w", table, err)
			}
			copied++
			if opts.Progress != nil && copied%1000 == 0 {
				opts.Progress(table, copied)
			}
			return nil
		})
		if err != nil {
			return manifest, err
		}
		manifest.Tables[table] = copied
		if opts.Progress != nil && copied > 0 {
			opts.Progress(table, copied)
		}
		opts.Logger.LogAttrs(ctx, slog.LevelDebug, "migrate: table done",
			slog.String("table", table), slog.Int64("rows", copied))
	}

	// Blobs: stream from src.Blobs into dst.Blobs.
	srcBlobs := srcBE.Blobs()
	dstBlobs := dstBE.Blobs()
	err = srcSnap.EnumerateBlobHashes(ctx, func(hash string, size int64) error {
		rc, err := srcBlobs.Get(ctx, hash)
		if err != nil {
			return fmt.Errorf("migrate read blob %s: %w", hash, err)
		}
		defer rc.Close()
		ref, err := dstBlobs.Put(ctx, rc)
		if err != nil {
			return fmt.Errorf("migrate put blob %s: %w", hash, err)
		}
		// Cross-backend invariant: SQLite and Postgres both
		// canonicalise CRLF and BLAKE3-hash the result. When both
		// endpoints use the same canonical hash the new ref must
		// match the source hash exactly.
		if ref.Hash != hash {
			return fmt.Errorf("migrate: blob hash drift %s -> %s", hash, ref.Hash)
		}
		manifest.Blobs.Count++
		manifest.Blobs.Bytes += size
		return nil
	})
	if err != nil {
		return manifest, err
	}

	if err := sink.Commit(ctx); err != nil {
		return manifest, fmt.Errorf("migrate: commit: %w", err)
	}
	committed = true
	return manifest, nil
}
