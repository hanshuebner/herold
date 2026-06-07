package imapimport

// idle.go implements Part A of sub-step 3d: the IDLE / NOOP-poll loop that
// replaces the single-pass attempt() with a durable running state.
//
// Design:
//   - After an initial full syncAllFolders pass the worker holds IDLE on
//     INBOX (REQ-IMAP-IMP-20).  Any unsolicited update (EXISTS / EXPUNGE /
//     FETCH) wakes the loop; a sync round runs on the second connection when
//     available (REQ-IMAP-IMP-21), then IDLE is re-armed.
//   - If the upstream did NOT advertise IDLE (RFC 2177 not in CAPABILITY),
//     the worker falls back to NOOP-poll every cfg.PollInterval
//     (REQ-IMAP-IMP-23).
//   - Second connection: opened optimistically after the primary connects; on
//     failure the loop falls back to single-connection mode and logs once.
//     Rate-limit errors increment the backoff exponent; repeated rate-limiting
//     leaves the second-connection decision in "single" mode but does NOT
//     immediately flip to errored (that happens at the accountWorker level via
//     consecutive failures). REQ-IMAP-IMP-21/73.
//   - Context cancellation cleanly closes IDLE and returns.
//   - Metrics: herold_imapimport_idle_seconds{account} (gauge) measures the
//     cumulative seconds the primary connection spent in IDLE per connect
//     session. REQ-IMAP-IMP-63.

import (
	"context"
	"log/slog"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/observe"
)

// defaultPollInterval is the NOOP-poll interval when the upstream does not
// advertise IDLE. REQ-IMAP-IMP-23.
const defaultPollInterval = 60 * time.Second

// runIDLELoop is the durable "running" phase entered after attempt() has
// completed an initial full syncAllFolders. It holds IDLE (or NOOP-polls)
// on the primary connection until ctx is cancelled or a fatal error occurs.
//
// primary is the already-authenticated, already-synced primary connection.
// Returns non-nil on a fatal error (connection drop, etc.) so the caller
// (accountWorker.attempt) can record it and trigger reconnect/backoff.
func (w *accountWorker) runIDLELoop(ctx context.Context, primary Conn) error {
	account := w.opts.account
	log := w.opts.log

	caps := primary.Caps()
	supportsIDLE := caps.Has(imap.CapIdle)

	// Try to establish a second connection for concurrent FETCH.
	// REQ-IMAP-IMP-21.
	var syncConn Conn
	useSingleConn := true // default until second connection succeeds

	if syncConn2, err := w.openSecondaryConn(ctx); err != nil {
		log.Info("imapimport: second connection unavailable; using single-connection mode",
			slog.String("account_id", account.ID),
			slog.String("reason", redactError(err)),
		)
		useSingleConn = true
	} else {
		syncConn = syncConn2
		useSingleConn = false
		defer func() {
			if logoutErr := syncConn.Logout(); logoutErr != nil {
				log.Debug("imapimport: secondary LOGOUT error",
					slog.String("account_id", account.ID),
					slog.String("error", logoutErr.Error()))
			}
			syncConn.Close()
		}()
	}

	if useSingleConn {
		syncConn = primary
	}

	// Record the connection mode in the live status (REQ-IMAP-IMP-65).
	if useSingleConn {
		w.status.setConnMode("single")
	} else {
		w.status.setConnMode("dual")
	}

	log.Info("imapimport: entering running loop",
		slog.String("account_id", account.ID),
		slog.Bool("idle", supportsIDLE),
		slog.Bool("single_conn", useSingleConn),
	)

	if supportsIDLE {
		return w.idleLoop(ctx, primary, syncConn, useSingleConn)
	}
	return w.noopPollLoop(ctx, primary, syncConn, useSingleConn)
}

// idleLoop drives the primary connection in a repeating IDLE → wake → sync
// cycle.  On IDLE support from the upstream.  REQ-IMAP-IMP-20.
func (w *accountWorker) idleLoop(ctx context.Context, primary, syncConn Conn, useSingleConn bool) error {
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

		// Block until: (a) update arrives, (b) ctx cancelled, (c) conn drop.
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
		}

		// Run sync on the sync connection (second conn or primary in
		// single-conn mode). REQ-IMAP-IMP-22 latency target is here.
		w.status.setPhase(PhaseSyncing, w.opts.clk.Now())
		if err := w.syncAfterWake(ctx, syncConn, useSingleConn); err != nil {
			// Non-fatal: log and continue the IDLE loop (don't reconnect for
			// a single failed sync round).
			log.Warn("imapimport: sync after IDLE wake failed",
				slog.String("account_id", account.ID),
				slog.String("error", err.Error()),
			)
		} else {
			w.status.recordSyncOK(w.opts.clk.Now())
		}
	}
}

// noopPollLoop drives the primary connection via periodic NOOP polling.
// Used when the upstream does not advertise IDLE.  REQ-IMAP-IMP-23.
func (w *accountWorker) noopPollLoop(ctx context.Context, primary, syncConn Conn, useSingleConn bool) error {
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

		// NOOP flushes any pending unsolicited responses on the primary.
		if err := primary.Noop(ctx); err != nil {
			return err
		}

		w.status.setPhase(PhaseSyncing, w.opts.clk.Now())
		if err := w.syncAfterWake(ctx, syncConn, useSingleConn); err != nil {
			log.Warn("imapimport: sync after NOOP poll failed",
				slog.String("account_id", account.ID),
				slog.String("error", err.Error()),
			)
		} else {
			w.status.recordSyncOK(w.opts.clk.Now())
		}
	}
}

// syncAfterWake runs a full syncAllFolders round. When using two
// connections, syncConn is the secondary; when single-conn, it is the
// primary (which is NOT in IDLE at this point — IDLE has been closed
// before this call). REQ-IMAP-IMP-22.
func (w *accountWorker) syncAfterWake(ctx context.Context, syncConn Conn, _ bool) error {
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
