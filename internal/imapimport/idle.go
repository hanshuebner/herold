package imapimport

// idle.go implements Part A of sub-step 3d: the IDLE / NOOP-poll loop that
// replaces the single-pass attempt() with a durable running state.
//
// Design:
//   - After an initial full syncAllFolders pass the worker holds IDLE on
//     INBOX (REQ-IMAP-IMP-20).  Any unsolicited update (EXISTS / EXPUNGE /
//     FETCH) wakes the loop; a sync round runs on the second connection when
//     available (REQ-IMAP-IMP-21), then IDLE is re-armed.
//   - When the upstream advertises both NOTIFY (RFC 5465) and IDLE, the
//     worker issues NOTIFY SET for SELECTED + PERSONAL before entering IDLE
//     so events from all personal mailboxes wake the loop (REQ-IMAP-IMP-27).
//     Watch-mode preference: NOTIFY+IDLE > IDLE (INBOX-only) > NOOP-poll.
//   - If the upstream did NOT advertise IDLE (RFC 2177 not in CAPABILITY),
//     the worker falls back to NOOP-poll every cfg.PollInterval
//     (REQ-IMAP-IMP-23).
//   - FETCH connection: each wake-driven sync round opens a fresh secondary
//     connection on demand and closes it when the round returns, so no FETCH
//     connection is parked between rounds where an upstream inactivity timeout
//     could silently close it and wedge every later round on a dead socket.
//     When the secondary open is refused with a rate-limit / too-many-connections
//     error the round falls back to the primary and the worker sticks to
//     single-connection mode for its life, raising the backoff exponent; a
//     non-rate-limit open failure falls back to the primary for that round only
//     and retries a fresh secondary next round. REQ-IMAP-IMP-21/73.
//   - Context cancellation cleanly closes IDLE and returns.
//   - Metrics: herold_imapimport_idle_seconds{account} (gauge) measures the
//     cumulative seconds the primary connection spent in IDLE per connect
//     session. REQ-IMAP-IMP-63.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/observe"
)

// defaultPollInterval is the NOOP-poll interval when the upstream does not
// advertise IDLE. REQ-IMAP-IMP-23.
const defaultPollInterval = 60 * time.Second

// stateRecheckInterval bounds how long an idle (no-upstream-event) worker may
// sit in IDLE before re-reading its persisted account state, so a runtime
// transition to migrating / migrated / disabled requested via JMAP or admin
// PATCH is observed without waiting for a disconnect (REQ-IMAP-IMP-90). It is
// a coarse interval — the latency-sensitive path is the upstream-event wake,
// not state polling. Driven by the injected clock, so tests with a fake clock
// that is never advanced never trip it.
const stateRecheckInterval = 30 * time.Second

// errNotifyRejected is returned by Conn.EnableNotify when the server responds
// to NOTIFY SET with NO or BAD. The caller falls back to INBOX-only IDLE and
// logs once; the worker does not flip to errored. REQ-IMAP-IMP-27.
var errNotifyRejected = errors.New("imapimport: NOTIFY SET rejected by server (NO/BAD)")

// runIDLELoop is the durable "running" phase entered after attempt() has
// completed an initial full syncAllFolders. It holds IDLE (or NOOP-polls)
// on the primary connection until ctx is cancelled or a fatal error occurs.
//
// Watch-mode preference order (REQ-IMAP-IMP-27/29):
//
//	NOTIFY+IDLE > IDLE (INBOX-only) > NOOP-poll
//
// primary is the already-authenticated, already-synced primary connection.
// Returns non-nil on a fatal error (connection drop, etc.) so the caller
// (accountWorker.attempt) can record it and trigger reconnect/backoff.
func (w *accountWorker) runIDLELoop(ctx context.Context, primary Conn) error {
	account := w.opts.account
	log := w.opts.log

	caps := primary.Caps()
	supportsIDLE := caps.Has(imap.CapIdle)
	supportsNOTIFY := caps.Has(imap.CapNotify)

	// Sync rounds open their FETCH connection on demand and close it when the
	// round returns (see syncAfterWake). No FETCH connection is held open
	// between rounds, so an upstream inactivity timeout cannot silently close a
	// parked connection and wedge every later round on a dead socket
	// (REQ-IMAP-IMP-21/22). connMode reflects whether a separate FETCH
	// connection is used at all: "single" when a sticky rate-limit fallback
	// (REQ-IMAP-IMP-73) makes the primary serve sync rounds too, "dual"
	// otherwise.
	if w.forceSingleConn {
		w.status.setConnMode("single")
	} else {
		w.status.setConnMode("dual")
	}

	log.Info("imapimport: entering running loop",
		slog.String("account_id", account.ID),
		slog.Bool("idle", supportsIDLE),
		slog.Bool("notify", supportsNOTIFY),
		slog.Bool("single_conn", w.forceSingleConn),
	)

	// Watch-mode selection (REQ-IMAP-IMP-27/29): NOTIFY+IDLE > IDLE > poll.
	switch {
	case supportsIDLE && supportsNOTIFY:
		// notifyIdleLoop sets the watch mode internally ("notify" on success,
		// "idle" on fallback after a rejected SET).
		return w.notifyIdleLoop(ctx, primary)
	case supportsIDLE:
		w.status.setWatchMode("idle")
		return w.idleLoop(ctx, primary)
	default:
		w.status.setWatchMode("poll")
		return w.noopPollLoop(ctx, primary)
	}
}

