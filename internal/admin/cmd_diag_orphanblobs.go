package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/orphanblobs"
	"github.com/hanshuebner/herold/internal/store"
)

// newDiagOrphanBlobsCmd builds `herold diag orphan-blobs list|restore`, the
// recovery path behind #487: the IMAP-import upstream-expunge reconcile can
// delete a message row while its content-addressed blob survives on disk
// (the blob store is never garbage-collected on message deletion). list is
// a dry run over every such orphan; restore re-inserts one named blob.
func newDiagOrphanBlobsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "orphan-blobs",
		Short: "list and restore blobs whose message row was deleted (re #487)",
	}
	c.AddCommand(newDiagOrphanBlobsListCmd())
	c.AddCommand(newDiagOrphanBlobsRestoreCmd())
	return c
}

func newDiagOrphanBlobsListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "list every blob no live message references",
		Long: `Opens the store from --system-config and walks the blob store, reporting
every blob no live message (across every principal) currently references.
For each orphan, parses the Date/From/Subject/Message-ID headers directly
from the blob (the row that carried them is gone) and reports:

  - duplicate-of-live-message-id: a live message already carries the same
    Message-ID (the mirror's own sent-copy dedup, not a loss).
  - referenced-by-live-thread: a live message's In-Reply-To or References
    names this orphan's Message-ID (a thread the orphan belongs to
    survived even though its own row did not).

Dry run: writes nothing. Use restore to recover a chosen blob.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return fmt.Errorf("diag orphan-blobs list: open store: %w", err)
			}
			defer st.Close()

			orphans, err := orphanblobs.List(ctx, st)
			if err != nil {
				return fmt.Errorf("diag orphan-blobs list: %w", err)
			}

			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeOrphanBlobsJSON(cmd.OutOrStdout(), orphans)
			}
			return writeOrphanBlobsTable(cmd.OutOrStdout(), orphans)
		},
	}
	return c
}

func newDiagOrphanBlobsRestoreCmd() *cobra.Command {
	var principalRef string
	var hash string
	var labelRef uint64
	c := &cobra.Command{
		Use:   "restore",
		Short: "re-insert one orphan blob as a message",
		Long: `Opens the store from --system-config and re-inserts the named blob (--blob,
the hash from "orphan-blobs list") as a message for --principal, into that
principal's Archive mailbox plus --label (a mailbox ID) when given, with
$seen set on every membership. Threads and deduplicates through the normal
insert path, exactly like a freshly-arrived message (including a
late-arriving ancestor's merge into a reply's existing thread). Refuses,
without writing anything, when a live message already carries the blob's
Message-ID header.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(principalRef) == "" {
				return fmt.Errorf("--principal <id> is required")
			}
			if strings.TrimSpace(hash) == "" {
				return fmt.Errorf("--blob <hash> is required")
			}
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
			if err != nil {
				return fmt.Errorf("diag orphan-blobs restore: open store: %w", err)
			}
			defer st.Close()

			pid, err := resolvePrincipalFromStore(ctx, st, strings.TrimSpace(principalRef))
			if err != nil {
				return err
			}

			res, err := orphanblobs.Restore(ctx, st, pid, strings.TrimSpace(hash), store.MailboxID(labelRef))
			if err != nil {
				return fmt.Errorf("diag orphan-blobs restore: %w", err)
			}
			return writeOrphanRestoreResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), g, res)
		},
	}
	c.Flags().StringVar(&principalRef, "principal", "", "principal to restore into, by canonical email or numeric ID (required)")
	c.Flags().StringVar(&hash, "blob", "", "blob hash to restore, from \"orphan-blobs list\" (required)")
	c.Flags().Uint64Var(&labelRef, "label", 0, "optional mailbox ID to also add the restored message to")
	return c
}

// orphanBlobJSON is the JSON wire shape for one OrphanBlob row.
type orphanBlobJSON struct {
	Hash                     string `json:"hash"`
	Size                     int64  `json:"size"`
	Date                     string `json:"date,omitempty"`
	From                     string `json:"from,omitempty"`
	Subject                  string `json:"subject,omitempty"`
	MessageID                string `json:"messageId,omitempty"`
	DuplicateOfLiveMessageID uint64 `json:"duplicateOfLiveMessageId,omitempty"`
	ReferencedByLiveThread   bool   `json:"referencedByLiveThread,omitempty"`
	ParseError               string `json:"parseError,omitempty"`
}

func writeOrphanBlobsJSON(w interface{ Write(p []byte) (int, error) }, orphans []orphanblobs.OrphanBlob) error {
	out := make([]orphanBlobJSON, len(orphans))
	for i, o := range orphans {
		out[i] = orphanBlobJSON{
			Hash:                     o.Hash,
			Size:                     o.Size,
			From:                     o.From,
			Subject:                  o.Subject,
			MessageID:                o.MessageID,
			DuplicateOfLiveMessageID: uint64(o.DuplicateOfLiveMessageID),
			ReferencedByLiveThread:   o.ReferencedByLiveThread,
			ParseError:               o.ParseError,
		}
		if !o.Date.IsZero() {
			out[i].Date = o.Date.Format("2006-01-02T15:04:05Z07:00")
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func writeOrphanBlobsTable(w interface{ Write(p []byte) (int, error) }, orphans []orphanblobs.OrphanBlob) error {
	fmt.Fprintf(w, "%d orphan blob(s)\n", len(orphans))
	for _, o := range orphans {
		if o.ParseError != "" {
			fmt.Fprintf(w, "%s  %8d bytes  (unparsable: %s)\n", o.Hash, o.Size, o.ParseError)
			continue
		}
		flags := ""
		if o.DuplicateOfLiveMessageID != 0 {
			flags += fmt.Sprintf(" duplicate-of-live-message-id=%d", o.DuplicateOfLiveMessageID)
		}
		if o.ReferencedByLiveThread {
			flags += " referenced-by-live-thread"
		}
		date := ""
		if !o.Date.IsZero() {
			date = o.Date.Format("2006-01-02")
		}
		fmt.Fprintf(w, "%s  %8d bytes  %-10s  %-30s  %-40s  %s%s\n",
			o.Hash, o.Size, date, truncate(o.From, 30), truncate(o.Subject, 40), o.MessageID, flags)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func writeOrphanRestoreResult(stdout, stderr interface{ Write(p []byte) (int, error) }, g *globalOptions, res orphanblobs.RestoreResult) error {
	if g.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Hash      string `json:"hash"`
			MessageID uint64 `json:"messageId,omitempty"`
			Refused   bool   `json:"refused"`
			Reason    string `json:"reason,omitempty"`
		}{Hash: res.Hash, MessageID: uint64(res.MessageID), Refused: res.Refused, Reason: res.Reason})
	}
	if res.Refused {
		return fmt.Errorf("refused: %s", res.Reason)
	}
	if !g.quiet {
		fmt.Fprintf(stderr, "restored blob %s as message %d\n", res.Hash, res.MessageID)
	}
	return nil
}
