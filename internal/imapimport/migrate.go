package imapimport

// migrate.go implements the complete-migration cutover (REQ-IMAP-IMP-90..96,
// Wave C). A principal trials herold in client mode, then requests complete
// migration off the upstream while preserving the state built up in herold.
//
// The transition is enabled -> migrating -> migrated:
//
//   - migrating (attemptMigration below): the worker forces the backfill floor
//     to "all" (no floor) and runs a one-shot COMPLETE backfill of every mapped
//     folder — the bounded re-scan of REQ-IMAP-IMP-19 driven down to the
//     earliest upstream UID. Authority has already transferred to herold, so
//     the reconcile does NOT apply the upstream-authoritative overwrite of
//     REQ-IMAP-IMP-42 to already-mirrored mail (gated via authorityIsHerold in
//     sync.go); the complete backfill only ADDS not-yet-mirrored messages,
//     deduped by Message-ID / blob_hash. Progress is observable via the
//     backfill_remaining gauge draining to zero and the live "migrating" phase
//     (REQ-IMAP-IMP-91/92).
//   - migrated: on a clean backfill the account is flipped to the terminal
//     migrated state and the worker stops. IDLE / poll loops never start, the
//     write-back driver is never attached, and the single upstream connection
//     is closed on return (REQ-IMAP-IMP-93). The account row, cursors, and
//     message_state are retained for provenance.
//
// Cutover is resumable and idempotent: a restart re-enters attemptMigration
// (the account is still migrating, included by ListEnabledIMAPImportAccounts),
// re-forces the floor, and re-runs the complete backfill, which never
// duplicates (dedup) and never regresses herold-side state (REQ-IMAP-IMP-94).
// A migrated account may be re-opened to enabled by the user, re-asserting
// upstream-authoritative handling from that point (REQ-IMAP-IMP-95); re-opening
// does not re-fetch already-mirrored mail because the cursors persist.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/store"
)

