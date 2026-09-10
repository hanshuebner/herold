package admin

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
)

func newSpamCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "spam",
		Short: "spam classifier policy inspection and update",
	}
	c.AddCommand(&cobra.Command{
		Use:   "policy-show",
		Short: "print the active spam policy (plugin, threshold, model)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/spam/policy", nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeSpamPolicyHuman(cmd.OutOrStdout(), out)
		},
	})

	setCmd := &cobra.Command{
		Use:   "policy-set",
		Short: "update the active spam policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			plugin, _ := cmd.Flags().GetString("plugin")
			threshold, _ := cmd.Flags().GetFloat64("threshold")
			model, _ := cmd.Flags().GetString("model")
			promptFile, _ := cmd.Flags().GetString("system-prompt-file")
			if plugin == "" {
				return errors.New("spam policy-set: --plugin is required")
			}
			if threshold < 0 || threshold > 1 {
				return errors.New("spam policy-set: --threshold must be in [0.0, 1.0]")
			}
			body := map[string]any{
				"plugin_name": plugin,
				"threshold":   threshold,
			}
			if model != "" {
				body["model"] = model
			}
			if promptFile != "" {
				raw, err := os.ReadFile(promptFile)
				if err != nil {
					return fmt.Errorf("read system-prompt-file %q: %w", promptFile, err)
				}
				body["system_prompt_override"] = string(raw)
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "PUT", "/api/v1/spam/policy", body, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeSpamPolicyHuman(cmd.OutOrStdout(), out)
		},
	}
	setCmd.Flags().String("plugin", "", "plugin name (required)")
	setCmd.Flags().Float64("threshold", 0.7, "spam-score threshold in [0.0, 1.0]")
	setCmd.Flags().String("model", "", "optional classifier model identifier")
	setCmd.Flags().String("system-prompt-file", "", "path to a file containing a system-prompt override")
	c.AddCommand(setCmd)
	c.AddCommand(newSpamApplyVerdictsCmd())
	return c
}

// newSpamApplyVerdictsCmd builds `herold spam apply-verdicts`, a
// store-backed maintenance command (opens the store from
// --system-config, like `diag reparse-envelopes` / `diag
// recompute-bodymeta`; no admin server needed) that applies the
// results of an offline batch spam-classification run to a
// principal's stored mail. See applySpamVerdicts / undoSpamVerdicts
// in spam_apply_verdicts.go for the row-level semantics.
func newSpamApplyVerdictsCmd() *cobra.Command {
	var csvPath, engine, undoLogPath, undoPath string
	var dryRun bool
	var minConfidence float64
	c := &cobra.Command{
		Use:   "apply-verdicts <email-or-id>",
		Short: "apply an offline batch spam-classification CSV to a principal's stored mail",
		Long: `Reads a batch-classification CSV (--csv) with a header row naming at least
id (herold messages.id), verdict (spam or ham), confidence (a 0..100
integer or 0..1 float, normalised to 0..1), and reason (free text, may
be empty); extra columns are ignored.

For every row whose message belongs to the given principal: the verdict is
recorded in the message's llm_classifications spam sub-record (--engine
names the model/engine recorded there; an existing record is overwritten).
A spam row whose message is already in a Junk- or Trash-attributed mailbox
is left in place; otherwise, when its confidence is at or above
--min-confidence, the message is moved into the principal's Junk mailbox,
keeping only Sent/Drafts memberships alongside it, using the same
MoveMessage / AddMessageToMailbox / RemoveMessageFromMailbox primitives a
client-driven move uses. Rows for a different principal or an unknown
message id are counted and skipped. delivery_disposition is never touched.

--undo-log <path> records, before each move, the message id and its
previous mailbox ids; pass --undo <path> in a later invocation to restore
those memberships from such a log. --dry-run reports the summary without
writing the classification record, moving anything, or writing --undo-log.`,
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

			p, err := resolveStorePrincipal(ctx, st, args[0])
			if err != nil {
				return err
			}

			if undoPath != "" {
				if csvPath != "" {
					return errors.New("apply-verdicts: --undo cannot be combined with --csv")
				}
				f, err := os.Open(undoPath)
				if err != nil {
					return fmt.Errorf("open undo log %q: %w", undoPath, err)
				}
				defer f.Close()
				rows, err := parseSpamUndoCSV(f)
				if err != nil {
					return fmt.Errorf("parse undo log %q: %w", undoPath, err)
				}
				sum, err := undoSpamVerdicts(ctx, st, p.ID, rows, dryRun)
				if err != nil {
					return err
				}
				return emitSpamUndoVerdictsSummary(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, dryRun, sum)
			}

			if csvPath == "" {
				return errors.New("apply-verdicts: --csv <path> is required (or use --undo <path>)")
			}
			f, err := os.Open(csvPath)
			if err != nil {
				return fmt.Errorf("open csv %q: %w", csvPath, err)
			}
			defer f.Close()
			rows, err := parseSpamVerdictsCSV(f)
			if err != nil {
				return fmt.Errorf("parse csv %q: %w", csvPath, err)
			}

			var undoWriter *csv.Writer
			if undoLogPath != "" && !dryRun {
				undoFile, err := os.Create(undoLogPath)
				if err != nil {
					return fmt.Errorf("create undo log %q: %w", undoLogPath, err)
				}
				defer undoFile.Close()
				undoWriter = csv.NewWriter(undoFile)
				if err := undoWriter.Write([]string{"message_id", "previous_mailbox_ids"}); err != nil {
					return fmt.Errorf("write undo log header: %w", err)
				}
			}

			engineName := engine
			if engineName == "" {
				engineName = "batch"
			}

			sum, err := applySpamVerdicts(ctx, st, clk, p.ID, rows, spamApplyVerdictsOptions{
				Engine:        engineName,
				DryRun:        dryRun,
				MinConfidence: minConfidence,
				UndoLog:       undoWriter,
			})
			if undoWriter != nil {
				undoWriter.Flush()
				if ferr := undoWriter.Error(); ferr != nil && err == nil {
					err = fmt.Errorf("flush undo log %q: %w", undoLogPath, ferr)
				}
			}
			if err != nil {
				return err
			}
			return emitSpamApplyVerdictsSummary(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, dryRun, sum)
		},
	}
	c.Flags().StringVar(&csvPath, "csv", "", "path to the batch-classification CSV (id,verdict,confidence,reason)")
	c.Flags().StringVar(&engine, "engine", "batch", "engine/model name recorded on the llm_classifications row")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report the summary without writing anything")
	c.Flags().Float64Var(&minConfidence, "min-confidence", 0, "skip moving spam rows below this confidence (0..1, normalised)")
	c.Flags().StringVar(&undoLogPath, "undo-log", "", "write an undo log (message id + previous mailbox ids) before each move")
	c.Flags().StringVar(&undoPath, "undo", "", "restore memberships from a previously written --undo-log instead of applying a CSV")
	return c
}

