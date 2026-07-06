package imapimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// errStateChanged is returned by the running IDLE / poll loops (and the
// migration path) when the account's persisted state has moved away from the
// state the current dispatch was started for. run() treats it as a clean
// re-dispatch signal, NOT a connection failure, so the backoff / consecutive-
// failure counters are untouched (REQ-IMAP-IMP-90/95).
var errStateChanged = errors.New("imapimport: account state changed; re-dispatch")

// maxConsecutiveFailures is the number of consecutive connection
// failures after which the account is flipped to state=errored and
// the worker stops reconnecting (REQ-IMAP-IMP-25).
const maxConsecutiveFailures = 20

// backoffBase is the initial retry delay after the first failure.
const backoffBase = 2 * time.Second

// backoffCap is the maximum retry delay.
const backoffCap = 5 * time.Minute

// maxRateLimitBackoffShift caps the extra backoff exponent accumulated from
// upstream rate-limit / too-many-connections responses (REQ-IMAP-IMP-73). It
// is added on top of the consecutive-failure exponent in backoffDuration so a
// rate-limited account waits materially longer before reconnecting, without
// letting the shift run away. backoffDuration already caps the total delay at
// backoffCap, so this bound mainly limits how quickly the cap is reached.
const maxRateLimitBackoffShift = 6

// accountWorkerOpts bundles the constructor parameters for
// accountWorker. All fields are required unless noted.
type accountWorkerOpts struct {
	account     store.IMAPImportAccount
	store       store.Store
	dataKey     []byte
	categoriser Categoriser
	cfg         sysconfig.IMAPImportConfig
	log         *slog.Logger
	clk         clock.Clock
	dialer      Dialer

	// maxConsecutiveFailures overrides the package-level constant for
	// tests that need a small M to keep execution fast. Zero means use
	// the package constant.
	maxConsecutiveFailures int
}

// accountWorker is the per-account supervising goroutine. It manages
// the lifecycle: connecting → running → (backoff on failure) → errored.
//
// In sub-step 3a the "running" phase is trivially: connect, record
// capabilities (for single-connection-fallback decision, REQ-IMAP-IMP-21),
// and cleanly disconnect. Sub-steps 3b-3e extend the running phase with
// folder sync, IDLE, and write-back.
type accountWorker struct {
	opts        accountWorkerOpts
	consecutive int // consecutive connection failures

	// status is the live, mutex-guarded status of this worker.
	// Read via workerStatus.snapshot(); mutated via setPhase etc.
	// REQ-IMAP-IMP-65.
	status workerStatus

	// backfillRemaining accumulates, across one full sync pass, the number of
	// in-horizon upstream UIDs below each folder's low-water mark that are not
	// yet mirrored (REQ-IMAP-IMP-63). syncAllFolders / syncAllFoldersGmail
	// reset it to 0 at the start of a pass and publish the sum to the
	// per-account herold_imapimport_backfill_remaining gauge at the end; each
	// folder pass adds its own count. Accessed only from the worker's single
	// supervising goroutine, so it needs no synchronisation.
	backfillRemaining int64

	// testTokenSource, when non-nil, overrides the OAuth token source
	// used in openCredential. Set in tests to inject a fakeTokenSource
	// without wiring a real HTTP endpoint. Production code leaves this nil.
	testTokenSource oauthTokenSource

	// forceSingleConn is set once the upstream rejects a second / write-back
	// connection with a rate-limit / too-many-connections response
	// (REQ-IMAP-IMP-21/73). It is sticky for the worker's lifetime: subsequent
	// sessions skip the second-connection attempt entirely and run in
	// single-connection mode, so the worker stops hammering a connection-capped
	// upstream. Accessed only from the worker's supervising goroutine.
	forceSingleConn bool

	// rateLimitBackoffShift is the extra backoff exponent accumulated from
	// upstream rate-limit responses (REQ-IMAP-IMP-73), added on top of the
	// consecutive-failure exponent in backoffDuration. Reset to 0 on a fully
	// successful enabled session alongside the consecutive counter. Capped at
	// maxRateLimitBackoffShift.
	rateLimitBackoffShift int
}

