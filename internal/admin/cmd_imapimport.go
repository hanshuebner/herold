package admin

// cmd_imapimport.go — CLI surface for IMAP import worker observation.
//
// Commands:
//
//	herold imapimport status   — live snapshot of every IMAP import worker
//
// REQ-IMAP-IMP-62.

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/cliout"
	"github.com/hanshuebner/herold/internal/clock"
)

func newIMAPImportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "imapimport",
		Short: "IMAP import worker observation",
	}
	c.AddCommand(newIMAPImportStatusCmd())
	c.AddCommand(newIMAPImportRepairOrphansCmd())
	return c
}

// newIMAPImportRepairOrphansCmd builds `herold imapimport repair-orphans`
// (issue #319), a store-backed maintenance command (opens the store from
// --system-config, like `spam apply-verdicts` / `diag reparse-envelopes`;
// no admin server needed). See imapimport_repair_orphans.go for the
// row-level semantics.
func newIMAPImportRepairOrphansCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "repair-orphans <email-or-id>",
		Short: "file label-only IMAP-import orphans into Junk or INBOX (re #319)",
		Long: `For every IMAP-import account belonging to the given principal, finds
messages that carry only the account's provenance label (REQ-IMAP-IMP-100)
and no imapimport_message_state row for that account -- the orphan shape
issue #319 describes, left behind by a pre-fix upstream \Deleted mirroring
a message down to its label alone.

Each orphan is filed into the principal's Junk mailbox when it carries a
recorded "spam" verdict (llm_classifications), or into INBOX otherwise --
the same split the live import path applies at ingest (REQ-FILT-02). The
provenance label and message_state history are not restored (the upstream
folder/UID that produced the orphan is unrecoverable); only the missing
folder membership is. --dry-run reports the counts without moving
anything.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return err
			}
			defer st.Close()

			p, err := resolveStorePrincipal(ctx, st, args[0])
			if err != nil {
				return err
			}

			sum, err := repairIMAPImportOrphans(ctx, st, p.ID, dryRun)
			if err != nil {
				return err
			}
			return emitIMAPImportRepairOrphansSummary(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, dryRun, sum)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report counts without moving any message")
	return c
}

// emitIMAPImportRepairOrphansSummary prints the repair-orphans run's
// summary, mirroring emitSpamApplyVerdictsSummary's --json convention.
func emitIMAPImportRepairOrphansSummary(stdout, stderr io.Writer, g *globalOptions, dryRun bool, sum IMAPImportRepairOrphansSummary) error {
	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Mode string `json:"mode"`
			IMAPImportRepairOrphansSummary
		}{Mode: mode, IMAPImportRepairOrphansSummary: sum})
	}
	if g.quiet {
		return nil
	}
	fmt.Fprintf(stderr, "repair-orphans: done (%s)\n", mode)
	fmt.Fprintf(stderr, "  accounts scanned: %d\n", sum.AccountsScanned)
	fmt.Fprintf(stderr, "  label members:    %d\n", sum.LabelMembers)
	fmt.Fprintf(stderr, "  orphans found:    %d\n", sum.Orphans)
	fmt.Fprintf(stderr, "  filed junk:       %d\n", sum.FiledJunk)
	fmt.Fprintf(stderr, "  filed inbox:      %d\n", sum.FiledInbox)
	fmt.Fprintf(stderr, "  errors:           %d\n", sum.Errors)
	return nil
}

func newIMAPImportStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show the live status of every IMAP import worker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/imap-imports/status", nil, &out); err != nil {
				return err
			}
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeResult(cmd.OutOrStdout(), g, out)
			}
			return writeIMAPImportStatusHuman(cmd.OutOrStdout(), out)
		},
	}
}

// writeIMAPImportStatusHuman renders the IMAP import worker snapshot as a
// human-readable table. REQ-IMAP-IMP-62.
func writeIMAPImportStatusHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no IMAP import workers running)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header(
		"ACCOUNT-ID",
		"PRINCIPAL",
		"HOST",
		"PHASE",
		"CONN-MODE",
		"CONNECTED",
		"LAST-SYNC",
		"FAILURES",
		"FETCHED",
		"LAST-ERROR",
	)
	for _, it := range items {
		connMode := sval(it, "conn_mode")
		if connMode == "" {
			connMode = "-"
		}
		lastSync := tval(it, "last_sync_at")
		if lastSync == "" {
			lastSync = "-"
		}
		phase := sval(it, "phase")
		if folder := sval(it, "current_folder"); folder != "" && phase == "syncing" {
			phase = phase + "/" + cliout.Trunc(folder, 20)
		}
		nextPoll := sval(it, "next_poll_at")
		if nextPoll != "" && phase == "polling" {
			// Append time-until-next-poll when we are in polling phase.
			if ts, perr := time.Parse(time.RFC3339, nextPoll); perr == nil {
				until := time.Until(ts).Truncate(time.Second)
				phase = fmt.Sprintf("%s (in %s)", phase, until)
			}
		}
		t.Row(
			sval(it, "account_id"),
			sval(it, "principal_id"),
			sval(it, "host"),
			phase,
			connMode,
			bval(it, "connected"),
			lastSync,
			fval(it, "consecutive_failures"),
			fval(it, "messages_fetched"),
			cliout.Trunc(sval(it, "last_error"), cliout.MaxErrorLen),
		)
	}
	return t.Flush()
}
