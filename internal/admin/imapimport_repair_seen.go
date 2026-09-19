package admin

// imapimport_repair_seen.go wires `herold imapimport repair-seen` (re #435)
// to imapimport.RepairSeen, which holds the row-level repair semantics. See
// that function's package doc for the full contract.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/imapimport"
	"github.com/hanshuebner/herold/internal/store"
)

// IMAPImportRepairSeenResult reports what repairIMAPImportSeen did for one
// message id. Mirrors imapimport.RepairSeenResult for the CLI's JSON/table
// output.
type IMAPImportRepairSeenResult struct {
	MessageID store.MessageID `json:"message_id"`
	// Action is one of "marked-seen", "already-seen", or "skipped" (an
	// error, or a message with no imapimport_message_state row; see Error).
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

// repairIMAPImportSeen repairs each message in msgIDs via
// imapimport.RepairSeen. dryRun reports the intended action per message
// without writing anything.
func repairIMAPImportSeen(ctx context.Context, st store.Store, pid store.PrincipalID, msgIDs []store.MessageID, dryRun bool) ([]IMAPImportRepairSeenResult, error) {
	results := make([]IMAPImportRepairSeenResult, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		r := imapimport.RepairSeen(ctx, st, pid, msgID, dryRun)
		results = append(results, IMAPImportRepairSeenResult{
			MessageID: r.MessageID,
			Action:    string(r.Action),
			Error:     r.Error,
		})
	}
	return results, nil
}

// anySkipped reports whether results contains a "skipped" entry -- the
// repair-seen CLI exits non-zero when it does, mirroring restore-archive's
// anyRefused convention.
func anySkipped(results []IMAPImportRepairSeenResult) bool {
	for _, r := range results {
		if r.Action == string(imapimport.RepairSeenActionSkipped) {
			return true
		}
	}
	return false
}

// formatIMAPImportRepairSeenResults renders the per-message results as
// plain lines, one per message, for the CLI's human-readable output.
func formatIMAPImportRepairSeenResults(results []IMAPImportRepairSeenResult) string {
	var b strings.Builder
	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(&b, "message %d: %s (%s)\n", r.MessageID, r.Action, r.Error)
		} else {
			fmt.Fprintf(&b, "message %d: %s\n", r.MessageID, r.Action)
		}
	}
	return b.String()
}