func newAccountWorker(opts accountWorkerOpts) *accountWorker {
	// Ensure metrics are registered; idempotent (sync.Once inside).
	// Called here (in addition to Pool construction) so tests that
	// create workers directly without a Pool still have valid collectors.
	observe.RegisterIMAPImportMetrics()
	w := &accountWorker{opts: opts}
	// Initialise the static identity fields of the live status once.
	w.status.mu.Lock()
	w.status.accountID = opts.account.ID
	w.status.principalID = fmt.Sprint(opts.account.PrincipalID)
	w.status.accountName = opts.account.AccountName
	w.status.host = opts.account.Host
	w.status.phase = PhaseStarting
	w.status.phaseSince = opts.clk.Now()
	w.status.debugLog = opts.account.DebugLog
	w.status.mu.Unlock()
	return w
}

// run is the main loop of the accountWorker. It runs until ctx is
// cancelled or the account reaches a terminal state (errored, migrated,
// disabled, or deleted).
//
// On each iteration the authoritative account state is re-read from the
// store so a transition requested at runtime via JMAP IMAPImport/set or the
// admin PATCH surface (enabled -> migrating, or migrated -> enabled) is
// observed even on an already-running worker — the running IDLE / poll loops
// also re-check the state and return errStateChanged on a change so this
// loop re-dispatches promptly (REQ-IMAP-IMP-90/95).
func (w *accountWorker) run(ctx context.Context) {
	defer w.status.setPhase(PhaseStopped, w.opts.clk.Now())

	for {
		if ctx.Err() != nil {
			return
		}

		// Re-read the authoritative state so a runtime transition is picked
		// up. A missing row (account deleted) or a terminal state stops the
		// worker. (REQ-IMAP-IMP-90/93/95.)
		if cur, err := w.opts.store.Meta().GetIMAPImportAccount(ctx, w.opts.account.ID); err == nil {
			w.refreshAccount(cur)
		} else if errors.Is(err, store.ErrNotFound) {
			w.opts.log.Info("imapimport: account row gone; stopping worker",
				slog.String("account_id", w.opts.account.ID))
			return
		} else if ctx.Err() != nil {
			return
		}
		switch w.opts.account.State {
		case store.IMAPImportAccountStateDisabled,
			store.IMAPImportAccountStateErrored,
			store.IMAPImportAccountStateMigrated:
			// Terminal / stopped: no worker activity. (REQ-IMAP-IMP-93.)
			return
		}

		// Transition to connecting before each attempt.
		w.status.setPhase(PhaseConnecting, w.opts.clk.Now())

		// Dispatch on the account state. migrating runs the one-shot complete
		// backfill and, on success, advances to migrated and stops; enabled
		// runs the perpetual mirror. (REQ-IMAP-IMP-90/91.)
		var err error
		migrating := w.opts.account.State == store.IMAPImportAccountStateMigrating
		if migrating {
			err = w.attemptMigration(ctx)
			if err == nil {
				// Cutover complete: account is now migrated, worker stops.
				return
			}
		} else {
			err = w.attempt(ctx)
		}

		// A clean state change (e.g. enabled -> migrating mid-IDLE, or the
		// account re-read above already moved on) is not a failure: loop and
		// re-dispatch without touching the backoff / consecutive counters.
		if errors.Is(err, errStateChanged) {
			continue
		}

		if !migrating && err == nil {
			// Successful enabled session: record success, reset failure counter
			// and the rate-limit backoff exponent (REQ-IMAP-IMP-73). The
			// forceSingleConn decision stays sticky — a connection-capped
			// upstream does not regain a second connection just because one
			// single-conn session succeeded.
			w.consecutive = 0
			w.rateLimitBackoffShift = 0
			w.status.setConsecutiveFailures(0)
			now := w.opts.clk.Now()
			if setErr := w.opts.store.Meta().SetIMAPImportAccountState(
				ctx,
				w.opts.account.ID,
				store.IMAPImportAccountStateEnabled,
				"",
				&now,
			); setErr != nil {
				w.opts.log.Warn("imapimport: failed to persist last_success_at",
					slog.String("error", setErr.Error()))
			}
			// The running phase loops internally (IDLE + fetch rounds) and only
			// returns here on a disconnection, so fall through to reconnect.
			continue
		}

		// err != nil: a connection / sync failure on either path.
		if stop := w.handleAttemptFailure(ctx, err); stop {
			return
		}
	}
}

