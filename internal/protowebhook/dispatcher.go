package protowebhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/cursors"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Default tunables. Exposed so callers and tests can reference one source
// of truth.
const (
	// DefaultCursorKey is the row name used in the cursors table to
	// persist the dispatcher's resume point. We share the cursors table
	// with the FTS worker (a different key) per the spec note in
	// store.Metadata.GetFTSCursor.
	DefaultCursorKey = "webhooks"

	// DefaultPollInterval is how long the loop sleeps when the change
	// feed returns no new entries.
	DefaultPollInterval = 5 * time.Second

	// DefaultMaxConcurrentDeliveries caps in-flight HTTP POSTs.
	DefaultMaxConcurrentDeliveries = 32

	// DefaultInlineBodyMaxSize is the inline-vs-fetch-URL threshold.
	DefaultInlineBodyMaxSize int64 = 64 * 1024

	// DefaultBatchSize is how many change-feed entries the loop reads
	// per tick.
	DefaultBatchSize = 256

	// DefaultHTTPTimeout is the per-attempt HTTP timeout.
	DefaultHTTPTimeout = 30 * time.Second

	// DefaultFetchURLTTL is the validity window for a signed body URL.
	DefaultFetchURLTTL = time.Hour

	// EventMailArrived is the value of the Herold-Event header on a
	// mail-arrival POST and the "event" field of the JSON payload.
	EventMailArrived = "mail.arrived"
)

// DefaultRetrySchedule is the exponential backoff we apply when a webhook
// delivery returns 5xx / 429 / network error. After the last entry the
// delivery is marked permanently failed.
//
// The schedule sums to ~17h, well inside the spec's "drop after a day"
// guidance (REQ-HOOK-12). Per-webhook RetryPolicy can override.
var DefaultRetrySchedule = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	1 * time.Hour,
	4 * time.Hour,
	12 * time.Hour,
}

// Options configures the Dispatcher. Zero values fall back to the
// Default* constants above.
type Options struct {
	// Store is the metadata + blob surface the dispatcher reads.
	Store store.Store
	// Logger is the structured logger; nil falls back to slog.Default().
	Logger *slog.Logger
	// Clock injects time; nil falls back to clock.NewReal().
	Clock clock.Clock
	// HTTPClient executes each delivery POST. Callers should configure
	// timeout and connection pooling; nil yields a Client with
	// DefaultHTTPTimeout.
	HTTPClient *http.Client
	// MaxConcurrentDeliveries caps in-flight HTTP POSTs. <= 0 uses
	// DefaultMaxConcurrentDeliveries.
	MaxConcurrentDeliveries int
	// PollInterval is how long the change-feed loop sleeps when a tick
	// returned no new changes. <= 0 uses DefaultPollInterval.
	PollInterval time.Duration
	// BatchSize is how many change-feed entries to read per tick.
	// <= 0 uses DefaultBatchSize.
	BatchSize int
	// InlineBodyMaxSize is the inline-vs-fetch-URL threshold in bytes.
	// <= 0 uses DefaultInlineBodyMaxSize.
	InlineBodyMaxSize int64
	// FetchURLBaseURL is prepended to fetch URLs (e.g. "https://mail.example.com").
	// Trailing slash is trimmed.
	FetchURLBaseURL string
	// FetchURLTTL is the validity window for a signed fetch URL.
	// <= 0 uses DefaultFetchURLTTL.
	FetchURLTTL time.Duration
	// SigningKey is the server-wide HMAC key for signing fetch URLs.
	// MUST be set when FetchURLBaseURL is non-empty.
	SigningKey []byte
	// CursorKey overrides the cursors-table row name; default
	// DefaultCursorKey. Tests use this to isolate concurrent runs.
	CursorKey string
	// RetrySchedule overrides DefaultRetrySchedule.
	RetrySchedule []time.Duration
}

