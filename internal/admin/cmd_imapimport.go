package admin

// cmd_imapimport.go — CLI surface for IMAP import worker observation.
//
// Commands:
//
//	herold imapimport status   — live snapshot of every IMAP import worker
//
// REQ-IMAP-IMP-62.

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/cliout"
)

func newIMAPImportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "imapimport",
		Short: "IMAP import worker observation",
	}
	c.AddCommand(newIMAPImportStatusCmd())
	return c
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
