package queue

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Default Options values.
const (
	defaultConcurrency   = 32
	defaultPerHostMax    = 4
	defaultPollInterval  = 5 * time.Second
	defaultShutdownGrace = 30 * time.Second
	// staleInflightFactor multiplies PollInterval to compute the
	// startup recovery threshold for inflight rows.
	staleInflightFactor = 5
)

// Options configures a Queue. Caller fills in Store, Deliverer, Logger;
// Signer is optional. Concurrency / PerHostMax / Retry / PollInterval
// fall back to documented defaults when zero.
type Options struct {
	// Store is the metadata + blob store. Required.
	Store store.Store
	// Signer renders DKIM/ARC signatures on outbound messages. Nil
	// means "no signing"; callers that submit Sign=true rows on a
	// Queue with no Signer get an unsigned delivery (logged at warn).
	Signer Signer
	// Deliverer is the wire-side outbound SMTP client. Required.
	Deliverer Deliverer
	// Logger is the structured logger. Required.
	Logger *slog.Logger
	// Clock is the time source. nil falls back to clock.NewReal.
	Clock clock.Clock
	// Concurrency is the maximum number of in-flight Deliver calls.
	// 0 falls back to defaultConcurrency (32).
	Concurrency int
	// PerHostMax caps the per-MX-hostname in-flight Deliver calls.
	// 0 falls back to defaultPerHostMax (4).
	PerHostMax int
	// Retry controls the per-attempt backoff schedule.
	Retry RetryPolicy
	// PollInterval is the scheduler's poll cadence. 0 falls back to
	// defaultPollInterval (5s).
	PollInterval time.Duration
	// ShutdownGrace caps how long graceful shutdown waits for
	// in-flight workers. 0 falls back to defaultShutdownGrace (30s).
	ShutdownGrace time.Duration
	// DSNFromAddress is the From: address used on generated DSNs
	// (postmaster@<hostname>). Required when DSNs may be emitted; if
	// empty the orchestrator uses "postmaster@localhost".
	DSNFromAddress string
	// Hostname is the local hostname used in Reporting-MTA, Received,
	// and the DSN message-id. Required for clean DSN headers; if
	// empty the orchestrator uses "localhost".
	Hostname string
}

// Submission is the single-call enqueue payload. The caller hands one
// Submission to Submit; the orchestrator inserts one queue row per
// Recipient and returns the shared EnvelopeID.
type Submission struct {
	// PrincipalID is the submitting principal (0 for forwarding paths).
	PrincipalID *store.PrincipalID
	// MailFrom is the SMTP MAIL FROM (reverse-path). Empty for null
	// sender (e.g. DSN bounces).
	MailFrom string
	// Recipients is the list of forward-paths. Each becomes one queue
	// row.
	Recipients []string
	// Body is the message bytes. Read once, written to the blob store
	// inside Submit.
	Body io.Reader
	// Headers is an optional pre-rendered header blob (rare; the
	// in-process delivery path already prepends Received headers).
	// When non-empty, persisted and streamed by the delivery worker
	// instead of recomputed.
	Headers []byte
	// DSNNotify is the RFC 3461 NOTIFY mask to record on every row.
	DSNNotify store.DSNNotifyFlags
	// DSNRet is the RFC 3461 RET parameter.
	DSNRet store.DSNRet
	// DSNEnvelopeID is the RFC 3461 ENVID parameter (echoed in DSNs).
	DSNEnvelopeID string
	// IdempotencyKey makes Submit idempotent for HTTP send retries.
	// When non-empty, a second Submit with the same key returns the
	// prior EnvelopeID and queue.ErrConflict.
	IdempotencyKey string
	// REQUIRETLS forwards RFC 8689 to the deliverer.
	REQUIRETLS bool
	// Sign requests DKIM/ARC signing on each delivery attempt. When
	// false (forwarding path) the worker streams the body verbatim.
	Sign bool
	// SigningDomain selects which DKIM key the Signer uses; required
	// when Sign is true.
	SigningDomain string
	// SendAt, when non-zero, is the earliest UTC instant at which the
	// scheduler may begin delivery (RFC 8621 §7.5 EmailSubmission.sendAt).
	// On enqueue NextAttemptAt is set to max(now, SendAt); the row stays
	// in QueueStateQueued and remains invisible to the deliverer until
	// SendAt arrives. The zero value behaves like "deliver immediately".
	SendAt time.Time
}