// Dispatcher reads the global change feed, looks up matching webhooks per
// message arrival, and POSTs each delivery as a bounded async job.
//
// Architecture: change-feed-driven. We follow the same pattern the FTS
// worker uses (see internal/storefts/worker.go) — read FTS().ReadChangeFeedForFTS
// with a durable cursor in the cursors table keyed "webhooks" (path b in
// the spec). The same global feed serves both subsystems; each cursor is
// independent.
type Dispatcher struct {
	store         store.Store
	logger        *slog.Logger
	clock         clock.Clock
	httpClient    *http.Client
	sem           *semaphore.Weighted
	pollInterval  time.Duration
	batchSize     int
	inlineMaxSize int64
	fetchBase     string
	fetchTTL      time.Duration
	signingKey    []byte
	cursorKey     string
	retrySchedule []time.Duration

	cursor  atomic.Uint64
	running atomic.Bool

	wg sync.WaitGroup
}

// New constructs a Dispatcher. Pass Run on a goroutine owned by the
// server lifecycle; cancel its ctx to drain.
func New(opts Options) *Dispatcher {
	d := &Dispatcher{
		store:         opts.Store,
		logger:        opts.Logger,
		clock:         opts.Clock,
		httpClient:    opts.HTTPClient,
		pollInterval:  opts.PollInterval,
		batchSize:     opts.BatchSize,
		inlineMaxSize: opts.InlineBodyMaxSize,
		fetchBase:     strings.TrimRight(opts.FetchURLBaseURL, "/"),
		fetchTTL:      opts.FetchURLTTL,
		signingKey:    append([]byte(nil), opts.SigningKey...),
		cursorKey:     opts.CursorKey,
		retrySchedule: opts.RetrySchedule,
	}
	if d.logger == nil {
		d.logger = slog.Default()
	}
	if d.clock == nil {
		d.clock = clock.NewReal()
	}
	if d.httpClient == nil {
		d.httpClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	if d.pollInterval <= 0 {
		d.pollInterval = DefaultPollInterval
	}
	if d.batchSize <= 0 {
		d.batchSize = DefaultBatchSize
	}
	if d.inlineMaxSize <= 0 {
		d.inlineMaxSize = DefaultInlineBodyMaxSize
	}
	if d.fetchTTL <= 0 {
		d.fetchTTL = DefaultFetchURLTTL
	}
	if d.cursorKey == "" {
		d.cursorKey = DefaultCursorKey
	}
	if len(d.retrySchedule) == 0 {
		d.retrySchedule = append([]time.Duration(nil), DefaultRetrySchedule...)
	}
	max := opts.MaxConcurrentDeliveries
	if max <= 0 {
		max = DefaultMaxConcurrentDeliveries
	}
	d.sem = semaphore.NewWeighted(int64(max))
	return d
}

// Cursor returns the dispatcher's last persisted change-feed Seq. Useful
// for tests and operator diag.
func (d *Dispatcher) Cursor() uint64 { return d.cursor.Load() }

// Run consumes the change feed until ctx is cancelled. Returns nil on
// graceful shutdown; non-nil on a fatal store error. Safe to call once
// per Dispatcher; a second concurrent call returns an error.
//
// Cursor persistence guarantee: Run flushes the in-memory cursor to
// disk before returning, even when ctx is already cancelled, so that a
// caller that does cancel(); <-done can be certain the cursor is durable
// by the time the receive on done completes.
func (d *Dispatcher) Run(ctx context.Context) error {
	if !d.running.CompareAndSwap(false, true) {
		return errors.New("protowebhook: dispatcher already running")
	}
	defer d.running.Store(false)
	// On shutdown: first drain in-flight deliveries, then flush the cursor.
	// Defers run LIFO so we register the cursor flush before wg.Wait:
	// the flush runs last (first registered = last executed in LIFO) and
	// sees the final in-memory cursor that the draining deliveries wrote.
	//
	// ShutdownFlusher uses a fresh context so a cancellation of ctx cannot
	// prevent the write.
	defer cursors.ShutdownFlusher{
		Get:       d.cursor.Load,
		Put:       func(ctx context.Context, seq uint64) error { return d.store.Meta().SetFTSCursor(ctx, d.cursorKey, seq) },
		Logger:    d.logger,
		Subsystem: "protowebhook",
	}.Flush()
	// Wait for in-flight deliveries so the cursor flush above sees the
	// final in-memory cursor position. Registered after the flush defer so
	// it executes before the flush (LIFO order: last registered runs first).
	defer d.wg.Wait()

	// Hydrate the resume cursor.
	if seq, err := d.store.Meta().GetFTSCursor(ctx, d.cursorKey); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("protowebhook: load cursor: %w", err)
	} else {
		d.cursor.Store(seq)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		changes, err := d.store.FTS().ReadChangeFeedForFTS(ctx, d.cursor.Load(), d.batchSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("protowebhook: read change feed: %w", err)
		}
		if len(changes) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-d.clock.After(d.pollInterval):
			}
			continue
		}
		var maxSeq uint64
		for _, c := range changes {
			if err := ctx.Err(); err != nil {
				return nil
			}
			if c.Seq > maxSeq {
				maxSeq = c.Seq
			}
			if c.Kind != store.EntityKindEmail || c.Op != store.ChangeOpCreated {
				continue
			}
			d.processChange(ctx, c)
		}
		if maxSeq > 0 {
			d.cursor.Store(maxSeq)
			if err := d.store.Meta().SetFTSCursor(ctx, d.cursorKey, maxSeq); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					// The deferred shutdown flush will persist d.cursor.
					return nil
				}
				d.logger.Warn("protowebhook: persist cursor",
					"activity", observe.ActivityInternal,
					"key", d.cursorKey,
					"seq", maxSeq,
					"err", err.Error())
			}
		}
	}
}

