package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	gmail "github.com/hanshuebner/herold/internal/import/gmail"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// newImportCmd registers `herold import <vendor>`. The umbrella verb
// reserves a place for future importers (Outlook PST, Apple Mail mbox,
// Maildir) without name churn; the only vendor wired in v1 is Gmail.
func newImportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "import",
		Short: "import mail and settings from another provider (gmail)",
	}
	c.AddCommand(newImportGmailCmd())
	return c
}

func newImportGmailCmd() *cobra.Command {
	var (
		principalRef string
		source       string
		localeFlag   string
		dryRun       bool
		skipMail     bool
		skipSettings bool
		fresh        bool
	)
	c := &cobra.Command{
		Use:   "gmail",
		Short: "import a Google Takeout (extracted) directory into a principal",
		Long: `Imports a Google Takeout export into the named principal:
mbox messages, X-Gmail-Labels mapping (locale auto-detected), Filter.json
translated to an inactive Sieve script "imported-from-gmail", blocked /
forwarding addresses recorded for review, delegated send-as addresses
created as JMAP Identity rows. Run on the herold host with the data
directory writable.

The --source path must be an extracted Takeout directory (run
"tar xzf takeout.tgz" first). Tarball-as-input is on the roadmap.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if principalRef == "" {
				return errors.New("--principal is required")
			}
			if source == "" {
				return errors.New("--source is required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			clk := clock.NewReal()
			st, err := openStoreBulk(ctx, cfg, discardLogger(), clk)
			if err != nil {
				return err
			}
			defer st.Close()

			principal, err := resolvePrincipalLocal(ctx, st, principalRef)
			if err != nil {
				return err
			}

			imp := gmail.Importer{
				Store:     st,
				Principal: principal,
				Logger:    importProgressLogger(g, cmd.ErrOrStderr()),
				Options: gmail.Options{
					Source:             source,
					Locale:             gmail.Locale(localeFlag),
					DryRun:             dryRun,
					SkipMail:           skipMail,
					SkipSettings:       skipSettings,
					FreshPrincipal:     fresh,
					InternalizeImports: importImagesPolicy(cfg),
				},
			}
			res, err := imp.Run(ctx)
			if err != nil {
				return fmt.Errorf("import gmail: %w", err)
			}
			return emitImportResult(cmd.OutOrStdout(), g, res, dryRun)
		},
	}
	c.Flags().StringVar(&principalRef, "principal", "", "target principal (canonical email)")
	c.Flags().StringVar(&source, "source", "", "extracted Takeout directory")
	c.Flags().StringVar(&localeFlag, "locale", "", "force a Gmail UI locale (en, de, fr, ...); empty = auto-detect")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report counts without writing to the store")
	c.Flags().BoolVar(&skipMail, "no-mail", false, "skip the mbox import")
	c.Flags().BoolVar(&skipSettings, "no-settings", false, "skip filters/blocked/forwarding/delegated")
	c.Flags().BoolVar(&fresh, "fresh", false, "assert the target principal has no existing messages; skip the start-up dedup scan")
	return c
}

// importImagesPolicy reads the operator's external-image import
// policy. The default (live mode "internalize") flags eligible
// imported messages for on-demand rewrite at first JMAP read
// (17-external-images.md REQ-EXTIMG-91/-93). Operators who do not
// want any rewrite — bulk or on-demand — set
// [external_images] mode = "passthrough" in system.toml; we map
// that to InternalizeImports="off". A future per-policy knob
// (REQ-EXTIMG-92) can decouple them; today they share the toggle.
func importImagesPolicy(cfg *sysconfig.Config) string {
	if cfg != nil && cfg.ExternalImages.Mode == sysconfig.ExternalImagesModePassthrough {
		return "off"
	}
	return "on_demand"
}

// importProgressLogger returns the slog.Logger the importer writes
// progress lines to. Behaviour:
//
//   - --quiet (g.quiet): discard everything; the operator only sees the
//     final summary on stdout.
//   - --json (g.jsonOut): JSON handler on stderr so the progress events
//     can be filtered alongside the final summary blob.
//   - default: TextHandler on stderr at info level. Emits one line per
//     1000-message progress checkpoint, plus the locale/scan/complete
//     events; per-message parse warnings appear at warn level.
//
// The handler is intentionally separate from the server's slog setup
// because the CLI is operator-tty bound and gets a different format
// from production daemon logs.
func importProgressLogger(g *globalOptions, stderr io.Writer) *slog.Logger {
	if g.quiet {
		return discardLogger()
	}
	level := slog.LevelInfo
	if g.logVerbose {
		level = slog.LevelDebug
	}
	if g.jsonOut {
		return slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
}

// resolvePrincipalLocal looks up the principal by canonical email
// against the directly-opened store. The CLI does not consult the REST
// surface; multi-GB import workloads stay process-local.
func resolvePrincipalLocal(ctx context.Context, st store.Store, ref string) (store.Principal, error) {
	p, err := st.Meta().GetPrincipalByEmail(ctx, strings.ToLower(strings.TrimSpace(ref)))
	if err != nil {
		return store.Principal{}, fmt.Errorf("resolve principal %q: %w", ref, err)
	}
	return p, nil
}

// emitImportResult writes a human or JSON summary of the import job.
// JSON output is the default in non-tty / --json mode; otherwise we
// render a small table to stdout.
func emitImportResult(w io.Writer, g *globalOptions, res gmail.Result, dryRun bool) error {
	if g.jsonOut || !isTerminal(w) {
		return json.NewEncoder(w).Encode(map[string]any{
			"locale":                       string(res.Locale),
			"messages_scanned":             res.MessagesScanned,
			"messages_imported":            res.MessagesImported,
			"messages_skipped_duplicate":   res.MessagesSkippedDuplicate,
			"messages_skipped_error":       res.MessagesSkippedError,
			"mailboxes_created":            res.MailboxesCreated,
			"filters_translated":           res.FiltersTranslated,
			"blocked_addresses":            res.BlockedAddresses,
			"forwarding_addresses":         res.ForwardingAddresses,
			"delegated_identities_created": res.DelegatedIdentitiesCreated,
			"first_errors":                 res.FirstErrors,
			"dry_run":                      dryRun,
		})
	}
	prefix := "imported"
	if dryRun {
		prefix = "would import (dry run)"
	}
	fmt.Fprintf(w, "Gmail import: %s\n", prefix)
	fmt.Fprintf(w, "  locale (auto-detected):  %s\n", res.Locale)
	fmt.Fprintf(w, "  messages scanned:        %d\n", res.MessagesScanned)
	fmt.Fprintf(w, "  messages imported:       %d\n", res.MessagesImported)
	fmt.Fprintf(w, "  duplicates skipped:      %d\n", res.MessagesSkippedDuplicate)
	fmt.Fprintf(w, "  per-message errors:      %d\n", res.MessagesSkippedError)
	fmt.Fprintf(w, "  mailboxes created:       %d\n", res.MailboxesCreated)
	fmt.Fprintf(w, "  filters translated:      %d  (Sieve script 'imported-from-gmail', inactive)\n", res.FiltersTranslated)
	fmt.Fprintf(w, "  blocked addresses:       %d  (recorded; opt in via Sieve script)\n", res.BlockedAddresses)
	fmt.Fprintf(w, "  forwarding addresses:    %d  (recorded; user must enable forwarding rules separately)\n", res.ForwardingAddresses)
	fmt.Fprintf(w, "  delegated identities:    %d  (created; require SMTP credentials before use)\n", res.DelegatedIdentitiesCreated)
	if n := len(res.FirstErrors); n > 0 {
		fmt.Fprintf(w, "  first errors (showing up to 32):\n")
		for _, e := range res.FirstErrors {
			fmt.Fprintf(w, "    offset=%d: %s\n", e.ByteOffset, e.Reason)
		}
	}
	return nil
}