// emitSpamApplyVerdictsSummary prints the apply-verdicts run's summary
// to stdout (JSON, when --json is set) or stderr (human-readable),
// matching the --json convention used by the diag maintenance commands.
func emitSpamApplyVerdictsSummary(stdout, stderr io.Writer, g *globalOptions, dryRun bool, sum SpamApplyVerdictsSummary) error {
	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode string `json:"mode"`
			SpamApplyVerdictsSummary
		}{Mode: mode, SpamApplyVerdictsSummary: sum})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "apply-verdicts: done (%s)\n", mode)
	fmt.Fprintf(stderr, "  rows read:               %d\n", sum.RowsRead)
	fmt.Fprintf(stderr, "  applied:                 %d\n", sum.Applied)
	fmt.Fprintf(stderr, "  moved:                   %d\n", sum.Moved)
	fmt.Fprintf(stderr, "  already junk:            %d\n", sum.AlreadyJunk)
	fmt.Fprintf(stderr, "  skipped unknown:         %d\n", sum.SkippedUnknown)
	fmt.Fprintf(stderr, "  skipped other principal: %d\n", sum.SkippedOtherPrincipal)
	return nil
}

// emitSpamUndoVerdictsSummary is emitSpamApplyVerdictsSummary's
// counterpart for `spam apply-verdicts --undo`.
func emitSpamUndoVerdictsSummary(stdout, stderr io.Writer, g *globalOptions, dryRun bool, sum SpamUndoVerdictsSummary) error {
	mode := "undo"
	if dryRun {
		mode = "dry-run"
	}
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode string `json:"mode"`
			SpamUndoVerdictsSummary
		}{Mode: mode, SpamUndoVerdictsSummary: sum})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "apply-verdicts --undo: done (%s)\n", mode)
	fmt.Fprintf(stderr, "  rows read:               %d\n", sum.RowsRead)
	fmt.Fprintf(stderr, "  restored:                %d\n", sum.Restored)
	fmt.Fprintf(stderr, "  skipped unknown:         %d\n", sum.SkippedUnknown)
	fmt.Fprintf(stderr, "  skipped other principal: %d\n", sum.SkippedOtherPrincipal)
	return nil
}
