package admin

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/ghostrows"
)

// newDiagImapImportGhostRowsCmd builds `herold diag imapimport-ghost-rows
// [--dry-run=false]`, the cleanup behind #558: IMAP-import message_state
// rows whose UID is at or above their folder's UIDNEXT name no upstream
// message. Dry-run by default; reports counts per account and folder only.
func newDiagImapImportGhostRowsCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "imapimport-ghost-rows",
		Short: "list or remove IMAP-import state rows whose UID cannot exist upstream (re #558)",
		Long: `Opens the store from --system-config and finds every imapimport_message_state
row whose upstream UID is at or above the UIDNEXT of its folder's sync cursor.
Such a row was left behind by a write-back move that recorded the moved
message under the destination folder with the source folder's UID.

--dry-run (the default) only reports row counts per account and folder.
--dry-run=false deletes the rows; no message or mailbox membership is
touched, and a live message keeps its remaining state rows.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return fmt.Errorf("diag imapimport-ghost-rows: open store: %w", err)
			}
			defer st.Close()

			ghosts, err := ghostrows.Find(ctx, st)
			if err != nil {
				return fmt.Errorf("diag imapimport-ghost-rows: %w", err)
			}
			counts, err := ghostrows.Summarize(ctx, st, ghosts)
			if err != nil {
				return fmt.Errorf("diag imapimport-ghost-rows: %w", err)
			}
			deleted := 0
			if !dryRun {
				deleted, err = ghostrows.Delete(ctx, st, ghosts)
				if err != nil {
					return fmt.Errorf("diag imapimport-ghost-rows: %w", err)
				}
			}
			out := cmd.OutOrStdout()
			if g.jsonOut {
				type row struct {
					AccountID    string `json:"accountId"`
					Folder       string `json:"folder"`
					Rows         int    `json:"rows"`
					LiveMessages int    `json:"liveMessages"`
				}
				res := struct {
					DryRun  bool  `json:"dryRun"`
					Deleted int   `json:"deleted"`
					Total   int   `json:"total"`
					Folders []row `json:"folders"`
				}{DryRun: dryRun, Deleted: deleted, Total: len(ghosts)}
				for _, c := range counts {
					res.Folders = append(res.Folders, row{c.AccountID, c.Folder, c.Rows, c.LiveMessages})
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			if g.quiet {
				return nil
			}
			for _, c := range counts {
				fmt.Fprintf(out, "account %s folder %q: %d ghost row(s), %d on live messages\n", c.AccountID, c.Folder, c.Rows, c.LiveMessages)
			}
			if dryRun {
				fmt.Fprintf(out, "dry run: %d ghost row(s) would be removed\n", len(ghosts))
			} else {
				fmt.Fprintf(out, "removed %d ghost row(s)\n", deleted)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", true, "report counts without deleting (use --dry-run=false to delete)")
	return c
}
