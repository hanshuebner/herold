package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
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
	c.AddCommand(newSpamReclassifyCmd())
	c.AddCommand(newSpamShowCmd())
	return c
}

// newSpamShowCmd builds `herold spam show <message-id>`, a store-backed
// read command (opens the store from --system-config, like `spam
// reclassify` / `diag reparse-envelopes`; no admin server needed) that
// prints one message's recorded llm_classifications outcome (re #326):
// the observability gap this command closes is that a classifier
// timeout, error, or unparseable-output outcome used to leave no
// record at all, indistinguishable after the fact from a genuine ham
// verdict.
func newSpamShowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "show <message-id>",
		Short: "print the recorded spam-classification outcome for one message",
		Long: `Looks up the numeric store message id's llm_classifications row
(the same id shown by ` + "`message-research`" + ` and stored-message listings)
and prints verdict, confidence, reason, model, and when it ran.

reason is the plugin's own one-sentence explanation for a genuine
ham/spam/suspect verdict, or a "<class>: <detail>" string when verdict is
"unclassified" and the classifier was actually invoked and failed
(class one of timeout, plugin_error, unparseable, not_configured).
"unclassified" with no reason and no recorded row at all both mean the
classifier was never invoked for this message.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			mid, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("spam show: invalid message id %q: %w", args[0], err)
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return err
			}
			defer st.Close()

			rec, err := st.Meta().GetLLMClassification(ctx, store.MessageID(mid))
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("spam show: no classification record for message %d (the classifier was never invoked for it, or the message id does not exist)", mid)
				}
				return fmt.Errorf("spam show: %w", err)
			}
			return writeResult(cmd.OutOrStdout(), g, llmClassificationRecordToMap(rec))
		},
	}
	return c
}

// llmClassificationRecordToMap renders rec as the flat map writeResult
// expects (JSON when --json/non-terminal, aligned key/value lines
// otherwise). Nil fields are omitted rather than rendered as null/empty.
func llmClassificationRecordToMap(rec store.LLMClassificationRecord) map[string]any {
	out := map[string]any{
		"message_id":   uint64(rec.MessageID),
		"principal_id": uint64(rec.PrincipalID),
	}
	if rec.SpamVerdict != nil {
		out["spam_verdict"] = *rec.SpamVerdict
	}
	if rec.SpamConfidence != nil {
		out["spam_confidence"] = *rec.SpamConfidence
	}
	if rec.SpamReason != nil {
		out["spam_reason"] = *rec.SpamReason
	}
	if rec.SpamModel != nil {
		out["spam_model"] = *rec.SpamModel
	}
	if rec.SpamClassifiedAt != nil {
		out["spam_classified_at"] = rec.SpamClassifiedAt.UTC().Format(time.RFC3339)
	}
	if rec.CategoryAssigned != nil {
		out["category_assigned"] = *rec.CategoryAssigned
	}
	if rec.CategoryModel != nil {
		out["category_model"] = *rec.CategoryModel
	}
	if rec.CategoryClassifiedAt != nil {
		out["category_classified_at"] = rec.CategoryClassifiedAt.UTC().Format(time.RFC3339)
	}
	return out
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

// newSpamReclassifyCmd builds `herold spam reclassify`, the online
// counterpart to `herold spam apply-verdicts` (issue #318): a
// store-backed maintenance command in the same family (opens the store
// from --system-config, no admin server needed) that starts the
// operator's configured classifier plugin itself -- the same way
// admin.StartServer wires it for SMTP delivery -- and re-runs it over a
// principal's already-stored mail, for messages that missed
// classification during a plugin outage or on a freshly enabled
// classifier. See reclassifySpam in spam_reclassify.go for the
// row-level semantics; the routing and undo-log format are shared with
// apply-verdicts.
func newSpamReclassifyCmd() *cobra.Command {
	var since, engine, undoLogPath, undoPath string
	var unclassifiedOnly, dryRun bool
	var limit int
	c := &cobra.Command{
		Use:   "reclassify <email-or-id>",
		Short: "re-run the configured classifier plugin over a principal's stored mail",
		Long: `Selects the given principal's messages (every mailbox, deduplicated)
received at or after --since (an RFC3339 timestamp, or a duration such
as 24h measured back from now; omitted means no cutoff). By default
(--unclassified-only=true) a message that already carries a recorded
spam verdict is skipped; pass --unclassified-only=false to reclassify
every selected message regardless.

For each processed message: the stored blob is re-parsed, the
configured classifier plugin is called with the same request projection
SMTP delivery and IMAP import use (spam.BuildRequest, nil auth results
-- a re-parsed stored message carries no fresh server-side auth
verdict), and the verdict is recorded in the message's
llm_classifications spam sub-record (--engine names the recorded
engine; defaults to the plugin's configured name). REQ-FILT-02 routing
then applies: spam moves into the principal's Junk mailbox (unless
already in a Junk- or Trash-attributed mailbox, dropping every other
membership except Sent/Drafts); suspect gains the "$Junk" keyword in
place; ham is left untouched. delivery_disposition is never touched.

