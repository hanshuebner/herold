package admin

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/dupmessages"
	"github.com/hanshuebner/herold/internal/store"
)

// newDiagDuplicateMessagesCmd builds `herold diag duplicate-messages
// list|merge`, the repair path behind #496: SMTP delivery and the
// IMAP/JMAP importers each dedup independently by Message-ID, so a message
// that reaches one principal via two ingest paths whose stored bytes
// differ can still end up as two live rows sharing one Message-ID in the
// same thread. list is a dry run over one principal's duplicate groups;
// merge folds each group onto its oldest row and removes the rest.
func newDiagDuplicateMessagesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "duplicate-messages",
		Short: "list and merge messages sharing a Message-ID for one principal (re #496)",
	}
	c.AddCommand(newDiagDuplicateMessagesListCmd())
	c.AddCommand(newDiagDuplicateMessagesMergeCmd())
	return c
}

func newDiagDuplicateMessagesListCmd() *cobra.Command {
	var principalRef string
	c := &cobra.Command{
		Use:   "list",
		Short: "list Message-IDs with more than one live row for --principal",
		Long: `Opens the store from --system-config and reports every Message-ID
--principal holds more than one live message row for, oldest row first
within each group. Dry run: writes nothing.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(principalRef) == "" {
				return fmt.Errorf("--principal <id> is required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return fmt.Errorf("diag duplicate-messages list: open store: %w", err)
			}
			defer st.Close()

			pid, err := resolvePrincipalFromStore(ctx, st, strings.TrimSpace(principalRef))
			if err != nil {
				return err
			}
			groups, err := dupmessages.List(ctx, st, pid)
			if err != nil {
				return fmt.Errorf("diag duplicate-messages list: %w", err)
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeDupMessageGroupsJSON(cmd.OutOrStdout(), groups)
			}
			return writeDupMessageGroupsTable(cmd.OutOrStdout(), groups)
		},
	}
	c.Flags().StringVar(&principalRef, "principal", "", "principal to inspect, by canonical email or numeric ID (required)")
	return c
}

func newDiagDuplicateMessagesMergeCmd() *cobra.Command {
	var principalRef string
	var messageIDFilter string
	var undoLogPath string
	var dryRun bool
	c := &cobra.Command{
		Use:   "merge",
		Short: "fold each duplicate group for --principal onto its oldest row",
		Long: `Opens the store from --system-config and, for --principal, folds every
duplicate-Message-ID group (or just --message-id, when given) onto its
oldest row: the survivor gains every mailbox membership any duplicate
held, each membership's $seen flag and keywords OR'd in, and every other
row is removed through the normal destroy path (the change feed sees the
removal exactly like an Email/set destroy).

A removed row's blob is never garbage-collected by this command (re
#487): with --undo-log <path>, each removed row's blob hash and original
mailbox memberships are appended to a CSV an operator can use to manually
recover a row via "herold diag orphan-blobs restore --blob <hash>
--principal <principal> --label <mailbox-id>" if the merge turns out to
be wrong.

--dry-run reports what would be merged without writing anything.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(principalRef) == "" {
				return fmt.Errorf("--principal <id> is required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return fmt.Errorf("diag duplicate-messages merge: open store: %w", err)
			}
			defer st.Close()

			pid, err := resolvePrincipalFromStore(ctx, st, strings.TrimSpace(principalRef))
			if err != nil {
				return err
			}
			groups, err := dupmessages.List(ctx, st, pid)
			if err != nil {
				return fmt.Errorf("diag duplicate-messages merge: %w", err)
			}
			if strings.TrimSpace(messageIDFilter) != "" {
				filtered := groups[:0]
				for _, gr := range groups {
					if gr.MessageID == strings.TrimSpace(messageIDFilter) {
						filtered = append(filtered, gr)
					}
				}
				groups = filtered
				if len(groups) == 0 {
					return fmt.Errorf("no duplicate group %q found for this principal", messageIDFilter)
				}
			}

			var undoW *csv.Writer
			if undoLogPath != "" && !dryRun {
				f, err := os.Create(undoLogPath)
				if err != nil {
					return fmt.Errorf("diag duplicate-messages merge: create undo log: %w", err)
				}
				defer f.Close()
				undoW = csv.NewWriter(f)
				if err := undoW.Write([]string{"removed_message_id", "kept_message_id", "principal_id", "blob_hash", "mailbox_id", "flags", "keywords"}); err != nil {
					return fmt.Errorf("diag duplicate-messages merge: write undo log header: %w", err)
				}
				defer undoW.Flush()
			}

			var results []dupmessages.MergeResult
			for _, gr := range groups {
				if dryRun {
					results = append(results, dupmessages.MergeResult{
						MessageID:     gr.MessageID,
						KeptMessageID: gr.Messages[0].ID,
					})
					continue
				}
				res, err := dupmessages.Merge(ctx, st, gr)
				if err != nil {
					return fmt.Errorf("diag duplicate-messages merge: %q: %w", gr.MessageID, err)
				}
				if undoW != nil {
					if err := writeDupUndoRows(undoW, pid, res); err != nil {
						return fmt.Errorf("diag duplicate-messages merge: write undo log: %w", err)
					}
				}
				results = append(results, res)
			}
			if undoW != nil {
				undoW.Flush()
				if err := undoW.Error(); err != nil {
					return fmt.Errorf("diag duplicate-messages merge: flush undo log: %w", err)
				}
			}
			return writeDupMergeResults(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, results, dryRun)
		},
	}
	c.Flags().StringVar(&principalRef, "principal", "", "principal to merge, by canonical email or numeric ID (required)")
	c.Flags().StringVar(&messageIDFilter, "message-id", "", "merge only this normalised Message-ID's group (default: every duplicate group for the principal)")
	c.Flags().StringVar(&undoLogPath, "undo-log", "", "write a CSV of removed rows (blob hash + original mailboxes) to this path")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be merged without writing anything")
	return c
}