// processChange resolves a single mail-arrival event and enqueues one
// delivery job per matching webhook. Lookup errors are logged and
// swallowed; the cursor still advances so a transient resolution
// failure does not block the whole feed.
func (d *Dispatcher) processChange(ctx context.Context, c store.FTSChange) {
	msgID := store.MessageID(c.EntityID)
	mailboxID := store.MailboxID(c.ParentEntityID)
	msg, err := d.store.Meta().GetMessage(ctx, msgID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.logger.Warn("protowebhook: get message",
				"activity", observe.ActivityInternal,
				"message_id", uint64(msgID),
				"err", err.Error())
		}
		return
	}
	mb, err := d.store.Meta().GetMailboxByID(ctx, mailboxID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.logger.Warn("protowebhook: get mailbox",
				"activity", observe.ActivityInternal,
				"mailbox_id", uint64(mailboxID),
				"err", err.Error())
		}
		return
	}
	principal, err := d.store.Meta().GetPrincipalByID(ctx, mb.PrincipalID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.logger.Warn("protowebhook: get principal",
				"activity", observe.ActivityInternal,
				"principal_id", uint64(mb.PrincipalID),
				"err", err.Error())
		}
		return
	}
	domain := principalDomain(principal)
	hooks := d.matchingWebhooks(ctx, principal, domain)
	if len(hooks) == 0 {
		return
	}
	for _, h := range hooks {
		hook := h
		// Acquire the semaphore to bound concurrency. Acquire honours
		// ctx so shutdown drains cleanly.
		if err := d.sem.Acquire(ctx, 1); err != nil {
			return
		}
		d.wg.Add(1)
		go func() {
			defer d.sem.Release(1)
			defer d.wg.Done()
			d.deliver(ctx, hook, principal, mb, msg)
		}()
	}
}

