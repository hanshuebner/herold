package admin

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/reparseenvelopes"
)

// newDiagReparseEnvelopesCmd builds `herold diag reparse-envelopes`, a
// once-off maintenance pass that re-derives each message's cached
// Envelope columns from its raw blob. It repairs rows ingested before
// the mailparse header-charset fix (commit 2a8ae001, re #257) whose
// Subject -- and, by the same stored-envelope trap, From/Message-ID/
// Date (re #244) -- were decoded to empty and persisted that way.
//
// --dry-run (the default) reports what would change and writes
// nothing; --apply performs the writes. See internal/reparseenvelopes
// for the merge/safety rules: only currently-empty stored fields are
// filled, so a run is idempotent and safe against concurrent writes.
func newDiagReparseEnvelopesCmd() *cobra.Command {
	var apply bool
	var pageSize int
	c := &cobra.Command{
		Use:   "reparse-envelopes",
		Short: "re-derive cached message envelopes (subject/from/message-id/date) from raw blobs",
		Long: `Walks every message in the configured store, reparses its raw blob, and
fills any empty envelope field (env_subject, env_from, env_message_id, env_date_us,
etc.) from the freshly-parsed value. A currently non-empty stored field is never
overwritten, so a run is idempotent and safe to repeat.

Reports totals (scanned / changed / applied / skipped-parse-error /
skipped-no-blob) plus a sample of up to 20 changed subjects.

Default is --dry-run: reports only, writes nothing. Pass --apply to perform
the updates.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			out := cmd.ErrOrStderr()
			pages := 0
			progress := func(sum reparseenvelopes.Summary) {
				pages++
				if g.quiet {
					return
				}
				// Print every 10th page so a multi-million-row production
				// table doesn't flood the terminal while the operator
				// still sees the run moving.
				if pages%10 == 0 {
					fmt.Fprintf(out, "reparse-envelopes: scanned=%d changed=%d applied=%d\n",
						sum.Scanned, sum.Changed, sum.Applied)
				}
			}

			mode := "dry-run"
			if apply {
				mode = "apply"
			}
			if !g.quiet {
				fmt.Fprintf(out, "reparse-envelopes: starting (%s)\n", mode)
			}

			sum, err := reparseenvelopes.Run(ctx, st, reparseenvelopes.Options{
				Apply:    apply,
				PageSize: pageSize,
				Progress: progress,
			})
			if err != nil {
				return fmt.Errorf("reparse-envelopes: %w", err)
			}
			return emitReparseSummary(cmd.OutOrStdout(), out, g, mode, sum)
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "perform the updates (default is --dry-run: report only, write nothing)")
	c.Flags().IntVar(&pageSize, "page-size", 0, "messages per page (default: a modest built-in value)")
	return c
}

// emitReparseSummary prints the run's Summary to stdout (JSON, when
// --json is set) or stderr (human-readable), matching the --json
// convention used by the other diag subcommands (see emitManifest).
func emitReparseSummary(stdout, stderr interface{ Write(p []byte) (int, error) }, g *globalOptions, mode string, sum reparseenvelopes.Summary) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode string `json:"mode"`
			reparseenvelopes.Summary
		}{Mode: mode, Summary: sum})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "reparse-envelopes: done (%s)\n", mode)
	fmt.Fprintf(stderr, "  scanned:             %d\n", sum.Scanned)
	fmt.Fprintf(stderr, "  changed:             %d\n", sum.Changed)
	fmt.Fprintf(stderr, "  applied:             %d\n", sum.Applied)
	fmt.Fprintf(stderr, "  skipped-parse-error: %d\n", sum.SkippedParseError)
	fmt.Fprintf(stderr, "  skipped-no-blob:     %d\n", sum.SkippedNoBlob)
	if sum.WriteErrors > 0 {
		fmt.Fprintf(stderr, "  write-errors:        %d\n", sum.WriteErrors)
	}
	if len(sum.Samples) > 0 {
		fmt.Fprintf(stderr, "  sample changes (%d):\n", len(sum.Samples))
		for _, s := range sum.Samples {
			fmt.Fprintf(stderr, "    %d: %q -> %q\n", s.ID, s.OldSubject, s.NewSubject)
		}
	}
	return nil
}