// idleLoop drives the primary connection in a repeating IDLE → wake → sync
// cycle when the upstream advertises IDLE but not NOTIFY. REQ-IMAP-IMP-20.
func (w *accountWorker) idleLoop(ctx context.Context, primary Conn) error {
	account := w.opts.account
	log := w.opts.log

	// We need INBOX selected on the primary connection so IDLE fires for
	// INBOX updates (REQ-IMAP-IMP-20).  Other folders are handled via the
	// periodic full-sync round triggered on each wake.
	if _, err := primary.Select(ctx, "INBOX"); err != nil {
		// If INBOX doesn't exist upstream yet that is non-fatal; we just
		// won't receive IDLE notifications for it.
		log.Debug("imapimport: INBOX SELECT before IDLE failed (non-fatal)",
			slog.String("account_id", account.ID),
			slog.String("error", err.Error()),
		)
	}

	return w.idleLoopCore(ctx, primary)
}

// notifyIdleLoop drives the primary connection with NOTIFY+IDLE so events from
// all personal mailboxes wake the sync loop (REQ-IMAP-IMP-27). It SELECTs
// INBOX, issues NOTIFY SET, then runs the same IDLE wake→sync cycle as
// idleLoopCore. On a server NO/BAD rejection it falls back to idleLoopCore
// (watch mode "idle") and logs once; the worker does not flip to errored.
func (w *accountWorker) notifyIdleLoop(ctx context.Context, primary Conn) error {
	account := w.opts.account
	log := w.opts.log

	// SELECT INBOX so IDLE delivers INBOX events (same as idleLoop).
	if _, err := primary.Select(ctx, "INBOX"); err != nil {
		log.Debug("imapimport: INBOX SELECT before NOTIFY+IDLE failed (non-fatal)",
			slog.String("account_id", account.ID),
			slog.String("error", err.Error()),
		)
	}

	// Issue NOTIFY SET for SELECTED (all events) + PERSONAL (core events).
	if err := primary.EnableNotify(ctx); err != nil {
		if errors.Is(err, errNotifyRejected) {
			// The server advertised NOTIFY but rejected the SET command.
			// Fall back to INBOX-only IDLE and log once (REQ-IMAP-IMP-27).
			log.Info("imapimport: NOTIFY SET rejected by server; falling back to INBOX-only IDLE",
				slog.String("account_id", account.ID),
			)
			w.status.setWatchMode("idle")
			return w.idleLoopCore(ctx, primary)
		}
		// Connection-level error; surface to the caller for reconnect/backoff.
		return err
	}

	w.status.setWatchMode("notify")
	log.Info("imapimport: NOTIFY+IDLE active; watching all personal mailboxes",
		slog.String("account_id", account.ID),
	)

	// The UnilateralDataHandler callbacks (Mailbox, Expunge, Fetch,
	// NotificationOverflow) are all wired to signalNotify() at dial time, so
	// events from any watched mailbox — and NOTIFICATIONOVERFLOW — wake the
	// loop through the same notify channel (REQ-IMAP-IMP-28). No change to the
	// core IDLE cycle is needed.
	return w.idleLoopCore(ctx, primary)
}