// handleAttemptFailure records a failed connection/sync attempt: it counts the
// error for metrics and the live snapshot, flips the account to errored after
// M consecutive failures (REQ-IMAP-IMP-25), and otherwise sleeps the backoff.
// Returns true when the worker should stop (errored or ctx cancelled).
func (w *accountWorker) handleAttemptFailure(ctx context.Context, err error) (stop bool) {
	// Classify the error kind for metrics — without revealing credential
	// content in any label value.
	kind := classifyErrorKind(err)
	observe.IMAPImportConnectionErrorsTotal.WithLabelValues(
		w.opts.account.ID,
		kind,
	).Inc()

	// An upstream rate-limit / too-many-connections response forces
	// single-connection mode for subsequent sessions and raises the backoff
	// exponent so the worker backs off harder (REQ-IMAP-IMP-73). Repeated
	// rate-limiting still accumulates consecutive failures and ultimately flips
	// the account to errored below.
	if kind == "rate_limit" {
		w.forceSingleConn = true
		if w.rateLimitBackoffShift < maxRateLimitBackoffShift {
			w.rateLimitBackoffShift++
		}
	}

	w.consecutive++
	redacted := redactError(err)
	w.status.setConsecutiveFailures(w.consecutive)
	w.status.setLastError(redacted)
	w.opts.log.Warn("imapimport: connection attempt failed",
		slog.String("account_id", w.opts.account.ID),
		slog.String("kind", kind),
		slog.String("error", redacted),
		slog.Int("consecutive_failures", w.consecutive),
	)

	limit := maxConsecutiveFailures
	if w.opts.maxConsecutiveFailures > 0 {
		limit = w.opts.maxConsecutiveFailures
	}
	if w.consecutive >= limit {
		// Flip to errored; worker stops (REQ-IMAP-IMP-25). A migrating account
		// that cannot reach the upstream errors like any other; the user
		// re-enables (which resumes the cutover) once the cause is fixed.
		lastErr := fmt.Sprintf("connection failed after %d consecutive attempts: %s",
			w.consecutive, redacted)
		w.status.setPhase(PhaseErrored, w.opts.clk.Now())
		w.status.setLastError(lastErr)
		if setErr := w.opts.store.Meta().SetIMAPImportAccountState(
			ctx,
			w.opts.account.ID,
			store.IMAPImportAccountStateErrored,
			lastErr,
			nil,
		); setErr != nil {
			w.opts.log.Warn("imapimport: failed to persist errored state",
				slog.String("error", setErr.Error()))
		}
		w.opts.log.Error("imapimport: account errored after M consecutive failures; stopping worker",
			slog.String("account_id", w.opts.account.ID),
			slog.Int("max_failures", limit),
		)
		return true
	}

	// Exponential backoff with jitter (REQ-IMAP-IMP-25), raised by any
	// accumulated rate-limit shift (REQ-IMAP-IMP-73).
	delay := backoffDurationShift(w.consecutive, w.rateLimitBackoffShift)
	w.status.setPhase(PhaseBackoff, w.opts.clk.Now())
	w.opts.log.Info("imapimport: waiting before retry",
		slog.String("account_id", w.opts.account.ID),
		slog.Duration("delay", delay),
	)
	select {
	case <-ctx.Done():
		return true
	case <-w.opts.clk.After(delay):
	}
	return false
}

// refreshAccount copies the durable fields that can change at runtime from a
// freshly-read account row onto the worker's in-memory copy, so the next
// dispatch and the running loops see the current state, backfill floor,
// provenance label, delete policy, and debug-log flag. Called only from the
// worker's own goroutine between attempts, so no other goroutine reads
// w.opts.account concurrently. The cached identity fields in w.status are
// left untouched (account_id / host do not change).
func (w *accountWorker) refreshAccount(cur store.IMAPImportAccount) {
	w.opts.account.State = cur.State
	w.opts.account.BackfillFloorDate = cur.BackfillFloorDate
	w.opts.account.AccountName = cur.AccountName
	w.opts.account.DeletePropagates = cur.DeletePropagates
	w.opts.account.ProvenanceMailboxID = cur.ProvenanceMailboxID
	w.opts.account.DebugLog = cur.DebugLog
	w.status.setDebugLog(cur.DebugLog)
}

