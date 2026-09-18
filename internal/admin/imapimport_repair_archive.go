package admin

// imapimport_repair_archive.go wires `herold imapimport restore-archive`
// (a one-off, store-backed maintenance command in the family of `imapimport
// repair-orphans` / `spam apply-verdicts`; no admin server needed) to
// imapimport.RestoreArchivePlacement, which holds the row-level repair
// semantics (re #376, second round). See that function's package doc for
// the eligibility guard and the message_state repair it performs.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/imapimport"
	"github.com/hanshuebner/herold/internal/store"
)

// IMAPImportRestoreArchiveResult reports what restoreIMAPImportArchive did
// for one message id. Mirrors imapimport.RestoreArchiveResult for the CLI's
// JSON/table output.
type IMAPImportRestoreArchiveResult struct {
	MessageID store.MessageID `json:"message_id"`
	// Action is one of "moved-to-archive", "already-archived" (no INBOX
	// membership found, nothing to do), "refused" (an INBOX membership
	// exists but the message matched neither #376 eligibility shape and
	// --force was not given), or "skipped" (an error prevented the
	// repair; see Error).
	Action string `json:"action"`
	// Reason explains the eligibility verdict for a "moved-to-archive" or
	// "refused" result.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// restoreIMAPImportArchive repairs each message in msgIDs via
// imapimport.RestoreArchivePlacement. dryRun reports the intended action per
// message, including the eligibility verdict, without writing anything.
// force moves a message that fails the eligibility guard.
func restoreIMAPImportArchive(ctx context.Context, st store.Store, pid store.PrincipalID, msgIDs []store.MessageID, dryRun, force bool) ([]IMAPImportRestoreArchiveResult, error) {
	results := make([]IMAPImportRestoreArchiveResult, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		r := imapimport.RestoreArchivePlacement(ctx, st, pid, msgID, dryRun, force)
		results = append(results, IMAPImportRestoreArchiveResult{
			MessageID: r.MessageID,
			Action:    string(r.Action),
			Reason:    r.Reason,
			Error:     r.Error,
		})
	}
	return results, nil
}

// anyRefused reports whether results contains a "refused" entry -- the
// restore-archive CLI exits non-zero when it does, since the operator named
// a message the repair declined to touch (re #376, second round).
func anyRefused(results []IMAPImportRestoreArchiveResult) bool {
	for _, r := range results {
		if r.Action == string(imapimport.RestoreArchiveActionRefused) {
			return true
		}
	}
	return false
}

// formatIMAPImportRestoreArchiveResults renders the per-message results as
// plain lines, one per message, for the CLI's human-readable output.
func formatIMAPImportRestoreArchiveResults(results []IMAPImportRestoreArchiveResult) string {
	var b strings.Builder
	for _, r := range results {
		switch {
		case r.Error != "":
			fmt.Fprintf(&b, "message %d: %s (%s)\n", r.MessageID, r.Action, r.Error)
		case r.Reason != "":
			fmt.Fprintf(&b, "message %d: %s (%s)\n", r.MessageID, r.Action, r.Reason)
		default:
			fmt.Fprintf(&b, "message %d: %s\n", r.MessageID, r.Action)
		}
	}
	return b.String()
}
