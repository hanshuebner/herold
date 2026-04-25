package restore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/zeebo/blake3"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/store"
)

// Mode controls how Restore handles existing rows / blobs in the
// target store.
type Mode int

const (
	// ModeFresh requires the target be empty; aborts otherwise.
	ModeFresh Mode = iota
	// ModeMerge inserts rows that don't already exist; existing rows
	// are skipped without error.
	ModeMerge
	// ModeReplace truncates every backed-up table before inserting.
	ModeReplace
)

// String returns the canonical lowercase token for m. Used by the
// CLI flag parser.
func (m Mode) String() string {
	switch m {
	case ModeFresh:
		return "fresh"
	case ModeMerge:
		return "merge"
	case ModeReplace:
		return "replace"
	}
	return "unknown"
}

// ParseMode parses a CLI-supplied mode token. Returns an error on an
// unknown value.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(s) {
	case "fresh", "":
		return ModeFresh, nil
	case "merge":
		return ModeMerge, nil
	case "replace":
		return ModeReplace, nil
	}
	return 0, fmt.Errorf("restore: unknown mode %q", s)
}

// Options configures a Restore.
type Options struct {
	Store  store.Store
	Logger *slog.Logger
	Clock  clock.Clock
}

// Restore is the top-level controller for reading a bundle.
type Restore struct {
	opts Options
}

// New returns a Restore configured with opts.
func New(opts Options) *Restore {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	return &Restore{opts: opts}
}

// RestoreBundle reads the bundle at src and applies it to the
// configured target store under the given Mode. Returns the bundle's
// manifest on success.
func (r *Restore) RestoreBundle(ctx context.Context, src string, mode Mode) (backup.Manifest, error) {
	manifest, err := backup.ReadManifest(src)
	if err != nil {
		return backup.Manifest{}, err
	}
	if manifest.BackupVersion > backup.CurrentBackupVersion {
		return manifest, fmt.Errorf("restore: bundle version %d is newer than this binary (max %d)",
			manifest.BackupVersion, backup.CurrentBackupVersion)
	}

	be, err := backup.BackendFor(r.opts.Store)
	if err != nil {
		return manifest, err
	}

	switch mode {
	case ModeFresh:
		empty, err := be.IsEmpty(ctx)
		if err != nil {
			return manifest, fmt.Errorf("restore: probe target: %w", err)
		}
		if !empty {
			return manifest, errors.New("restore: target is not empty (use --mode replace or merge)")
		}
	case ModeReplace:
		if err := be.TruncateAll(ctx); err != nil {
			return manifest, fmt.Errorf("restore: truncate: %w", err)
		}
	case ModeMerge:
		// Nothing to pre-clean; the Sink swallows duplicate-key errors.
	default:
		return manifest, fmt.Errorf("restore: unknown mode %d", mode)
	}

	sink, err := be.Restore(ctx)
	if err != nil {
		return manifest, fmt.Errorf("restore: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sink.Rollback(ctx)
		}
	}()

	for _, table := range backup.TableNames {
		if err := restoreTable(ctx, sink, src, table, mode, r.opts.Logger); err != nil {
			return manifest, fmt.Errorf("restore: %s: %w", table, err)
		}
	}

	if err := restoreBlobs(ctx, src, be.Blobs(), manifest); err != nil {
		return manifest, fmt.Errorf("restore: blobs: %w", err)
	}

	if err := sink.Commit(ctx); err != nil {
		return manifest, fmt.Errorf("restore: commit: %w", err)
	}
	committed = true
	return manifest, nil
}

// restoreTable streams one table's JSONL into the Sink.
func restoreTable(ctx context.Context, sink backup.Sink, src, table string, mode Mode, logger *slog.Logger) error {
	_, err := backup.ReadTableJSONL(src, table, func(row any) error {
		if err := sink.Insert(ctx, table, row); err != nil {
			if mode == ModeMerge && isDuplicate(err) {
				logger.LogAttrs(ctx, slog.LevelDebug, "restore: skip duplicate",
					slog.String("table", table))
				return nil
			}
			return err
		}
		return nil
	})
	return err
}

// restoreBlobs copies every blob file from <src>/blobs/ into the
// target store via Blobs.Put. Each blob is BLAKE3-verified during
// the copy.
func restoreBlobs(ctx context.Context, src string, blobs store.Blobs, manifest backup.Manifest) error {
	root := filepath.Join(src, "blobs")
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Skip dotfiles and stray garbage; valid blob filenames are
		// 64-char lowercase hex.
		if len(info.Name()) != 64 {
			return nil
		}
		// Stream the file through Blobs.Put. The blob store
		// re-canonicalises CRLF and re-hashes; for SQL-backed
		// bundles BLAKE3 lines up. For fakestore bundles (SHA-256)
		// the filename is treated as opaque and the new store
		// computes its own hash. The manifest carries Backend so
		// callers know what to expect.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		// Verify the file's BLAKE3 against its name when the
		// producing backend was BLAKE3 (sqlite/postgres). Mismatch
		// signals corruption and aborts.
		if manifest.Backend != "fakestore" {
			h := blake3.New()
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
			got := hex.EncodeToString(h.Sum(nil))
			if got != info.Name() {
				return fmt.Errorf("restore: blob %s hash mismatch (got %s)", info.Name(), got)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
		if _, err := blobs.Put(ctx, f); err != nil {
			return fmt.Errorf("restore: put blob %s: %w", info.Name(), err)
		}
		return nil
	})
}

// isDuplicate returns true when err looks like a unique-constraint
// violation surfaced by the SQL drivers. Used by ModeMerge.
func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrConflict) {
		return true
	}
	msg := err.Error()
	// SQLite (modernc) and Postgres (pgx) both spell unique-constraint
	// failures with substrings we can pattern-match. The strings are
	// stable enough to be exact: pgx surfaces "duplicate key value
	// violates unique constraint", SQLite surfaces "UNIQUE constraint
	// failed".
	for _, s := range []string{
		"UNIQUE constraint failed",
		"PRIMARY KEY constraint failed",
		"duplicate key value violates unique constraint",
		"violates unique constraint",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