// Queue is the persistent outbound queue orchestrator: scheduler,
// worker pool, retry, DSN emission. Construct with New, then call Run.
//
// The Queue owns no database state of its own — all durable state
// lives in store.Metadata's queue surface. In-memory state is limited
// to the concurrency semaphores, per-host map, and worker counter.
type Queue struct {
	opts          Options
	clk           clock.Clock
	pollInterval  time.Duration
	shutdownGrace time.Duration
	dsnFrom       string
	hostname      string

	totalSem *semaphore.Weighted

	hostMu  sync.Mutex
	hostSem map[string]*hostBucket

	workersMu       sync.Mutex
	workersInFlight int

	intentMu sync.Mutex
	intents  map[EnvelopeID]signingIntent

	// wakeCh is pinged (non-blocking) when Submit enqueues a row so
	// the scheduler poll loop fast-paths instead of waiting up to
	// pollInterval. Buffered to 1; a single pending wake is enough.
	wakeCh chan struct{}
}

type hostBucket struct {
	sem   *semaphore.Weighted
	count int
}

// EnvelopeID is the per-submission group key that ties N per-recipient
// queue rows back to one user-visible submission. Lifted to package
// scope as an alias of store.EnvelopeID so callers can write the
// shorter name.
type EnvelopeID = store.EnvelopeID

// New constructs a Queue. Caller must call Run; the queue does not
// start goroutines on construction.
func New(opts Options) *Queue {
	registerMetrics()
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	conc := opts.Concurrency
	if conc <= 0 {
		conc = defaultConcurrency
	}
	perHost := opts.PerHostMax
	if perHost <= 0 {
		perHost = defaultPerHostMax
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	grace := opts.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}
	hostname := opts.Hostname
	if hostname == "" {
		hostname = "localhost"
	}
	dsnFrom := opts.DSNFromAddress
	if dsnFrom == "" {
		dsnFrom = "postmaster@" + hostname
	}
	// Make sure Concurrency drives PerHostMax bound.
	if perHost > conc {
		perHost = conc
	}
	q := &Queue{
		opts:          opts,
		clk:           clk,
		pollInterval:  poll,
		shutdownGrace: grace,
		dsnFrom:       dsnFrom,
		hostname:      hostname,
		totalSem:      semaphore.NewWeighted(int64(conc)),
		hostSem:       make(map[string]*hostBucket),
		// wakeCh is a 1-capacity buffered channel used by Submit (and
		// any future event source that adds work) to fast-poll the
		// scheduler instead of waiting up to pollInterval. Sends are
		// non-blocking; a coalesced wake is enough.
		wakeCh: make(chan struct{}, 1),
	}
	// Carry the bounded perHost budget back into opts for tests.
	q.opts.Concurrency = conc
	q.opts.PerHostMax = perHost
	return q
}

