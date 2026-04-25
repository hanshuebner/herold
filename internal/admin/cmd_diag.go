package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/diag/migrate"
	"github.com/hanshuebner/herold/internal/diag/restore"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// newDiagCmd assembles the `herold diag` subtree (backup / restore /
// migrate / verify). Each subcommand opens the source store from
// --system-config, performs the operation, and emits the manifest in
// human or JSON form depending on the global --json flag.
func newDiagCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "diag",
		Short: "diagnostic and migration utilities (backup, restore, migrate, verify)",
	}
	c.AddCommand(newDiagBackupCmd())
	c.AddCommand(newDiagRestoreCmd())
	c.AddCommand(newDiagMigrateCmd())
	c.AddCommand(newDiagVerifyCmd())
	// Wave 2.4 ops-observability surface: dns-check + collect leaves
	// live in their own files (cmd_diag_dns.go, cmd_diag_collect.go) so
	// the parallel backup-migrate agent's surface and the ops-observability
	// surface can evolve independently. The two surfaces share this single
	// `diag` cobra parent.
	c.AddCommand(newDiagDNSCheckCmd())
	c.AddCommand(newDiagCollectCmd())
	return c
}

func newDiagBackupCmd() *cobra.Command {
	var to string
	c := &cobra.Command{
		Use:   "backup",
		Short: "write a consistent backup bundle to --to <path>",
		Long: `Opens the configured store, snapshots it, and writes the bundle (manifest +
JSONL per table + content-addressed blobs/) to --to. Concurrent application
writes are allowed; the snapshot is consistent at the start of the run.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if to == "" {
				return fmt.Errorf("--to <path> is required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			clk := clock.NewReal()
			st, err := openStore(ctx, cfg, discardLogger(), clk)
			if err != nil {
				return err
			}
			defer st.Close()
			b := backup.New(backup.Options{
				Store:  st,
				Logger: discardLogger(),
				Clock:  clk,
			})
			m, err := b.CreateBundle(ctx, to)
			if err != nil {
				return err
			}
			return emitManifest(cmd.ErrOrStderr(), cmd.OutOrStdout(), g, "backup", m)
		},
	}
	c.Flags().StringVar(&to, "to", "", "destination directory (required)")
	return c
}

func newDiagRestoreCmd() *cobra.Command {
	var from string
	var modeStr string
	c := &cobra.Command{
		Use:   "restore",
		Short: "restore a bundle into the configured store",
		Long: `Reads the backup bundle at --from and inserts every row plus blob into the
configured store. --mode controls conflict handling: fresh (default) requires
the target be empty; merge skips existing rows; replace truncates first.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				return fmt.Errorf("--from <path> is required")
			}
			mode, err := restore.ParseMode(modeStr)
			if err != nil {
				return err
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			clk := clock.NewReal()
			st, err := openStore(ctx, cfg, discardLogger(), clk)
			if err != nil {
				return err
			}
			defer st.Close()
			r := restore.New(restore.Options{Store: st, Logger: discardLogger(), Clock: clk})
			m, err := r.RestoreBundle(ctx, from, mode)
			if err != nil {
				return err
			}
			return emitManifest(cmd.ErrOrStderr(), cmd.OutOrStdout(), g, "restore", m)
		},
	}
	c.Flags().StringVar(&from, "from", "", "source bundle directory (required)")
	c.Flags().StringVar(&modeStr, "mode", "fresh", "conflict mode: fresh|merge|replace")
	return c
}