// idleLoopCore is the repeating IDLE → wake → sync inner loop shared by
// idleLoop and notifyIdleLoop. It assumes the mailbox is already selected and
// (for NOTIFY mode) NOTIFY SET has already been issued.
func (w *accountWorker) idleLoopCore(ctx context.Context, primary Conn) error {
	account := w.opts.account
	log := w.opts.log

	for {
		if ctx.Err() != nil {
			return nil
		}

		idleStart := w.opts.clk.Now()
		// Phase: idle (blocking in IDLE command).
		w.status.setPhase(PhaseIdle, idleStart)

		handle, err := primary.Idle(ctx)
		if err != nil {
			return err // connection-level error → reconnect
		}
		w.emitDebugEvent(ctx, "imapimport.idle.entered", "IDLE armed", nil)

		// Block until: (a) update arrives, (b) ctx cancelled, (c) conn drop,
		// (d) the periodic state-recheck timer fires (REQ-IMAP-IMP-90).
		waitDone := make(chan error, 1)
		go func() { waitDone <- handle.Wait() }()

		select {
		case <-ctx.Done():
			// Stopping: close IDLE cleanly.
			_ = handle.Close()
			<-waitDone
			// Record idle time before returning.
			w.recordIdleSeconds(idleStart)
			return nil

		case <-w.opts.clk.After(stateRecheckInterval):
			// Periodic wake to re-check the persisted state for a runtime
			// transition (e.g. enabled -> migrating). Close IDLE, then either
			// re-dispatch (state changed) or re-arm IDLE.
			_ = handle.Close()
			<-waitDone
			w.recordIdleSeconds(idleStart)
			if w.stateChangedFromEnabled(ctx) {
				return errStateChanged
			}
			continue

		case waitErr := <-waitDone:
			// Either an unsolicited update (waitErr == nil from our
			// prodIdleHandle.Wait which returns nil on notify-channel wake),
			// or a connection error.
			if waitErr != nil {
				w.recordIdleSeconds(idleStart)
				return waitErr
			}
			// Got an update: close IDLE (stop further updates being batched)
			// then run a sync round.
			_ = handle.Close()
			w.recordIdleSeconds(idleStart)
			w.emitDebugEvent(ctx, "imapimport.idle.woke", "IDLE woke; scheduling sync round", nil)
		}

		// Run sync on the sync connection (second conn or primary in
		// single-conn mode). REQ-IMAP-IMP-22 latency target is here.
		w.status.setPhase(PhaseSyncing, w.opts.clk.Now())
		syncErr := w.syncAfterWake(ctx, primary)
		if syncErr != nil {
			// Non-fatal: log and continue the IDLE loop (don't reconnect for
			// a single failed sync round).
			log.Warn("imapimport: sync after IDLE wake failed",
				slog.String("account_id", account.ID),
				slog.String("error", syncErr.Error()),
			)
			w.emitDebugEvent(ctx, "imapimport.sync.round.failed",
				"sync round after IDLE wake failed: "+syncErr.Error(), nil)
		} else {
			syncNow := w.opts.clk.Now()
			w.status.recordSyncOK(syncNow)
			w.persistSuccessAt(ctx, syncNow)
			snap := w.status.snapshot()
			w.emitDebugEvent(ctx, "imapimport.sync.round.completed",
				"sync round after IDLE wake completed",
				map[string]string{
					"messages_fetched": fmt.Sprintf("%d", snap.MessagesFetched),
					"flags_propagated": fmt.Sprintf("%d", snap.FlagsPropagated),
				})
		}

		// A runtime transition observed after the sync round re-dispatches
		// (REQ-IMAP-IMP-90).
		if w.stateChangedFromEnabled(ctx) {
			return errStateChanged
		}
	}
}