// matchingWebhooks returns the union of:
//   - active webhooks owned by the principal's domain (legacy
//     owner_kind=domain),
//   - active synthetic-target webhooks for that domain
//     (target_kind=synthetic, REQ-HOOK-02), surfaced by the same
//     ListActiveWebhooksForDomain call in the storesqlite/storepg
//     adapters,
//   - active webhooks owned by the principal directly.
//
// Synthetic recipients accepted with no principal_id (REQ-DIR-RCPT-07
// fall-through case) flow through DispatchSynthetic — they do not enter
// the change feed because they lack a mailbox row.  This function only
// fires for principal-bound deliveries (the change-feed path).
//
// Duplicates (a hook listed under both ownership paths, which the schema
// permits but conventional usage avoids) are coalesced by ID.
func (d *Dispatcher) matchingWebhooks(ctx context.Context, p store.Principal, domain string) []store.Webhook {
	seen := make(map[store.WebhookID]struct{}, 4)
	var out []store.Webhook
	if domain != "" {
		hooks, err := d.store.Meta().ListActiveWebhooksForDomain(ctx, domain)
		if err != nil {
			d.logger.Warn("protowebhook: list domain webhooks",
				"activity", observe.ActivityInternal,
				"domain", domain,
				"err", err.Error())
		}
		for _, h := range hooks {
			if _, dup := seen[h.ID]; dup {
				continue
			}
			seen[h.ID] = struct{}{}
			out = append(out, h)
		}
	}
	pidStr := strconv.FormatUint(uint64(p.ID), 10)
	hooks, err := d.store.Meta().ListWebhooks(ctx, store.WebhookOwnerPrincipal, pidStr)
	if err != nil {
		d.logger.Warn("protowebhook: list principal webhooks",
			"activity", observe.ActivityInternal,
			"principal_id", uint64(p.ID),
			"err", err.Error())
	}
	for _, h := range hooks {
		if !h.Active {
			continue
		}
		if _, dup := seen[h.ID]; dup {
			continue
		}
		seen[h.ID] = struct{}{}
		out = append(out, h)
	}
	return out
}

// principalDomain extracts the domain part of a principal's canonical
// email, lowercased. Returns "" when the email lacks an "@".
func principalDomain(p store.Principal) string {
	idx := strings.IndexByte(p.CanonicalEmail, '@')
	if idx < 0 {
		return ""
	}
	return strings.ToLower(p.CanonicalEmail[idx+1:])
}

// SyntheticDispatch carries the per-message inputs for a synthetic-
// recipient webhook delivery (REQ-DIR-RCPT-07, REQ-HOOK-02). The
// dispatcher uses BlobHash to render fetch URLs and reads the parsed
// MIME tree out of Parsed when extracted-mode is in play.
type SyntheticDispatch struct {
	// Domain is the recipient's domain (lowercased). Used to filter
	// matching subscriptions (target_kind=synthetic + value=Domain plus
	// owner_kind=domain + owner_id=Domain).
	Domain string
	// Recipient is the synthetic RCPT TO address (full local@domain).
	// Echoed into the payload's envelope.to.
	Recipient string
	// MailFrom is the SMTP MAIL FROM (reverse-path).
	MailFrom string
	// RouteTag is the directory.resolve_rcpt plugin's correlation token
	// (REQ-DIR-RCPT-07). Echoed into payload.route_tag.
	RouteTag string
	// BlobHash is the canonical content hash of the persisted message
	// body. Used to mint signed fetch URLs (REQ-HOOK-30/31).
	BlobHash string
	// Size is the persisted body byte count.
	Size int64
	// Parsed is the mailparse.Message produced from the stored bytes.
	// Reused so the dispatcher does not re-parse for every subscription.
	Parsed mailparse.Message
}