func newDiagMigrateCmd() *cobra.Command {
	var toBackend string
	var toDSN string
	var toBlobDir string
	c := &cobra.Command{
		Use:   "migrate",
		Short: "copy every row + blob from the configured store into another backend",
		Long: `Opens the source store from --system-config and the target store from
--to-backend (sqlite|postgres) plus --to-dsn (Postgres DSN or SQLite path).
Both stores must be at the same schema version; the target must be empty.
Blob hashes are verified during copy; FK-respecting insert order is preserved.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if toBackend == "" {
				return fmt.Errorf("--to-backend is required")
			}
			if toDSN == "" {
				return fmt.Errorf("--to-dsn is required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			clk := clock.NewReal()
			src, err := openStore(ctx, cfg, discardLogger(), clk)
			if err != nil {
				return fmt.Errorf("open source: %w", err)
			}
			defer src.Close()

			dst, err := openTargetStore(ctx, toBackend, toDSN, toBlobDir, clk)
			if err != nil {
				return fmt.Errorf("open target: %w", err)
			}
			defer dst.Close()

			progress := func(table string, rowsDone int64) {
				if !g.quiet {
					fmt.Fprintf(cmd.ErrOrStderr(), "migrate: %s: %d rows\n", table, rowsDone)
				}
			}
			m, err := migrate.Migrate(ctx, src, dst, migrate.MigrateOptions{
				Logger:   discardLogger(),
				Clock:    clk,
				Progress: progress,
			})
			if err != nil {
				return err
			}
			return emitManifest(cmd.ErrOrStderr(), cmd.OutOrStdout(), g, "migrate", m)
		},
	}
	c.Flags().StringVar(&toBackend, "to-backend", "", "target backend: sqlite|postgres")
	c.Flags().StringVar(&toDSN, "to-dsn", "", "target DSN (sqlite path or postgres URL)")
	c.Flags().StringVar(&toBlobDir, "to-blob-dir", "", "target blob directory (defaults to <to-dsn>.blobs for sqlite, ./data/blobs for postgres)")
	return c
}

func newDiagVerifyCmd() *cobra.Command {
	var bundle string
	c := &cobra.Command{
		Use:   "verify",
		Short: "verify a bundle's manifest matches its JSONL row counts and blob hashes",
		Long:  "Reads --bundle, re-counts each JSONL, verifies blob hashes, and prints the manifest. Read-only; no store access.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if bundle == "" {
				return fmt.Errorf("--bundle <path> is required")
			}
			g := globals(cmd.Context())
			m, err := backup.VerifyBundle(cmd.Context(), bundle)
			if err != nil {
				return err
			}
			if err := backup.VerifyBundleHashes(cmd.Context(), bundle); err != nil {
				return err
			}
			return emitManifest(cmd.ErrOrStderr(), cmd.OutOrStdout(), g, "verify", m)
		},
	}
	c.Flags().StringVar(&bundle, "bundle", "", "bundle directory (required)")
	return c
}

// emitManifest prints the manifest to either stdout (JSON) or stderr
// (human progress) per the global --json flag. The op label
// distinguishes backup / restore / migrate / verify summaries in
// human-readable mode.
func emitManifest(stderr, stdout interface{ Write(p []byte) (int, error) },
	g *globalOptions, op string, m backup.Manifest) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "%s: backend=%s schema=%d backup_version=%d\n",
		op, m.Backend, m.SchemaVersion, m.BackupVersion)
	for _, t := range backup.TableNames {
		if c, ok := m.Tables[t]; ok && c > 0 {
			fmt.Fprintf(stderr, "  %s: %d rows\n", t, c)
		}
	}
	if m.Blobs.Count > 0 {
		fmt.Fprintf(stderr, "  blobs: %d files (%d bytes)\n", m.Blobs.Count, m.Blobs.Bytes)
	}
	if m.TotalBytes > 0 {
		fmt.Fprintf(stderr, "  total: %d bytes\n", m.TotalBytes)
	}
	return nil
}

// openTargetStore opens the migrate destination per --to-backend +
// --to-dsn. Returns the store handle plus an error; the caller
// closes the handle.
func openTargetStore(ctx context.Context, backend, dsn, blobDir string, clk clock.Clock) (store.Store, error) {
	logger := discardLogger()
	switch backend {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(dsn), 0o750); err != nil {
			return nil, fmt.Errorf("mkdir sqlite dir: %w", err)
		}
		return storesqlite.Open(ctx, dsn, logger, clk)
	case "postgres":
		if blobDir == "" {
			blobDir = "./data/blobs"
		}
		if err := os.MkdirAll(blobDir, 0o750); err != nil {
			return nil, fmt.Errorf("mkdir blob dir: %w", err)
		}
		return storepg.Open(ctx, dsn, blobDir, logger, clk)
	}
	return nil, fmt.Errorf("unknown backend %q", backend)
}

// silenceLoggerCompat avoids an "imported and not used" build error
// during piecewise development. Removed when slog is referenced
// elsewhere in this file (it already is via discardLogger).
var _ = slog.LevelInfo