// emitDebugEvent appends a system event tagged with the account ID when
// per-account debug logging is enabled (re #138). It is a no-op (zero
// overhead) when DebugLog is false. The event is tagged with the account ID
// as ActorID so the admin Events view can filter by account.
func (w *accountWorker) emitDebugEvent(ctx context.Context, action, message string, metadata map[string]string) {
	if !w.opts.account.DebugLog {
		return
	}
	ev := store.SystemEvent{
		At:       w.opts.clk.Now(),
		Action:   action,
		ActorID:  w.opts.account.ID,
		Subject:  "imapimport:" + w.opts.account.ID,
		Outcome:  store.OutcomeSuccess,
		Message:  message,
		Metadata: metadata,
		// Domain is left empty: the worker does not know which herold-managed
		// domain the principal belongs to. Only super-admins see these events
		// in the scoped Events view; domain-scoped operators do not.
	}
	if err := w.opts.store.Meta().AppendSystemEvent(ctx, ev); err != nil {
		w.opts.log.Debug("imapimport: failed to append debug system event",
			slog.String("account_id", w.opts.account.ID),
			slog.String("action", action),
			slog.String("error", err.Error()))
	}
}

// authorityIsHerold reports whether herold (not the upstream) is the
// authoritative source for already-mirrored mail. True once the
// complete-migration cutover has begun (migrating) or finished (migrated):
// from that point the reconcile MUST NOT apply the upstream-authoritative
// overwrite of REQ-IMAP-IMP-42 to herold-side values (REQ-IMAP-IMP-92).
func (w *accountWorker) authorityIsHerold() bool {
	return w.opts.account.State == store.IMAPImportAccountStateMigrating ||
		w.opts.account.State == store.IMAPImportAccountStateMigrated
}

// stateChangedFromEnabled re-reads the account state and reports whether it is
// no longer "enabled" (a runtime transition to migrating/migrated/disabled or
// a delete). The running IDLE / poll loops call this so a transition is
// observed without waiting for a disconnect (REQ-IMAP-IMP-90). As a side
// effect it refreshes the debug-log flag so a runtime toggle takes effect
// within the stateRecheckInterval without a reconnect (re #138). A read error
// is treated as "no change" so a transient store hiccup does not bounce the
// loop.
func (w *accountWorker) stateChangedFromEnabled(ctx context.Context) bool {
	cur, err := w.opts.store.Meta().GetIMAPImportAccount(ctx, w.opts.account.ID)
	if err != nil {
		return errors.Is(err, store.ErrNotFound)
	}
	// Refresh the debug-log flag while we have the row — this lets an operator
	// toggle debugging without waiting for a full reconnect cycle.
	w.opts.account.DebugLog = cur.DebugLog
	w.status.setDebugLog(cur.DebugLog)
	return cur.State != store.IMAPImportAccountStateEnabled
}