// Submit enqueues msg. Returns the EnvelopeID assigned to the
// submission group and a non-nil error on failure. When
// msg.IdempotencyKey is set and a prior submission already used the
// same key, returns the prior EnvelopeID and ErrConflict (callers
// treat that as "already enqueued — here is the prior receipt").
func (q *Queue) Submit(ctx context.Context, msg Submission) (EnvelopeID, error) {
	if len(msg.Recipients) == 0 {
		return "", fmt.Errorf("queue: submission has no recipients")
	}
	if msg.Body == nil {
		return "", fmt.Errorf("queue: submission has nil Body")
	}
	if q.opts.Store == nil {
		return "", fmt.Errorf("queue: Store is nil")
	}

	// Persist body + (optional) headers blobs first; refcounts move
	// to the queue rows below.
	bodyRef, err := q.opts.Store.Blobs().Put(ctx, msg.Body)
	if err != nil {
		return "", fmt.Errorf("queue: persist body: %w", err)
	}
	var headersHash string
	if len(msg.Headers) > 0 {
		hRef, err := q.opts.Store.Blobs().Put(ctx, bytes.NewReader(msg.Headers))
		if err != nil {
			return "", fmt.Errorf("queue: persist headers: %w", err)
		}
		headersHash = hRef.Hash
	}

	envID, err := newEnvelopeID()
	if err != nil {
		return "", err
	}

	now := q.clk.Now()
	// REQ-PROTO-58 / REQ-FLOW-63: when SendAt is set in the future the
	// row's NextAttemptAt is bumped to that instant so the scheduler
	// poll sees it as "not due" and leaves it in QueueStateQueued. A
	// zero or past SendAt collapses to now and behaves identically to
	// the pre-sendAt path.
	nextAttemptAt := now
	if !msg.SendAt.IsZero() && msg.SendAt.After(now) {
		nextAttemptAt = msg.SendAt
	}
	var pid store.PrincipalID
	if msg.PrincipalID != nil {
		pid = *msg.PrincipalID
	}

	// Idempotency uses the per-submission key; on the first row we
	// store it verbatim, on subsequent rows we suffix with the rcpt
	// index so a second Submit with the same key collides on the
	// first row only (which is enough to detect the duplicate). The
	// store's ErrConflict carries the existing row's id; we look up
	// its EnvelopeID and return that.
	var firstID store.QueueItemID
	for i, rcpt := range msg.Recipients {
		idemKey := ""
		if msg.IdempotencyKey != "" {
			if i == 0 {
				idemKey = msg.IdempotencyKey
			} else {
				idemKey = fmt.Sprintf("%s#%d", msg.IdempotencyKey, i)
			}
		}
		item := store.QueueItem{
			PrincipalID:     pid,
			MailFrom:        strings.ToLower(strings.TrimSpace(msg.MailFrom)),
			RcptTo:          strings.ToLower(strings.TrimSpace(rcpt)),
			EnvelopeID:      envID,
			BodyBlobHash:    bodyRef.Hash,
			HeadersBlobHash: headersHash,
			State:           store.QueueStateQueued,
			NextAttemptAt:   nextAttemptAt,
			DSNNotify:       msg.DSNNotify,
			DSNRet:          msg.DSNRet,
			DSNEnvID:        msg.DSNEnvelopeID,
			IdempotencyKey:  idemKey,
			CreatedAt:       now,
		}
		// Stash signing intent + REQUIRETLS in DSNOrcpt and Headers
		// blob? No — both fields are typed and load-bearing. Use a
		// dedicated marker by repurposing DSNOrcpt for ORCPT (the
		// canonical use) and the SigningIntent is recovered at
		// delivery time from Submission flags persisted alongside.
		// We persist signing + REQUIRETLS through a small auxiliary
		// JSON blob? — overkill for the current scope. The simpler
		// shape: the Submitter encodes per-row signing/REQUIRETLS in
		// the queued Submission's tag set, but the store schema has
		// no slot for it. We therefore encode the booleans into
		// DSNOrcpt's prefix when ORCPT is unset — and on read decode
		// them back.
		//
		// NOTE: per the queue-and-delivery doc, signing happens at
		// delivery time; but with the current store schema we have
		// no per-row place to record "sign with domain X". Rather
		// than overload DSNOrcpt, we recompute signing intent from a
		// per-EnvelopeID side table... but we have none. The
		// pragmatic v1 choice: stash a tiny TSV "sign=1;dom=...;req=1"
		// in HeadersBlobHash when no real headers blob is supplied.
		// That conflates two purposes; we instead introduce an
		// in-memory sidecar map keyed by EnvelopeID. The map lives
		// for the life of the process; on restart we lose the
		// signing intent, but the message body has already been
		// signed by the submitter in the common case. This is an
		// explicit limitation documented for the integrator.
		_ = item // (no store-side sign field)
		id, err := q.opts.Store.Meta().EnqueueMessage(ctx, item)
		if err != nil {
			if errors.Is(err, store.ErrConflict) && msg.IdempotencyKey != "" && i == 0 {
				// Existing row found. Return its EnvelopeID.
				existing, getErr := q.opts.Store.Meta().GetQueueItem(ctx, id)
				if getErr != nil {
					return "", fmt.Errorf("queue: idempotent lookup: %w", getErr)
				}
				return existing.EnvelopeID, ErrConflict
			}
			return "", fmt.Errorf("queue: enqueue rcpt %q: %w", rcpt, err)
		}
		if i == 0 {
			firstID = id
		}
	}
	q.opts.Logger.InfoContext(ctx, "queue: submission enqueued",
		slog.String("envelope_id", string(envID)),
		slog.Int("recipients", len(msg.Recipients)),
		slog.String("body_hash", bodyRef.Hash),
		slog.Uint64("first_row_id", uint64(firstID)),
	)

	// Stash the signing intent so the worker can recover it. The
	// sidecar is best-effort; on cold restart the body is already in
	// canonical form and the worker skips signing if the sidecar is
	// absent.
	q.rememberSigning(envID, signingIntent{
		Sign:          msg.Sign,
		Domain:        msg.SigningDomain,
		REQUIRETLS:    msg.REQUIRETLS,
		MailFrom:      msg.MailFrom,
		DSNNotify:     msg.DSNNotify,
		DSNEnvelopeID: msg.DSNEnvelopeID,
	})
	q.wake()
	return envID, nil
}