--undo-log <path> records, before each move, the message id and its
previous mailbox ids, in the same format ` + "`spam apply-verdicts --undo-log`" + ` writes;
pass --undo <path> in a later invocation (of either command) to restore
those memberships. --dry-run reports the summary without writing the
classification record, moving anything, or writing --undo-log.`,
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

			sinceCutoff, err := parseSpamReclassifySince(since, clk.Now())
			if err != nil {
				return err
			}

			pluginName := firstPluginOfType(cfg.Plugin, "spam", "classifier")
			if pluginName == "" {
				return errors.New(`spam reclassify: no spam/classifier plugin configured in system.toml; add a [[plugin]] block with type = "classifier"`)
			}
			var pluginCfg *sysconfig.PluginConfig
			for i := range cfg.Plugin {
				if cfg.Plugin[i].Name == pluginName {
					pluginCfg = &cfg.Plugin[i]
					break
				}
			}
			if pluginCfg == nil {
				return fmt.Errorf("spam reclassify: plugin %q not found among system.toml [[plugin]] blocks", pluginName)
			}
			resolvedOpts, err := resolvePluginOptions(pluginCfg.Options)
			if err != nil {
				return fmt.Errorf("spam reclassify: plugin %q options: %w", pluginName, err)
			}

			pluginMgr := plugin.NewManager(plugin.ManagerOptions{
				Logger:        discardLogger(),
				Clock:         clk,
				ServerVersion: "admin-spam-reclassify",
			})
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = pluginMgr.Shutdown(shutdownCtx)
			}()
			pl, err := pluginMgr.Start(ctx, plugin.Spec{
				Name:      pluginCfg.Name,
				Path:      pluginCfg.Path,
				Type:      plugin.PluginType(pluginCfg.Type),
				Lifecycle: plugin.Lifecycle(pluginCfg.Lifecycle),
				Options:   resolvedOpts,
			})
			if err != nil {
				return fmt.Errorf("spam reclassify: start plugin %q: %w", pluginName, err)
			}
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) && pl.State() != plugin.StateHealthy {
				if pl.State() == plugin.StateDisabled || pl.State() == plugin.StateExited {
					return fmt.Errorf("spam reclassify: plugin %q reached state %s before healthy", pluginName, pl.State())
				}
				time.Sleep(50 * time.Millisecond)
			}
			if pl.State() != plugin.StateHealthy {
				return fmt.Errorf("spam reclassify: plugin %q did not reach healthy state in 15s (current=%s)", pluginName, pl.State())
			}

			spamClassifier := spam.New(pluginInvoker{mgr: pluginMgr}, discardLogger(), clk)
			if d := cfg.Spam.ClassifyTimeout.AsDuration(); d > 0 {
				spamClassifier = spamClassifier.WithTimeout(d)
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
				engineName = pluginName
			}

			sum, err := reclassifySpam(ctx, st, clk, spamClassifier, engineName, p.ID, spamReclassifyOptions{
				Since:            sinceCutoff,
				UnclassifiedOnly: unclassifiedOnly,
				Limit:            limit,
				DryRun:           dryRun,
				UndoLog:          undoWriter,
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
			return emitSpamReclassifySummary(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, dryRun, sum)
		},
	}
	c.Flags().StringVar(&since, "since", "", "only messages received at/after this RFC3339 timestamp or duration (e.g. 24h)")
	c.Flags().BoolVar(&unclassifiedOnly, "unclassified-only", true, "skip messages that already carry a recorded spam verdict")
	c.Flags().IntVar(&limit, "limit", 0, "max messages to process (0 = unlimited)")
	c.Flags().StringVar(&engine, "engine", "", "engine/model name recorded on the llm_classifications row (default: the plugin name)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report the summary without writing anything")
	c.Flags().StringVar(&undoLogPath, "undo-log", "", "write an undo log (message id + previous mailbox ids) before each move")
	c.Flags().StringVar(&undoPath, "undo", "", "restore memberships from a previously written --undo-log instead of reclassifying")
	return c
}

// parseSpamReclassifySince parses --since as either an RFC3339 timestamp
// or a duration (e.g. "24h") measured back from now. An empty string
// means no cutoff.
func parseSpamReclassifySince(s string, now time.Time) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("--since %q is neither an RFC3339 timestamp nor a duration (e.g. 24h): %w", s, err)
	}
	if d < 0 {
		return nil, fmt.Errorf("--since duration must be positive, got %q", s)
	}
	t := now.Add(-d)
	return &t, nil
}

// emitSpamReclassifySummary prints the reclassify run's summary to
// stdout (JSON, when --json is set) or stderr (human-readable), matching
// the --json convention used by the apply-verdicts / diag maintenance
// commands.
func emitSpamReclassifySummary(stdout, stderr io.Writer, g *globalOptions, dryRun bool, sum SpamReclassifySummary) error {
	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode string `json:"mode"`
			SpamReclassifySummary
		}{Mode: mode, SpamReclassifySummary: sum})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "reclassify: done (%s)\n", mode)
	fmt.Fprintf(stderr, "  selected:   %d\n", sum.Selected)
	fmt.Fprintf(stderr, "  classified: %d\n", sum.Classified)
	fmt.Fprintf(stderr, "  spam:       %d\n", sum.Spam)
	fmt.Fprintf(stderr, "  suspect:    %d\n", sum.Suspect)
	fmt.Fprintf(stderr, "  ham:        %d\n", sum.Ham)
	fmt.Fprintf(stderr, "  moved:      %d\n", sum.Moved)
	fmt.Fprintf(stderr, "  errors:     %d\n", sum.Errors)
	fmt.Fprintf(stderr, "  skipped:    %d\n", sum.Skipped)
	return nil
}