// attempt performs one connection attempt: open credential, dial,
// check capabilities, record single-connection fallback decision,
// then disconnect cleanly. Returns nil on success.
//
// In 3b-3e this will be extended to run the folder-sync + IDLE loop
// before returning.
func (w *accountWorker) attempt(ctx context.Context) error {
	account := w.opts.account

	// Reject password/app_password when the operator disabled plain auth
	// (REQ-IMAP-IMP-05). xoauth2 is unaffected.
	if !w.opts.cfg.IMAPImportAllowPassword() &&
		(account.AuthMethod == store.IMAPImportAuthMethodPassword ||
			account.AuthMethod == store.IMAPImportAuthMethodAppPassword) {
		return fmt.Errorf("imapimport: auth_method=%s rejected: allow_password=false in sysconfig (REQ-IMAP-IMP-05)",
			account.AuthMethod)
	}

	// Open credential immediately before connect; never retained
	// beyond this function. (REQ-IMAP-IMP-70.)
	credPlaintext, err := w.openCredential(ctx, account)
	if err != nil {
		return fmt.Errorf("imapimport: credential open failed: %w", err)
	}

	// Dial + authenticate.
	conn, err := w.opts.dialer.Dial(ctx, dialParams{
		AccountID:           account.ID,
		Host:                account.Host,
		Port:                account.Port,
		TLSMode:             string(account.TLSMode),
		Username:            account.Username,
		AuthMethod:          string(account.AuthMethod),
		CredentialPlaintext: credPlaintext,
	})
	// Zero the plaintext as soon as the dial call returns; the
	// credential is now either in the Conn (if authed) or discarded
	// (on error). Either way we do not keep it.
	credPlaintext = ""
	_ = credPlaintext

	if err != nil {
		w.status.setConnected(false)
		return err
	}
	w.status.setConnected(true)
	w.emitDebugEvent(ctx, "imapimport.dial.connected",
		"connected and authenticated to "+account.Host,
		map[string]string{"host": account.Host, "username": account.Username})
	defer func() {
		w.status.setConnected(false)
		// Orderly close: LOGOUT then close the TCP connection.
		if logoutErr := conn.Logout(); logoutErr != nil {
			w.opts.log.Debug("imapimport: LOGOUT error (non-fatal)",
				slog.String("account_id", account.ID),
				slog.String("error", logoutErr.Error()))
		}
		conn.Close()
		w.emitDebugEvent(ctx, "imapimport.dial.closed",
			"connection to "+account.Host+" closed",
			map[string]string{"host": account.Host})
	}()

	// Record single-connection-fallback capability for later sub-steps.
	// (REQ-IMAP-IMP-21 — just carry the decision in 3a.)
	_ = conn.Caps() // caps are available; used in 3b+ to decide IDLE vs poll

	w.opts.log.Info("imapimport: connected successfully",
		slog.String("account_id", account.ID),
		slog.String("host", account.Host),
	)

	// Ensure the per-account provenance label exists and is cached before the
	// first ingest (REQ-IMAP-IMP-100..101). A failure here is non-fatal: the
	// mirror can still run without the label; it will be retried next session.
	if err := w.ensureProvenanceMailbox(ctx); err != nil {
		w.opts.log.Warn("imapimport: failed to ensure provenance label; continuing without it",
			slog.String("account_id", account.ID),
			slog.String("error", err.Error()))
	}

	// 3b: drive a full sync pass for all mapped folders.
	w.status.setPhase(PhaseSyncing, w.opts.clk.Now())
	if err := w.syncAllFolders(ctx, conn); err != nil {
		// Sync errors are logged inside syncAllFolders per folder; a
		// non-nil return means all folders failed. Treat as a connection-
		// level error so the backoff + errored logic applies.
		return fmt.Errorf("imapimport: syncAllFolders: %w", err)
	}
	now := w.opts.clk.Now()
	w.status.recordSyncOK(now)
	w.persistSuccessAt(ctx, now)

	// 3d-B: start the write-back goroutine on a dedicated third connection.
	// If the third connection fails we start the write-back goroutine in
	// degraded mode (no write-back) but still run the IDLE loop.
	wbCtx, wbCancel := context.WithCancel(ctx)
	var wbWg sync.WaitGroup
	if w.forceSingleConn {
		// A rate-limited / connection-capped upstream forced single-connection
		// mode (REQ-IMAP-IMP-21/73); do not open the dedicated write-back
		// connection either. Flag changes still reconcile on the next sync
		// round over the primary connection.
		w.opts.log.Info("imapimport: rate-limit fallback active; skipping write-back connection",
			slog.String("account_id", account.ID),
		)
	} else if wbConn, wbErr := w.openSecondaryConn(wbCtx); wbErr == nil {
		wbWg.Add(1)
		go func() {
			defer wbWg.Done()
			w.runWriteBack(wbCtx, wbConn)
		}()
	} else {
		// A rate-limit / too-many-connections rejection of the write-back
		// connection is the same signal as for the second sync connection:
		// drop to single-connection mode and raise the backoff
		// (REQ-IMAP-IMP-21/73).
		w.noteSecondaryConnError(wbErr)
		w.opts.log.Info("imapimport: write-back connection unavailable; skipping write-back for this session",
			slog.String("account_id", account.ID),
			slog.String("error", redactError(wbErr)),
		)
	}
	defer func() {
		wbCancel()
		wbWg.Wait()
	}()

	// 3d-A: enter the durable IDLE / poll loop now that the initial sync is
	// done. runIDLELoop returns only on ctx cancellation (nil) or a fatal
	// connection error (non-nil). A nil return causes run() to record success
	// and reconnect; a non-nil return triggers the backoff path.
	return w.runIDLELoop(ctx, conn)
}