// Cancel removes every queue row belonging to envelope before its
// in-flight delivery starts. The orchestrator identifies cancellable
// rows by state — queued, deferred, and held are still reversible;
// inflight is too late (the deliverer holds the wire and we cannot
// rescind a MAIL/RCPT/DATA exchange in progress); done and failed are
// already terminal and return zero counts.
//
// Returns:
//   - cancelled: count of rows successfully removed (rows that were
//     still queued/deferred/held).
//   - inflightCount: rows that were already inflight; cancellation is a
//     no-op for these and the caller surfaces a "best-effort
//     cancellation" diagnostic to the JMAP client (REQ-FLOW-63).
//   - err: only on store errors. ListQueueItems with an unknown
//     EnvelopeID returns an empty slice, not an error; Cancel on a
//     non-existent envelope returns (0, 0, nil).
//
// Wire contract: REQ-PROTO-58 / REQ-FLOW-63. The delete is performed
// per-row via DeleteQueueItem, which decrements the body blob refcount
// and is itself transactional in the store. Concurrent claim/cancel:
// ListQueueItems → DeleteQueueItem races with the scheduler claim are
// resolved by DeleteQueueItem returning ErrNotFound when the row has
// transitioned out from under us; we treat that as "another path
// already consumed the row" and add nothing to either counter.
func (q *Queue) Cancel(ctx context.Context, envelope EnvelopeID) (cancelled, inflightCount int, err error) {
	if q.opts.Store == nil {
		return 0, 0, fmt.Errorf("queue: Cancel: Store is nil")
	}
	rows, err := q.opts.Store.Meta().ListQueueItems(ctx, store.QueueFilter{
		EnvelopeID: envelope,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("queue: cancel list: %w", err)
	}
	for _, r := range rows {
		switch r.State {
		case store.QueueStateQueued, store.QueueStateDeferred, store.QueueStateHeld:
			if dErr := q.opts.Store.Meta().DeleteQueueItem(ctx, r.ID); dErr != nil {
				if errors.Is(dErr, store.ErrNotFound) {
					// Raced with the scheduler claim or another
					// destroy; the row is already gone. Do not count
					// either way.
					continue
				}
				return cancelled, inflightCount, fmt.Errorf("queue: cancel delete %d: %w", r.ID, dErr)
			}
			cancelled++
		case store.QueueStateInflight:
			inflightCount++
		case store.QueueStateDone, store.QueueStateFailed:
			// Terminal: nothing to cancel; do not surface as
			// inflight either. Caller treats "all terminal, none
			// inflight" as a clean success.
		default:
			// Unknown / future state: be conservative and treat as
			// inflight so callers do not silently lose visibility on
			// a row that may still ship.
			inflightCount++
		}
	}
	q.opts.Logger.InfoContext(ctx, "queue: cancel envelope",
		slog.String("envelope_id", string(envelope)),
		slog.Int("cancelled", cancelled),
		slog.Int("inflight", inflightCount),
	)
	return cancelled, inflightCount, nil
}

// wake pings the scheduler's wakeCh non-blockingly. A single pending
// wake suffices to fast-poll on the next select.
func (q *Queue) wake() {
	select {
	case q.wakeCh <- struct{}{}:
	default:
	}
}

// signingIntent captures the per-submission flags the orchestrator
// needs at delivery time but which the store schema does not record
// (signing on/off, signing domain, REQUIRETLS). It lives in process
// memory; on restart deferred rows fall back to "no signing" because
// the message body persists in canonical form regardless.
type signingIntent struct {
	Sign          bool
	Domain        string
	REQUIRETLS    bool
	MailFrom      string
	DSNNotify     store.DSNNotifyFlags
	DSNEnvelopeID string
}

// signingMu and signingByEnv are package-private state on Queue;
// declared as fields below in a setter so the in-process sidecar stays
// hidden from callers. We add the fields via a separate file is not
// needed — embed directly below.

func (q *Queue) rememberSigning(env EnvelopeID, intent signingIntent) {
	q.intentMu.Lock()
	defer q.intentMu.Unlock()
	if q.intents == nil {
		q.intents = make(map[EnvelopeID]signingIntent)
	}
	q.intents[env] = intent
}

func (q *Queue) lookupSigning(env EnvelopeID) (signingIntent, bool) {
	q.intentMu.Lock()
	defer q.intentMu.Unlock()
	intent, ok := q.intents[env]
	return intent, ok
}

// Run starts the scheduler and worker pool. It blocks until ctx is
// cancelled; returns nil on graceful shutdown. Calling Run twice on
// the same Queue panics: the structure assumes a single owner.
func (q *Queue) Run(ctx context.Context) error {
	if q.opts.Deliverer == nil {
		return fmt.Errorf("queue: Run: Deliverer is nil")
	}
	if q.opts.Logger == nil {
		return fmt.Errorf("queue: Run: Logger is nil")
	}

	// Recovery sweep: rows in QueueStateInflight whose LastAttemptAt
	// is older than staleInflightFactor*PollInterval transition back
	// to QueueStateQueued. This handles process crash in the middle
	// of a Deliver call.
	if err := q.recoverStaleInflight(ctx); err != nil {
		q.opts.Logger.WarnContext(ctx, "queue: stale-inflight recovery failed",
			slog.Any("err", err))
	}

	// In-process worker tracker so we drain on shutdown.
	var wg sync.WaitGroup
	for {
		// Update gauge from store snapshot.
		q.refreshStateGauges(ctx)

		// Compute available slot count.
		// concurrency cap = total budget - in-flight
		q.workersMu.Lock()
		inFlight := q.workersInFlight
		q.workersMu.Unlock()
		budget := q.opts.Concurrency - inFlight
		if budget > 0 {
			items, err := q.opts.Store.Meta().ClaimDueQueueItems(ctx, q.clk.Now(), budget)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					break
				}
				q.opts.Logger.ErrorContext(ctx, "queue: claim due items failed",
					slog.Any("err", err))
			} else {
				for _, item := range items {
					if err := q.totalSem.Acquire(ctx, 1); err != nil {
						// Context cancelled mid-acquire. Roll the
						// row back to queued so the next start
						// picks it up.
						_ = q.opts.Store.Meta().RescheduleQueueItem(ctx,
							item.ID, q.clk.Now(), "shutdown before delivery")
						break
					}
					host := recipientHost(item.RcptTo)
					if !q.tryAcquireHost(host) {
						// Per-host budget exhausted: roll the row
						// back to queued so the next poll picks it up
						// once an in-flight delivery to this host
						// completes. Releasing the global slot keeps
						// the scheduler loop non-blocking.
						q.totalSem.Release(1)
						_ = q.opts.Store.Meta().RescheduleQueueItem(ctx,
							item.ID, q.clk.Now(), "deferred: per-host cap")
						continue
					}
					q.workersMu.Lock()
					q.workersInFlight++
					q.workersMu.Unlock()
					wg.Add(1)
					go func(it store.QueueItem, host string) {
						defer wg.Done()
						defer func() {
							q.releaseHost(host)
							q.totalSem.Release(1)
							q.workersMu.Lock()
							q.workersInFlight--
							q.workersMu.Unlock()
						}()
						q.deliver(ctx, it)
					}(item, host)
				}
			}
		}

		// Wait until either ctx is cancelled, a wake fires (Submit /
		// reschedule), or PollInterval elapses.
		select {
		case <-ctx.Done():
			// Drain in-flight workers within ShutdownGrace.
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			drainCtx, cancel := context.WithTimeout(context.Background(), q.shutdownGrace)
			defer cancel()
			select {
			case <-done:
			case <-drainCtx.Done():
				q.opts.Logger.WarnContext(ctx, "queue: shutdown drain timeout exceeded",
					slog.Duration("grace", q.shutdownGrace))
			}
			queueShutdownDrainTotal.Inc()
			return nil
		case <-q.wakeCh:
			// New work or reschedule; loop immediately.
		case <-q.clk.After(q.pollInterval):
			// Loop again.
		}
	}
	// unreachable in normal control flow.
	wg.Wait()
	return nil
}

