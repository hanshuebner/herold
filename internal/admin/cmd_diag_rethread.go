package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// newDiagRethreadCmd builds `herold diag rethread <email-or-id>`, an
// operator command that applies a threading-rule change to mail
// already stored (re #442). store.Metadata.RethreadPrincipal only
// fills in thread_id for messages that don't have one yet (the
// bulk-import / sub-account-migration convention); this command passes
// RethreadOptions.Force so already-threaded rows are recomputed too,
// bringing existing mail in line with whatever the current
// mailparse.SubjectsThreadTogether / NormalizeBaseSubject rule computes
// (REQ-STORE-40; see issue #437 for the rule change this lets an
// operator apply retroactively).
//
// --dry-run (the default) reports the number of messages that would be
// re-threaded and writes nothing, by asking the store to compute the
// same result set without applying it (RethreadOptions.DryRun) --
// rather than recomputing the count separately in the CLI layer, which
// could drift from what an apply run actually does. Pass --apply to
// perform the updates.
func newDiagRethreadCmd() *cobra.Command {
	var apply bool
	c := &cobra.Command{
		Use:   "rethread <email-or-id>",
		Short: "recompute a principal's message threads under the current threading rule",
		Long: `Opens the store from --system-config, resolves the principal by canonical
email or numeric ID, and recomputes thread_id for every one of its messages
using the current threading rule -- including messages that already carry a
thread_id assigned under an older rule. Use this after a threading-rule
change to bring already-stored mail in line with what new mail gets at
ingest.

Default is --dry-run: reports the number of messages that would be
re-threaded and writes nothing. Pass --apply to perform the updates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			pid, err := resolvePrincipalFromStore(ctx, st, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}

			mode := "dry-run"
			if apply {
				mode = "apply"
			}
			if !g.quiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "rethread: starting (%s)\n", mode)
			}

			n, err := st.Meta().RethreadPrincipal(ctx, pid, store.RethreadOptions{
				Force:  true,
				DryRun: !apply,
			})
			if err != nil {
				return fmt.Errorf("rethread: %w", err)
			}
			return emitRethreadSummary(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, mode, n)
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "perform the updates (default is --dry-run: report only, write nothing)")
	return c
}

// emitRethreadSummary prints the run's result to stdout (JSON, when
// --json is set) or stderr (human-readable), matching the --json
// convention used by the other diag subcommands (see
// emitRecomputeBodyMetaSummary / emitReparseSummary).
func emitRethreadSummary(stdout, stderr interface{ Write(p []byte) (int, error) }, g *globalOptions, mode string, n int) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode       string `json:"mode"`
			Rethreaded int    `json:"rethreaded"`
		}{Mode: mode, Rethreaded: n})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "rethread: done (%s)\n", mode)
	fmt.Fprintf(stderr, "  rethreaded: %d\n", n)
	return nil
}