// persistSuccessAt records a successful sync round to the durable store by
// writing last_success_at. Called after every completed syncAllFolders pass
// (the initial pass in attempt and each wake-driven round in the IDLE / poll
// loops) so the settings card shows current sync status without waiting for
// session end. Best-effort: a failure is logged and does not abort the loop.
func (w *accountWorker) persistSuccessAt(ctx context.Context, now time.Time) {
	if setErr := w.opts.store.Meta().SetIMAPImportAccountState(
		ctx,
		w.opts.account.ID,
		store.IMAPImportAccountStateEnabled,
		"",
		&now,
	); setErr != nil {
		w.opts.log.Warn("imapimport: failed to persist last_success_at after sync round",
			slog.String("account_id", w.opts.account.ID),
			slog.String("error", setErr.Error()),
		)
	}
}

// openCredential decrypts the account's sealed credential using the
// pool's data key. For xoauth2 accounts it additionally exchanges the
// refresh token for an access token via the configured token source.
// Returns the plaintext string; caller must zero it as soon as it is
// no longer needed. (REQ-IMAP-IMP-70.)
func (w *accountWorker) openCredential(ctx context.Context, account store.IMAPImportAccount) (string, error) {
	rawBytes, err := secrets.Open(w.opts.dataKey, account.CredentialCT)
	if err != nil {
		return "", fmt.Errorf("secrets.Open: %w", err)
	}

	if account.AuthMethod != store.IMAPImportAuthMethodXOAuth2 {
		// password / app_password: raw bytes are the credential.
		plain := string(rawBytes)
		// zero rawBytes immediately
		for i := range rawBytes {
			rawBytes[i] = 0
		}
		return plain, nil
	}

	// xoauth2: rawBytes is the sealed refresh token; exchange for access token.
	refreshToken := string(rawBytes)
	for i := range rawBytes {
		rawBytes[i] = 0
	}

	providerKey := account.Host // callers may override; use host as default key
	// Allow the configured account to carry a provider hint via Username
	// domain, but that's 3b+. For now look up the first oauth provider that
	// matches any entry, or fall back to the host name.
	ts, err := w.tokenSourceForProvider(ctx, providerKey, refreshToken)
	if err != nil {
		return "", err
	}

	accessToken, err := ts.Token(ctx, refreshToken)
	refreshToken = "" // zero immediately
	if err != nil {
		return "", fmt.Errorf("imapimport: token exchange failed: %w", err)
	}
	return accessToken, nil
}

// tokenSourceForProvider returns an oauthTokenSource for the named
// provider. In 3a we pick the only configured OAuth provider if there
// is exactly one, or return an error if zero/multiple are configured
// without a matching key. 3b will wire the per-account provider name.
//
// When the worker's testTokenSource override is set (test injection),
// that is returned without consulting the sysconfig OAuth map.
func (w *accountWorker) tokenSourceForProvider(_ context.Context, _ string, _ string) (oauthTokenSource, error) {
	// Test injection: bypass the real token endpoint.
	if w.testTokenSource != nil {
		return w.testTokenSource, nil
	}

	oauthCfg := w.opts.cfg.OAuth
	if len(oauthCfg) == 0 {
		return nil, fmt.Errorf("imapimport: xoauth2 requested but no [imap_import.oauth.*] provider configured")
	}
	// Pick the first (and typically only) provider; 3b will match by name.
	for _, prov := range oauthCfg {
		return newHTTPTokenSource(prov, nil), nil
	}
	return nil, fmt.Errorf("imapimport: no suitable OAuth provider found")
}