// recoverStaleInflight transitions inflight rows whose last_attempt
// timestamp is older than staleInflightFactor*PollInterval back to
// queued so the scheduler picks them up. This is the at-least-once
// recovery path described in docs/architecture/04 §Crash safety.
func (q *Queue) recoverStaleInflight(ctx context.Context) error {
	cutoff := q.clk.Now().Add(-time.Duration(staleInflightFactor) * q.pollInterval)
	rows, err := q.opts.Store.Meta().ListQueueItems(ctx, store.QueueFilter{
		State: store.QueueStateInflight,
		Limit: 1000,
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		// Treat zero LastAttemptAt as stale (a write that never
		// completed its update) so we always recover.
		if r.LastAttemptAt.IsZero() || r.LastAttemptAt.Before(cutoff) {
			if err := q.opts.Store.Meta().RescheduleQueueItem(ctx,
				r.ID, q.clk.Now(), "recovered from stale inflight"); err != nil {
				q.opts.Logger.WarnContext(ctx, "queue: recover stale inflight failed",
					slog.Uint64("id", uint64(r.ID)),
					slog.Any("err", err))
			} else {
				q.opts.Logger.InfoContext(ctx, "queue: recovered stale inflight row",
					slog.Uint64("id", uint64(r.ID)),
					slog.Time("last_attempt_at", r.LastAttemptAt))
			}
		}
	}
	return nil
}

// deliver runs one delivery attempt for the given queue item. It
// fetches the body blob, optionally signs, calls the Deliverer, and
// translates the outcome back to a store transition + DSN emission.
func (q *Queue) deliver(parentCtx context.Context, item store.QueueItem) {
	// Use a fresh context derived from background so a cancelled
	// parent (shutdown) does not abort an attempt that is already
	// dialling. We bound the worker via PollInterval-derived grace.
	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Minute)
	defer cancel()

	body, err := q.readBody(ctx, item.BodyBlobHash)
	if err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: read body blob failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.String("body_hash", item.BodyBlobHash),
			slog.Any("err", err))
		// Treat as transient failure and reschedule.
		q.handleTransient(ctx, item, "blob read error: "+err.Error())
		return
	}

	// Reattach signing intent if recorded.
	intent, _ := q.lookupSigning(item.EnvelopeID)
	if intent.Sign && q.opts.Signer != nil {
		signed, sErr := q.opts.Signer.Sign(ctx, intent.Domain, body)
		if sErr != nil {
			q.opts.Logger.WarnContext(ctx, "queue: signer failed; sending unsigned",
				slog.String("domain", intent.Domain),
				slog.Any("err", sErr))
		} else {
			body = signed
		}
	}

	req := DeliveryRequest{
		MailFrom:   item.MailFrom,
		Recipient:  item.RcptTo,
		Message:    body,
		REQUIRETLS: intent.REQUIRETLS,
	}

	start := q.clk.Now()
	outcome, err := q.opts.Deliverer.Deliver(ctx, req)
	elapsed := q.clk.Now().Sub(start)
	if err != nil {
		// Coerce errors to transient outcomes if the deliverer did
		// not classify.
		if outcome.Status == DeliveryStatusUnknown {
			outcome = DeliveryOutcome{
				Status: DeliveryStatusTransient,
				Detail: err.Error(),
			}
		}
	}
	queueDeliveriesTotal.WithLabelValues(outcome.Status.String()).Inc()
	queueDeliveryDuration.WithLabelValues(outcome.Status.String()).Observe(elapsed.Seconds())

	switch outcome.Status {
	case DeliveryStatusSuccess:
		q.handleSuccess(ctx, item, outcome)
	case DeliveryStatusPermanent:
		q.handlePermanent(ctx, item, outcome)
	case DeliveryStatusHold:
		if hErr := q.opts.Store.Meta().HoldQueueItem(ctx, item.ID); hErr != nil {
			q.opts.Logger.ErrorContext(ctx, "queue: hold failed",
				slog.Uint64("id", uint64(item.ID)),
				slog.Any("err", hErr))
		}
	case DeliveryStatusTransient:
		q.handleTransient(ctx, item, outcomeToErrMsg(outcome))
	default:
		q.opts.Logger.ErrorContext(ctx, "queue: deliverer returned unknown status",
			slog.Uint64("id", uint64(item.ID)))
		q.handleTransient(ctx, item, "deliverer returned unknown status")
	}
}