// MatchingSyntheticHooks returns the active webhook subscriptions that
// match a synthetic recipient on the supplied domain (REQ-HOOK-02).
// A subscription matches when EITHER:
//   - target_kind == synthetic AND target.value == domain, or
//   - the legacy owner_kind == domain AND owner_id == domain (so an
//     operator who has not migrated to target.kind still receives
//     synthetic-recipient deliveries).
//
// Inactive rows are filtered out; duplicates are coalesced by ID.
func (d *Dispatcher) MatchingSyntheticHooks(ctx context.Context, domain string) []store.Webhook {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil
	}
	hooks, err := d.store.Meta().ListActiveWebhooksForDomain(ctx, domain)
	if err != nil {
		d.logger.Warn("protowebhook: list synthetic webhooks",
			"activity", observe.ActivityInternal,
			"domain", domain,
			"err", err.Error())
		return nil
	}
	out := make([]store.Webhook, 0, len(hooks))
	seen := make(map[store.WebhookID]struct{}, len(hooks))
	for _, h := range hooks {
		if !h.Active {
			continue
		}
		if _, dup := seen[h.ID]; dup {
			continue
		}
		switch h.EffectiveTargetKind() {
		case store.WebhookTargetSynthetic, store.WebhookTargetDomain:
			seen[h.ID] = struct{}{}
			out = append(out, h)
		}
	}
	return out
}

// DispatchSynthetic enqueues one webhook delivery per matching
// subscription for a synthetic recipient (REQ-DIR-RCPT-07, REQ-HOOK-02).
// Unlike the change-feed-driven path, no messages-row lookup is required:
// the caller passes the parsed message + envelope + blob hash directly.
//
// Deliveries run as bounded goroutines under the dispatcher's parent
// ctx (the same shape as the change-feed path) so SIGTERM drains them.
// Errors that prevent any delivery from starting are returned; per-
// delivery failures are logged inside the goroutine and surfaced via
// the existing dispatcher metrics.
//
// The supplied subscriptions slice is the operator's pre-filtered hook
// list (typically MatchingSyntheticHooks output); passing an empty
// slice is a no-op.
func (d *Dispatcher) DispatchSynthetic(ctx context.Context, in SyntheticDispatch, hooks []store.Webhook) error {
	if len(hooks) == 0 {
		return nil
	}
	if in.BlobHash == "" {
		return errors.New("protowebhook: DispatchSynthetic: empty BlobHash")
	}
	for _, h := range hooks {
		hook := h
		if err := d.sem.Acquire(ctx, 1); err != nil {
			return fmt.Errorf("protowebhook: acquire semaphore: %w", err)
		}
		d.wg.Add(1)
		go func() {
			defer d.sem.Release(1)
			defer d.wg.Done()
			d.deliverSynthetic(ctx, hook, in)
		}()
	}
	return nil
}

// deliverSynthetic runs the per-job lifecycle for one synthetic
// dispatch: build payload, dispatch with retries.
func (d *Dispatcher) deliverSynthetic(ctx context.Context, hook store.Webhook, in SyntheticDispatch) {
	deliveryID, err := newDeliveryID()
	if err != nil {
		d.logger.Warn("protowebhook: synthetic generate delivery id",
			"activity", observe.ActivityInternal, "err", err.Error())
		return
	}
	payload, dropped, err := d.buildSyntheticPayload(ctx, hook, deliveryID, in)
	if err != nil {
		d.logger.Warn("protowebhook: build synthetic payload",
			"activity", observe.ActivityInternal,
			"webhook_id", uint64(hook.ID),
			"recipient", in.Recipient,
			"err", err.Error())
		return
	}
	if dropped {
		// REQ-HOOK-EXTRACTED-03: text_required + origin=none drops the
		// delivery without retry.
		d.recordOutcome(hook, "dropped_no_text")
		d.recordDropAudit(ctx, hook, deliveryID, 0, in.Parsed.Envelope.MessageID)
		d.logger.Info("protowebhook: synthetic dropped no_text",
			"activity", observe.ActivitySystem,
			"webhook_id", uint64(hook.ID),
			"delivery_id", deliveryID,
			"recipient", in.Recipient)
		return
	}
	d.dispatchPayload(ctx, hook, deliveryID, payload, 0)
}
