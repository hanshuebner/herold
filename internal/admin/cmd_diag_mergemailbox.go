package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/mergemailbox"
	"github.com/hanshuebner/herold/internal/store"
)

// newDiagMergeMailboxCmd builds `herold diag merge-mailbox --principal
// <id> --from <mailbox-id> --into <mailbox-id> [--dry-run]`, the repair
// path behind #509: the IMAP-import folder mapping could create a second
// mailbox carrying the same SPECIAL-USE role (e.g. an upstream "Spam"
// folder next to herold's provisioned "Junk"), which RFC 8621 S2 forbids
// and which left the duplicate's contents unreachable through the
// account's single-role lookup (the Suite's Spam view, JMAP's role
// filter). This command folds --from into --into: every message
// membership --from holds moves into --into through the store's normal
// move path (a fresh UID, a ModSeq bump, a change-feed row, keywords/
// flags/received_to preserved), or is dropped when the message already
// lives in --into too; every other table referencing --from's mailbox id
// (a snooze wake destination, a pre-trash restore snapshot, IMAP-import
// bookkeeping, a mailing list's archive mailbox) is rewritten to --into;
// and the now-empty --from mailbox is deleted. Refuses, rather than
// guessing, when --from has child mailboxes or ACL grants.
func newDiagMergeMailboxCmd() *cobra.Command {
	var principalRef string
	var fromID, intoID uint64
	var dryRun bool
	c := &cobra.Command{
		Use:   "merge-mailbox",
		Short: "fold a duplicate special-use mailbox into the account's surviving one (re #509)",
		Long: `Opens the store from --system-config and, for --principal, folds every
message membership --from holds into --into through the store's normal
move path, rewrites every other table referencing --from's mailbox id to
reference --into instead, and deletes the now-empty --from mailbox.

Refuses the merge, writing nothing, when --from has child mailboxes or
carries ACL grants -- rewriting either silently risks orphaning a child
mailbox or widening/narrowing another principal's access.

--dry-run reports what would be merged without writing anything.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(principalRef) == "" {
				return fmt.Errorf("--principal <id> is required")
			}
			if fromID == 0 || intoID == 0 {
				return fmt.Errorf("--from and --into mailbox IDs are required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return fmt.Errorf("diag merge-mailbox: open store: %w", err)
			}
			defer st.Close()

			pid, err := resolvePrincipalFromStore(ctx, st, strings.TrimSpace(principalRef))
			if err != nil {
				return err
			}

			var plan mergemailbox.Plan
			if dryRun {
				plan, err = mergemailbox.Analyze(ctx, st, pid, store.MailboxID(fromID), store.MailboxID(intoID))
			} else {
				plan, err = mergemailbox.Merge(ctx, st, pid, store.MailboxID(fromID), store.MailboxID(intoID))
			}
			if err != nil {
				return fmt.Errorf("diag merge-mailbox: %w", err)
			}
			return writeMergeMailboxResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, plan, dryRun)
		},
	}
	c.Flags().StringVar(&principalRef, "principal", "", "principal to merge mailboxes for, by canonical email or numeric ID (required)")
	c.Flags().Uint64Var(&fromID, "from", 0, "source mailbox ID: its contents move into --into and it is deleted (required)")
	c.Flags().Uint64Var(&intoID, "into", 0, "target mailbox ID: --from's contents are folded into this mailbox (required)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be merged without writing anything")
	return c
}

// mergeMailboxResultJSON is the JSON wire shape for one merge-mailbox
// result (both --dry-run and the real run report this shape).
type mergeMailboxResultJSON struct {
	DryRun  bool                   `json:"dryRun"`
	From    uint64                 `json:"fromMailboxId"`
	Into    uint64                 `json:"intoMailboxId"`
	Moved   int                    `json:"moved"`
	Dropped int                    `json:"dropped"`
	Refs    store.MailboxRefCounts `json:"refs"`
}

func writeMergeMailboxResult(stdout, stderr interface{ Write(p []byte) (int, error) }, g *globalOptions, plan mergemailbox.Plan, dryRun bool) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(mergeMailboxResultJSON{
			DryRun:  dryRun,
			From:    uint64(plan.From.ID),
			Into:    uint64(plan.To.ID),
			Moved:   len(plan.Moved),
			Dropped: len(plan.Dropped),
			Refs:    plan.Refs,
		})
	}
	if g.quiet {
		return nil
	}
	verb := "merged"
	if dryRun {
		verb = "would merge"
	}
	fmt.Fprintf(stderr, "%s mailbox %q (id %d) into %q (id %d): %d message(s) moved, %d dropped (already in target)\n",
		verb, plan.From.Name, plan.From.ID, plan.To.Name, plan.To.ID, len(plan.Moved), len(plan.Dropped))
	refsVerb := "rewritten"
	if dryRun {
		refsVerb = "would be rewritten"
	}
	fmt.Fprintf(stderr, "other references %s: %d wake-destination, %d pretrash-snapshot, %d import-message-state, %d import-provenance, %d mailing-list-archive\n",
		refsVerb, plan.Refs.WakeDestinations, plan.Refs.PretrashSnapshots, plan.Refs.ImportMessageState,
		plan.Refs.ImportProvenance, plan.Refs.MailingListArchives)
	return nil
}