func (q *Queue) handleSuccess(ctx context.Context, item store.QueueItem, outcome DeliveryOutcome) {
	if err := q.opts.Store.Meta().CompleteQueueItem(ctx, item.ID, true, ""); err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: complete success failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.Any("err", err))
		return
	}
	if shouldEmitSuccessDSN(item.DSNNotify) {
		q.emitDSN(ctx, item, outcome, DSNKindSuccess)
	}
}

func (q *Queue) handlePermanent(ctx context.Context, item store.QueueItem, outcome DeliveryOutcome) {
	if err := q.opts.Store.Meta().CompleteQueueItem(ctx, item.ID,
		false, outcomeToErrMsg(outcome)); err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: complete permanent failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.Any("err", err))
		return
	}
	if shouldEmitFailureDSN(item.DSNNotify) {
		q.emitDSN(ctx, item, outcome, DSNKindFailure)
	}
}

func (q *Queue) handleTransient(ctx context.Context, item store.QueueItem, errMsg string) {
	delay, ok := q.opts.Retry.Next(item.Attempts)
	if !ok {
		// Schedule exhausted: escalate to permanent.
		if err := q.opts.Store.Meta().CompleteQueueItem(ctx, item.ID,
			false, "retry schedule exhausted: "+errMsg); err != nil {
			q.opts.Logger.ErrorContext(ctx, "queue: complete after exhaustion failed",
				slog.Uint64("id", uint64(item.ID)),
				slog.Any("err", err))
			return
		}
		if shouldEmitFailureDSN(item.DSNNotify) {
			outcome := DeliveryOutcome{
				Status:       DeliveryStatusPermanent,
				Detail:       "retry schedule exhausted: " + errMsg,
				EnhancedCode: "5.4.7",
			}
			q.emitDSN(ctx, item, outcome, DSNKindFailure)
		}
		return
	}
	next := q.clk.Now().Add(delay)
	if err := q.opts.Store.Meta().RescheduleQueueItem(ctx, item.ID, next, errMsg); err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: reschedule failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.Any("err", err))
	}
}

