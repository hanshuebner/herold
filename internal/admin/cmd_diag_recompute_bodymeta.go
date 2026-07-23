package admin

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/recomputebodymeta"
)

// newDiagRecomputeBodyMetaCmd builds `herold diag recompute-bodymeta`,
// a once-off maintenance pass that re-derives each message's
// persisted body metadata (Email.preview + has_attachment) from its
// raw blob. It repairs rows ingested before the mailparse.BodyPreview
// HTML-fallback fix (commit 8dffa63c, re #263), whose HTML-only
// bodies had their preview persisted as raw, untruncated HTML instead
// of extracted text.
//
// --dry-run (the default) reports what would change and writes
// nothing; --apply performs the writes. See
// internal/recomputebodymeta for the overwrite-when-changed safety
// rule: a row is rewritten only when the recomputed preview or
// has_attachment differs from what is currently stored, so a run is
// idempotent.
func newDiagRecomputeBodyMetaCmd() *cobra.Command {
	var apply bool
	var pageSize int
	c := &cobra.Command{
		Use:   "recompute-bodymeta",
		Short: "re-derive persisted message body metadata (Email.preview, has_attachment) from raw blobs",
		Long: `Walks every message in the configured store, reparses its raw blob, and
overwrites the stored preview/has_attachment whenever the recomputed value
(using the current mailparse.BodyPreview / mailparse.HasAttachment logic)
differs from what is currently stored. A row whose recomputed value already
matches storage is left untouched, so a run is idempotent and safe to repeat.

Reports totals (scanned / changed / applied / skipped-parse-error /
skipped-no-blob) plus a sample of up to 20 changed previews.

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
			progress := func(sum recomputebodymeta.Summary) {
				pages++
				if g.quiet {
					return
				}
				// Print every 10th page so a multi-million-row production
				// table doesn't flood the terminal while the operator
				// still sees the run moving.
				if pages%10 == 0 {
					fmt.Fprintf(out, "recompute-bodymeta: scanned=%d changed=%d applied=%d\n",
						sum.Scanned, sum.Changed, sum.Applied)
				}
			}

			mode := "dry-run"
			if apply {
				mode = "apply"
			}
			if !g.quiet {
				fmt.Fprintf(out, "recompute-bodymeta: starting (%s)\n", mode)
			}

			sum, err := recomputebodymeta.Run(ctx, st, recomputebodymeta.Options{
				Apply:    apply,
				PageSize: pageSize,
				Progress: progress,
			})
			if err != nil {
				return fmt.Errorf("recompute-bodymeta: %w", err)
			}
			return emitRecomputeBodyMetaSummary(cmd.OutOrStdout(), out, g, mode, sum)
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "perform the updates (default is --dry-run: report only, write nothing)")
	c.Flags().IntVar(&pageSize, "page-size", 0, "messages per page (default: a modest built-in value)")
	return c
}

// emitRecomputeBodyMetaSummary prints the run's Summary to stdout
// (JSON, when --json is set) or stderr (human-readable), matching the
// --json convention used by the other diag subcommands (see
// emitManifest / emitReparseSummary).
func emitRecomputeBodyMetaSummary(stdout, stderr interface{ Write(p []byte) (int, error) }, g *globalOptions, mode string, sum recomputebodymeta.Summary) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode string `json:"mode"`
			recomputebodymeta.Summary
		}{Mode: mode, Summary: sum})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "recompute-bodymeta: done (%s)\n", mode)
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
			fmt.Fprintf(stderr, "    %d: %q (has_attachment=%v) -> %q (has_attachment=%v)\n",
				s.ID, s.OldPreview, s.OldHasAtt, s.NewPreview, s.NewHasAtt)
		}
	}
	return nil
}