// backoffDuration returns the exponential backoff delay for the n-th
// failure (1-indexed), capped at backoffCap and jittered by ±25 %.
func backoffDuration(n int) time.Duration {
	return backoffDurationShift(n, 0)
}

// backoffDurationShift is backoffDuration with an additional exponent applied
// on top of the consecutive-failure exponent (REQ-IMAP-IMP-73). extraShift is
// accumulated from upstream rate-limit responses so a rate-limited account
// waits materially longer than one failing only on transient network errors.
func backoffDurationShift(n, extraShift int) time.Duration {
	if n < 1 {
		n = 1
	}
	if extraShift < 0 {
		extraShift = 0
	}
	// shift n-1 so first failure -> base, plus any rate-limit exponent.
	shift := n - 1 + extraShift
	if shift > 20 {
		shift = 20 // prevent overflow
	}
	d := backoffBase << uint(shift)
	if d > backoffCap || d < 0 {
		d = backoffCap
	}
	// ±25% jitter
	jitter := time.Duration(rand.Int64N(int64(d/2))) - d/4
	d += jitter
	if d < time.Second {
		d = time.Second
	}
	if d > backoffCap {
		d = backoffCap
	}
	return d
}

// classifyErrorKind returns a short tag suitable for use as a Prometheus
// label value describing the error class. It MUST NOT include any
// credential or token material.
//
// Ordering: TLS/cert checks come before auth checks because x509 error
// strings can contain words like "authority" that might otherwise match
// a broad auth pattern.
func classifyErrorKind(err error) string {
	if err == nil {
		return ""
	}
	// Lower-case the message so detection is case-insensitive — real upstreams
	// phrase rate-limit responses as "Too many connections", "Rate Limited",
	// etc. (REQ-IMAP-IMP-73). The match patterns below are all lower-case.
	msg := toLower(err.Error())
	switch {
	case contains(msg, "tls") || contains(msg, "certificate") || contains(msg, "x509"):
		return "tls"
	case contains(msg, "too many") || contains(msg, "rate") || contains(msg, "quota"):
		return "rate_limit"
	case contains(msg, "connection") || contains(msg, "dial") || contains(msg, "network") ||
		contains(msg, "timeout") || contains(msg, "refused") || contains(msg, "reset"):
		return "network"
	case contains(msg, "auth") || contains(msg, "credential") || contains(msg, "login") ||
		contains(msg, "password") || contains(msg, "token") || contains(msg, "authenticate"):
		return "auth"
	default:
		return "other"
	}
}

// noteSecondaryConnError inspects a failed secondary / write-back connection
// open. When the upstream rejected it with a rate-limit / too-many-connections
// error (REQ-IMAP-IMP-21/73), it forces single-connection mode for the rest of
// the worker's life and raises the rate-limit backoff exponent so the next
// backoff is materially longer. Non-rate-limit failures (e.g. an upstream that
// simply caps at one connection) still fall back to single-connection mode in
// runIDLELoop, but do NOT raise the backoff — they are not a throttling signal.
func (w *accountWorker) noteSecondaryConnError(err error) {
	if classifyErrorKind(err) != "rate_limit" {
		return
	}
	w.forceSingleConn = true
	if w.rateLimitBackoffShift < maxRateLimitBackoffShift {
		w.rateLimitBackoffShift++
	}
}

// redactError returns a safe-to-log version of the error. It strips any
// substring that looks like it might carry credential material by
// replacing everything after known sentinel words with "[redacted]".
// This is best-effort; the real guard is that credentials are never
// included in error values produced by this package (REQ-IMAP-IMP-71).
func redactError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// toLower is strings.ToLower restricted to ASCII A-Z, kept local to avoid
// importing strings here (matching contains below). Error classification only
// needs ASCII-case folding of IMAP response text.
func toLower(s string) string {
	var changed bool
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			changed = true
			break
		}
	}
	if !changed {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// contains is strings.Contains without importing strings here.
func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}