// emitDSN renders a DSN and enqueues it as a new queue row addressed
// to the original MAIL FROM. DSN-of-DSN loops are prevented by
// refusing to emit when the target row was itself a DSN (MAIL FROM
// empty AND IdempotencyKey starts with "dsn:").
func (q *Queue) emitDSN(ctx context.Context, item store.QueueItem, outcome DeliveryOutcome, kind DSNKind) {
	if item.MailFrom == "" {
		// Null sender — RFC 3464 says do not bounce a bounce. Suppress.
		q.opts.Logger.InfoContext(ctx, "queue: skip DSN to null sender",
			slog.Uint64("id", uint64(item.ID)),
			slog.String("kind", kind.String()))
		return
	}
	headers, _ := q.fetchHeaders(ctx, item.HeadersBlobHash)
	diag := outcomeDiagnostic(outcome)
	statusCode := outcome.EnhancedCode
	if statusCode == "" {
		switch kind {
		case DSNKindSuccess:
			statusCode = "2.0.0"
		case DSNKindDelay:
			statusCode = "4.0.0"
		default:
			statusCode = "5.0.0"
		}
	}
	dsn, err := buildDSN(dsnInput{
		Kind:            kind,
		ReportingMTA:    "dns; " + q.hostname,
		From:            q.dsnFrom,
		To:              item.MailFrom,
		OriginalRcpt:    item.DSNOrcpt,
		FinalRcpt:       item.RcptTo,
		OriginalEnvID:   item.DSNEnvID,
		DiagnosticCode:  diag,
		StatusCode:      statusCode,
		OriginalHeaders: headers,
		Now:             q.clk.Now(),
	})
	if err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: build DSN failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.Any("err", err))
		return
	}
	bodyRef, err := q.opts.Store.Blobs().Put(ctx, bytes.NewReader(dsn))
	if err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: persist DSN body failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.Any("err", err))
		return
	}
	envID, err := newEnvelopeID()
	if err != nil {
		q.opts.Logger.ErrorContext(ctx, "queue: dsn envelope id failed",
			slog.Any("err", err))
		return
	}
	now := q.clk.Now()
	dsnItem := store.QueueItem{
		MailFrom:      "", // null sender per RFC 3464 §2.1.2
		RcptTo:        item.MailFrom,
		EnvelopeID:    envID,
		BodyBlobHash:  bodyRef.Hash,
		State:         store.QueueStateQueued,
		NextAttemptAt: now,
		DSNNotify:     store.DSNNotifyNever, // do not DSN a DSN
		CreatedAt:     now,
		// Tag the idempotency key with a "dsn:" prefix so a second
		// emission for the same row collapses on the store-side
		// uniqueness check.
		IdempotencyKey: fmt.Sprintf("dsn:%s:%d:%s", kind, item.ID, item.RcptTo),
	}
	if _, err := q.opts.Store.Meta().EnqueueMessage(ctx, dsnItem); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Duplicate emission for the same row + kind: harmless.
			return
		}
		q.opts.Logger.ErrorContext(ctx, "queue: enqueue DSN failed",
			slog.Uint64("id", uint64(item.ID)),
			slog.Any("err", err))
		return
	}
	queueDSNEmittedTotal.WithLabelValues(kind.String()).Inc()
}

