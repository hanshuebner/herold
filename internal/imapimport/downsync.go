package imapimport

// downsync.go implements the download direction of bidirectional flag sync:
// applying upstream-only \Seen / \Flagged changes back to herold (D2+D3).
//
// The herold->upstream direction is the change-feed-driven write-back loop in
// writeback.go. The reverse direction was missing: a pure-upstream flag change
// (the user marks a message read in the upstream's own webmail) was never
// reflected in herold, because the write-back loop only reacts to herold-side
// changes and HighestModSeq was stored but never used.
//
// Each sync round on an already-initialised folder reconciles upstream flags
// down to herold under the upstream-authoritative rule (REQ-IMAP-IMP-42):
//
//   - When the upstream advertises CONDSTORE, issue
//     "UID FETCH 1:* (FLAGS) (CHANGEDSINCE <highest_modseq>)" so only messages
//     whose flags changed since the last round are examined, then let the
//     cursor advance highest_modseq (REQ-IMAP-IMP-24).
//   - Otherwise fall back to a bounded re-fetch of the known (in-horizon) UID
//     set's FLAGS on each poll (REQ-IMAP-IMP-40).
//
// For each changed message the three-way compare of REQ-IMAP-IMP-42 runs with
// last_synced_flags as the base: only-upstream-changed applies upstream to
// herold; both-changed is a conflict where the upstream value still wins.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// downSyncFlags pulls upstream \Seen / \Flagged changes for upstreamFolder and
// applies them to herold (REQ-IMAP-IMP-40/24/42). The folder must already be
// SELECTed on conn. It is best-effort: a fetch error is returned for logging
// but does not abort the folder sync. cursor is read for the CONDSTORE base
// modseq; the caller persists the advanced highest_modseq afterwards.
func (w *accountWorker) downSyncFlags(
	ctx context.Context,
	conn Conn,
	upstreamFolder string,
	cursor *store.IMAPImportFolderCursor,
	floorDate *time.Time,
) error {
	var changed []uidFlags

	if conn.Caps().Has(imap.CapCondStore) && cursor.HighestModSeq > 0 {
		// Incremental CONDSTORE path: only messages changed since last round.
		ch, err := conn.UIDFetchFlagsChangedSince(ctx, cursor.HighestModSeq)
		if err != nil {
			return fmt.Errorf("imapimport: down-sync CHANGEDSINCE: %w", err)
		}
		changed = ch
	} else {
		// Fallback: bounded re-fetch of the in-horizon UID set's flags.
		var floor time.Time
		if floorDate != nil {
			floor = *floorDate
		}
		uids, err := conn.UIDSearchSince(ctx, floor)
		if err != nil {
			return fmt.Errorf("imapimport: down-sync search: %w", err)
		}
		ch, err := conn.UIDFetchFlagsMulti(ctx, uids)
		if err != nil {
			return fmt.Errorf("imapimport: down-sync flag re-fetch: %w", err)
		}
		changed = ch
	}

	for _, cf := range changed {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.applyUpstreamFlagChange(ctx, upstreamFolder, cf)
	}
	return nil
}

// applyUpstreamFlagChange reconciles one upstream message's flags down to
// herold under the upstream-authoritative three-way rule (REQ-IMAP-IMP-42).
func (w *accountWorker) applyUpstreamFlagChange(ctx context.Context, upstreamFolder string, cf uidFlags) {
	account := w.opts.account
	log := w.opts.log

	ms, found, err := w.opts.store.Meta().GetIMAPImportMessageState(ctx, account.ID, upstreamFolder, uint32(cf.UID))
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("imapimport: down-sync: GetIMAPImportMessageState failed",
				slog.String("account_id", account.ID),
				slog.String("folder", upstreamFolder),
				slog.Uint64("uid", uint64(cf.UID)),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	if !found {
		// Not mirrored yet: the download path handles new messages, and a
		// message with no state row cannot be addressed. Nothing to reconcile.
		return
	}

	upstreamSynced := syncedFlagsFromIMAP(cf.Flags)
	if upstreamSynced == ms.LastSyncedFlags {
		// Upstream unchanged since the last sync — nothing to apply.
		return
	}

	heroldMsg, err := w.opts.store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if err != nil {
		// Message already gone from herold — nothing to do.
		return
	}
	heroldSynced := syncedFlagsFromStoreFlags(heroldMsg.Flags)
	conflict := heroldSynced != ms.LastSyncedFlags

	// Upstream-authoritative: apply the upstream value to herold in both the
	// only-upstream-changed case and the both-changed (conflict) case.
	w.applyUpstreamFlagsToHerold(ctx, ms, upstreamSynced, heroldMsg)
	ms.LastSyncedFlags = upstreamSynced
	w.upsertMessageState(ctx, ms)
	observe.IMAPImportFlagsPropagatedTotal.WithLabelValues(account.ID, "down").Inc()

	if conflict {
		log.Info("imapimport: down-sync: flag conflict; upstream wins",
			slog.String("account_id", account.ID),
			slog.String("folder", upstreamFolder),
			slog.Uint64("uid", uint64(cf.UID)),
		)
		observe.IMAPImportConflictsTotal.WithLabelValues(account.ID, "flag").Inc()
	}
}