// noopPollLoop drives the primary connection via periodic NOOP polling.
// Used when the upstream does not advertise IDLE.  REQ-IMAP-IMP-23.
func (w *accountWorker) noopPollLoop(ctx context.Context, primary Conn) error {
	account := w.opts.account
	log := w.opts.log

	pollInterval := w.opts.cfg.PollInterval.AsDuration()
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	for {
		// Phase: polling — waiting for the next NOOP tick.
		nextPoll := w.opts.clk.Now().Add(pollInterval)
		w.status.setPhase(PhasePolling, w.opts.clk.Now())
		w.status.setNextPoll(nextPoll)

		select {
		case <-ctx.Done():
			return nil
		case <-w.opts.clk.After(pollInterval):
		}

		log.Debug("imapimport: NOOP poll tick",
			slog.String("account_id", account.ID),
			slog.Duration("interval", pollInterval),
		)
		w.emitDebugEvent(ctx, "imapimport.noop.tick",
			fmt.Sprintf("NOOP poll tick (interval %s)", pollInterval), nil)

		// NOOP flushes any pending unsolicited responses on the primary.
		if err := primary.Noop(ctx); err != nil {
			return err
		}

		w.status.setPhase(PhaseSyncing, w.opts.clk.Now())
		if syncErr := w.syncAfterWake(ctx, primary); syncErr != nil {
			log.Warn("imapimport: sync after NOOP poll failed",
				slog.String("account_id", account.ID),
				slog.String("error", syncErr.Error()),
			)
			w.emitDebugEvent(ctx, "imapimport.sync.round.failed",
				"sync round after NOOP poll failed: "+syncErr.Error(), nil)
		} else {
			pollNow := w.opts.clk.Now()
			w.status.recordSyncOK(pollNow)
			w.persistSuccessAt(ctx, pollNow)
			snap := w.status.snapshot()
			w.emitDebugEvent(ctx, "imapimport.sync.round.completed",
				"sync round after NOOP poll completed",
				map[string]string{
					"messages_fetched": fmt.Sprintf("%d", snap.MessagesFetched),
					"flags_propagated": fmt.Sprintf("%d", snap.FlagsPropagated),
				})
		}

		// Observe a runtime state transition between polls (REQ-IMAP-IMP-90).
		if w.stateChangedFromEnabled(ctx) {
			return errStateChanged
		}
	}
}

// syncAfterWake runs a full syncAllFolders round on a FETCH connection opened
// on demand for this round and closed when it returns, so no FETCH connection
// is parked between rounds — an upstream inactivity timeout would otherwise
// silently close a parked connection and every later round would fail on the
// dead socket (REQ-IMAP-IMP-22).
//
// In sticky single-connection mode (a prior rate-limit / too-many-connections
// rejection, REQ-IMAP-IMP-73), or when opening the on-demand connection is
// refused, the round runs over the primary, which is NOT in IDLE at this point
// (IDLE has been closed before this call).
func (w *accountWorker) syncAfterWake(ctx context.Context, primary Conn) error {
	if w.forceSingleConn {
		return w.syncAllFolders(ctx, primary)
	}

	syncConn, err := w.openSecondaryConn(ctx)
	if err != nil {
		// A rate-limit rejection makes single-connection mode sticky and raises
		// the backoff exponent (REQ-IMAP-IMP-73); any other open failure is
		// transient and a fresh secondary is retried on the next round. Either
		// way, run this round over the primary so the wake is not dropped.
		w.noteSecondaryConnError(err)
		if w.forceSingleConn {
			w.status.setConnMode("single")
		}
		w.opts.log.Info("imapimport: on-demand FETCH connection unavailable; using primary for this round",
			slog.String("account_id", w.opts.account.ID),
			slog.String("reason", redactError(err)),
		)
		return w.syncAllFolders(ctx, primary)
	}
	defer func() {
		if logoutErr := syncConn.Logout(); logoutErr != nil {
			w.opts.log.Debug("imapimport: on-demand FETCH connection LOGOUT error",
				slog.String("account_id", w.opts.account.ID),
				slog.String("error", logoutErr.Error()))
		}
		syncConn.Close()
	}()
	return w.syncAllFolders(ctx, syncConn)
}

// openSecondaryConn dials a new authenticated connection to the same
// upstream using the same credentials as the primary.  Returns the Conn on
// success, or an error if the second connection is unavailable.
// REQ-IMAP-IMP-21/73.
func (w *accountWorker) openSecondaryConn(ctx context.Context) (Conn, error) {
	account := w.opts.account

	credPlaintext, err := w.openCredential(ctx, account)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return conn, nil
}

// recordIdleSeconds adds the time spent since idleStart to the
// herold_imapimport_idle_seconds gauge.
func (w *accountWorker) recordIdleSeconds(idleStart time.Time) {
	elapsed := w.opts.clk.Now().Sub(idleStart).Seconds()
	observe.IMAPImportIdleSeconds.WithLabelValues(w.opts.account.ID).Add(elapsed)
}