// attemptMigration runs one complete-migration cutover attempt for a migrating
// account. It connects, runs a complete backfill of every mapped folder (floor
// forced to "all"), and on success flips the account to the terminal migrated
// state and returns nil so run() stops the worker. A connection / sync failure
// is returned for the caller's backoff + errored handling (REQ-IMAP-IMP-25). A
// runtime state change away from migrating returns errStateChanged so run()
// re-dispatches.
func (w *accountWorker) attemptMigration(ctx context.Context) error {
	account := w.opts.account

	// Reject password / app_password when the operator disabled plain auth
	// (REQ-IMAP-IMP-05), mirroring attempt().
	if !w.opts.cfg.IMAPImportAllowPassword() &&
		(account.AuthMethod == store.IMAPImportAuthMethodPassword ||
			account.AuthMethod == store.IMAPImportAuthMethodAppPassword) {
		return fmt.Errorf("imapimport: auth_method=%s rejected: allow_password=false in sysconfig (REQ-IMAP-IMP-05)",
			account.AuthMethod)
	}

	// Force the backfill floor to "all" (no floor) for the complete backfill
	// (REQ-IMAP-IMP-91). Done in-memory so the cutover is complete regardless
	// of how the transition was triggered; the JMAP / admin surfaces also
	// persist the cleared floor so IMAPImport/get reflects it and a restart
	// resumes with no floor. Because the worker re-forces nil on every
	// migrating session, resume is complete even if the floor was not
	// persisted (e.g. a programmatic SetIMAPImportAccountState transition).
	if w.opts.account.BackfillFloorDate != nil {
		w.opts.log.Info("imapimport: cutover forcing backfill floor to all",
			slog.String("account_id", account.ID))
		w.opts.account.BackfillFloorDate = nil
	}

	w.status.setPhase(PhaseMigrating, w.opts.clk.Now())

	// Open the credential immediately before connect; never retained beyond
	// this function (REQ-IMAP-IMP-70).
	credPlaintext, err := w.openCredential(ctx, w.opts.account)
	if err != nil {
		return fmt.Errorf("imapimport: cutover credential open failed: %w", err)
	}
	conn, err := w.opts.dialer.Dial(ctx, dialParams{
		AccountID:           account.ID,
		Host:                account.Host,
		Port:                account.Port,
		TLSMode:             string(account.TLSMode),
		Username:            account.Username,
		AuthMethod:          string(account.AuthMethod),
		CredentialPlaintext: credPlaintext,
	})
	credPlaintext = ""
	_ = credPlaintext
	if err != nil {
		w.status.setConnected(false)
		return err
	}
	w.status.setConnected(true)
	w.status.setConnMode("single")
	// Retirement: the single upstream connection is closed cleanly on return
	// (REQ-IMAP-IMP-93). No IDLE / poll loop and no write-back driver are
	// started for a cutover — herold owns the mail from here.
	defer func() {
		w.status.setConnected(false)
		if logoutErr := conn.Logout(); logoutErr != nil {
			w.opts.log.Debug("imapimport: cutover LOGOUT error (non-fatal)",
				slog.String("account_id", account.ID),
				slog.String("error", logoutErr.Error()))
		}
		conn.Close()
	}()

	// Ensure the provenance label exists so backfilled mail is tagged
	// (REQ-IMAP-IMP-100); non-fatal.
	if perr := w.ensureProvenanceMailbox(ctx); perr != nil {
		w.opts.log.Warn("imapimport: cutover: failed to ensure provenance label; continuing",
			slog.String("account_id", account.ID),
			slog.String("error", perr.Error()))
	}

	w.opts.log.Info("imapimport: complete migration: running complete backfill",
		slog.String("account_id", account.ID),
		slog.String("host", account.Host))

	// Run the complete backfill. With the floor forced to nil and
	// authorityIsHerold() true, syncAllFolders fetches every in-mailbox UID
	// below each folder's low-water mark (the full history) plus forward mail,
	// dedups, and never down-syncs upstream flags onto already-mirrored mail.
	w.status.setPhase(PhaseMigrating, w.opts.clk.Now())
	if err := w.syncAllFolders(ctx, conn); err != nil {
		return fmt.Errorf("imapimport: cutover backfill: %w", err)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// If the state moved away from migrating while we worked (e.g. the user
	// disabled or deleted the account), do not force migrated — re-dispatch.
	if w.stateChangedFromMigrating(ctx) {
		return errStateChanged
	}

	// Cutover complete: flip to the terminal migrated state (REQ-IMAP-IMP-93).
	now := w.opts.clk.Now()
	if setErr := w.opts.store.Meta().SetIMAPImportAccountState(
		ctx,
		account.ID,
		store.IMAPImportAccountStateMigrated,
		"",
		&now,
	); setErr != nil {
		// Could not persist the terminal state; surface as a failure so the
		// next attempt retries rather than silently leaving it migrating.
		return fmt.Errorf("imapimport: cutover: persist migrated state: %w", setErr)
	}
	w.opts.account.State = store.IMAPImportAccountStateMigrated
	w.status.setPhase(PhaseMigrated, w.opts.clk.Now())
	w.opts.log.Info("imapimport: complete migration finished; account migrated, upstream retired",
		slog.String("account_id", account.ID),
		slog.Int64("backfill_remaining", w.backfillRemaining))
	return nil
}

// stateChangedFromMigrating reports whether the account's persisted state is no
// longer "migrating" (the user disabled / deleted it, or another actor moved
// it). A read error other than not-found is treated as "no change" so a
// transient store hiccup does not abandon a finished cutover.
func (w *accountWorker) stateChangedFromMigrating(ctx context.Context) bool {
	cur, err := w.opts.store.Meta().GetIMAPImportAccount(ctx, w.opts.account.ID)
	if err != nil {
		// A deleted account (not-found) is a change; any other transient read
		// error is treated as "no change" so a finished cutover is not lost.
		return errors.Is(err, store.ErrNotFound)
	}
	return cur.State != store.IMAPImportAccountStateMigrating
}