func writeDupUndoRows(w *csv.Writer, pid store.PrincipalID, res dupmessages.MergeResult) error {
	for _, rm := range res.Removed {
		if len(rm.Mailboxes) == 0 {
			if err := w.Write([]string{
				strconv.FormatUint(uint64(rm.MessageID), 10),
				strconv.FormatUint(uint64(res.KeptMessageID), 10),
				strconv.FormatUint(uint64(pid), 10),
				rm.BlobHash, "", "", "",
			}); err != nil {
				return err
			}
			continue
		}
		for _, mm := range rm.Mailboxes {
			if err := w.Write([]string{
				strconv.FormatUint(uint64(rm.MessageID), 10),
				strconv.FormatUint(uint64(res.KeptMessageID), 10),
				strconv.FormatUint(uint64(pid), 10),
				rm.BlobHash,
				strconv.FormatUint(uint64(mm.MailboxID), 10),
				strconv.FormatUint(uint64(mm.Flags), 10),
				strings.Join(mm.Keywords, ";"),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// dupMessageGroupJSON is the JSON wire shape for one duplicate group.
type dupMessageGroupJSON struct {
	MessageID string   `json:"messageId"`
	Messages  []uint64 `json:"messages"`
}

func writeDupMessageGroupsJSON(w interface{ Write(p []byte) (int, error) }, groups []dupmessages.Group) error {
	out := make([]dupMessageGroupJSON, len(groups))
	for i, g := range groups {
		ids := make([]uint64, len(g.Messages))
		for j, m := range g.Messages {
			ids[j] = uint64(m.ID)
		}
		out[i] = dupMessageGroupJSON{MessageID: g.MessageID, Messages: ids}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func writeDupMessageGroupsTable(w interface{ Write(p []byte) (int, error) }, groups []dupmessages.Group) error {
	fmt.Fprintf(w, "%d duplicate Message-ID group(s)\n", len(groups))
	for _, g := range groups {
		fmt.Fprintf(w, "%s  (%d rows)\n", g.MessageID, len(g.Messages))
		for i, m := range g.Messages {
			role := "kept"
			if i > 0 {
				role = "duplicate"
			}
			fmt.Fprintf(w, "    id=%d  received=%s  ingest=%s  %s\n",
				m.ID, m.ReceivedAt.Format("2006-01-02T15:04:05Z"), m.IngestSource, role)
		}
	}
	return nil
}

func writeDupMergeResults(stdout, stderr interface{ Write(p []byte) (int, error) }, g *globalOptions, results []dupmessages.MergeResult, dryRun bool) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			DryRun  bool                      `json:"dryRun"`
			Results []dupmessages.MergeResult `json:"results"`
		}{DryRun: dryRun, Results: results})
	}
	if g.quiet {
		return nil
	}
	verb := "merged"
	if dryRun {
		verb = "would merge"
	}
	for _, res := range results {
		removedCount := len(res.Removed)
		fmt.Fprintf(stderr, "%s %q: kept message %d, %s %d duplicate(s)\n",
			verb, res.MessageID, res.KeptMessageID, verb, removedCount)
	}
	fmt.Fprintf(stderr, "%d group(s) %s\n", len(results), verb)
	return nil
}