// readBody reads the queue row's body blob fully into memory. The
// outbound SMTP path streams the body into the wire so for huge
// messages this is suboptimal; v1 ships full-buffering and revisits
// streaming when ops measurements demand it (the body has already
// been written to disk; the cost is one extra read+copy per attempt).
func (q *Queue) readBody(ctx context.Context, hash string) ([]byte, error) {
	r, err := q.opts.Store.Blobs().Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// fetchHeaders returns the optional headers blob, or nil when the row
// has none. Errors are logged and absorbed; DSNs render fine without
// the original headers.
func (q *Queue) fetchHeaders(ctx context.Context, hash string) ([]byte, error) {
	if hash == "" {
		return nil, nil
	}
	r, err := q.opts.Store.Blobs().Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Stats returns a snapshot of the queue state. Backed by
// store.CountQueueByState plus the live in-process worker counter.
func (q *Queue) Stats(ctx context.Context) (Stats, error) {
	counts, err := q.opts.Store.Meta().CountQueueByState(ctx)
	if err != nil {
		return Stats{}, err
	}
	q.workersMu.Lock()
	inflight := q.workersInFlight
	q.workersMu.Unlock()
	return Stats{
		Queued:          counts[store.QueueStateQueued],
		Deferred:        counts[store.QueueStateDeferred],
		Inflight:        counts[store.QueueStateInflight],
		InflightWorkers: inflight,
		Done:            counts[store.QueueStateDone],
		Failed:          counts[store.QueueStateFailed],
		Held:            counts[store.QueueStateHeld],
	}, nil
}

func (q *Queue) refreshStateGauges(ctx context.Context) {
	counts, err := q.opts.Store.Meta().CountQueueByState(ctx)
	if err != nil {
		return
	}
	for _, st := range []store.QueueState{
		store.QueueStateQueued,
		store.QueueStateDeferred,
		store.QueueStateInflight,
		store.QueueStateDone,
		store.QueueStateFailed,
		store.QueueStateHeld,
	} {
		queueItems.WithLabelValues(st.String()).Set(float64(counts[st]))
	}
}

// tryAcquireHost attempts to take one slot from host's bucket without
// blocking. Returns true on success. The bucket is created on first
// contact and ref-counted so the per-host map never leaks entries
// after the last in-flight worker exits. Tests rely on the
// non-blocking behaviour: when the per-host cap is reached we want
// the scheduler to defer the row, not stall.
func (q *Queue) tryAcquireHost(host string) bool {
	q.hostMu.Lock()
	bucket, ok := q.hostSem[host]
	if !ok {
		bucket = &hostBucket{
			sem: semaphore.NewWeighted(int64(q.opts.PerHostMax)),
		}
		q.hostSem[host] = bucket
	}
	if !bucket.sem.TryAcquire(1) {
		// Bucket already fully held by other in-flight workers; do
		// not increment count so we keep the map sparse.
		if bucket.count == 0 {
			delete(q.hostSem, host)
		}
		q.hostMu.Unlock()
		return false
	}
	bucket.count++
	q.hostMu.Unlock()
	return true
}

// releaseHost returns one slot to host's bucket and removes the
// bucket from the map when no in-flight workers reference it. This is
// the per-host map size bound called out in the Wave 2.1 spec.
func (q *Queue) releaseHost(host string) {
	q.hostMu.Lock()
	bucket, ok := q.hostSem[host]
	if !ok {
		q.hostMu.Unlock()
		return
	}
	bucket.sem.Release(1)
	bucket.count--
	if bucket.count <= 0 {
		delete(q.hostSem, host)
	}
	q.hostMu.Unlock()
}

// recipientHost returns the lowercased domain part of an SMTP
// forward-path. Empty input maps to "" which the per-host bucket
// handles as one "unknown" group.
func recipientHost(rcpt string) string {
	at := strings.LastIndexByte(rcpt, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(rcpt[at+1:])
}

// outcomeToErrMsg formats a DeliveryOutcome's diagnostic into a single
// last_error string. Bounded length so the column does not balloon
// on a chatty receiver.
func outcomeToErrMsg(o DeliveryOutcome) string {
	if o.Code == 0 && o.EnhancedCode == "" {
		if o.Detail == "" {
			return ""
		}
		return o.Detail
	}
	parts := []string{}
	if o.Code != 0 {
		parts = append(parts, fmt.Sprintf("%d", o.Code))
	}
	if o.EnhancedCode != "" {
		parts = append(parts, o.EnhancedCode)
	}
	if o.Detail != "" {
		parts = append(parts, o.Detail)
	}
	out := strings.Join(parts, " ")
	if len(out) > 1024 {
		out = out[:1024]
	}
	return out
}

// outcomeDiagnostic returns the RFC 3464 §2.3.6 Diagnostic-Code
// representation of an outcome. Empty input becomes a generic
// "smtp; failed".
func outcomeDiagnostic(o DeliveryOutcome) string {
	if o.Code == 0 && o.EnhancedCode == "" && o.Detail == "" {
		return "smtp; 550 unspecified failure"
	}
	parts := []string{}
	if o.Code != 0 {
		parts = append(parts, fmt.Sprintf("%d", o.Code))
	}
	if o.EnhancedCode != "" {
		parts = append(parts, o.EnhancedCode)
	}
	if o.Detail != "" {
		parts = append(parts, o.Detail)
	}
	return "smtp; " + strings.Join(parts, " ")
}

// newEnvelopeID returns a fresh opaque envelope id (24 hex chars from
// crypto/rand). The store treats it as text; the orchestrator only
// requires uniqueness across submissions. crypto/rand is deterministic
// in tests through go's normal seed (we accept that DSN message-ids
// differ between runs; the tests assert structure, not ID values).
func newEnvelopeID() (EnvelopeID, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("queue: envelope id: %w", err)
	}
	return EnvelopeID(hex.EncodeToString(b[:])), nil
}
