package webpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/vapid"
)

// Default tunables used when Options leaves a field zero.
const (
	DefaultPollInterval     = 5 * time.Second
	DefaultHTTPTimeout      = 30 * time.Second
	DefaultJWTExpiry        = 12 * time.Hour
	DefaultTTLSeconds       = 24 * 60 * 60 // 1 day; gateways typically permit up to 28 days.
	DefaultBatchSize        = 256
	DefaultMaxAttempts4xx   = 3
	DefaultMaxAttempts5xx   = 5
	defaultEventChannelSize = 1024
)

// Options configures Dispatcher. The Required block (Store, VAPID,
// Clock, Logger) must be supplied; the rest fall back to the defaults
// above.
type Options struct {
	Store    store.Store
	VAPID    *vapid.Manager
	Clock    clock.Clock
	Logger   *slog.Logger
	HTTPDoer HTTPDoer

	// Hostname is used to build the default subject claim
	// ("mailto:postmaster@<hostname>") when sysconfig leaves
	// Subject empty.
	Hostname string
	Subject  string

	// PollInterval governs how often the change-feed reader wakes
	// when no work is available. Defaults to DefaultPollInterval.
	PollInterval time.Duration

	// HTTPTimeout caps a single outbound POST. Defaults to
	// DefaultHTTPTimeout.
	HTTPTimeout time.Duration

	// JWTExpiry caps the VAPID JWT exp - iat. Hard-capped at 24h
	// (vapid.MaxJWTExpiry); a higher value is silently lowered.
	JWTExpiry time.Duration

	// RateLimitPerMinute / RateLimitPerDay / CooldownDuration tune
	// the per-subscription rate limiter. Zero falls back to
	// REQ-PROTO-126 defaults (60 / 1000 / 5 min).
	RateLimitPerMinute int
	RateLimitPerDay    int
	CooldownDuration   time.Duration

	// MaxAttempts4xx / MaxAttempts5xx cap retry budgets per
	// subscription. Zero falls back to defaults.
	MaxAttempts4xx int
	MaxAttempts5xx int

	// CursorKey is the durable change-feed cursor slot. Defaults to
	// "webpush". Tests can override to keep parallel runs isolated.
	CursorKey string
}

// HTTPDoer is the minimal Do interface the dispatcher consumes. The
// stdlib *http.Client implements it; tests inject a fake.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Dispatcher is the change-feed-driven outbound Web Push loop.
// Construct via New; call Run(ctx) inside a server-lifecycle
// errgroup. SendVerificationPing is invoked synchronously from JMAP
// PushSubscription/set { create } handlers (wrapped in a goroutine
// inside the JMAP path so JMAP responses do not block on the gateway).
type Dispatcher struct {
	store    store.Store
	vapid    *vapid.Manager
	doer     HTTPDoer
	clock    clock.Clock
	logger   *slog.Logger
	subject  string
	hostname string

	pollInterval   time.Duration
	httpTimeout    time.Duration
	jwtExpiry      time.Duration
	maxAttempts4xx int
	maxAttempts5xx int
	cursorKey      string

	rl *rateLimiter

	cursor atomic.Uint64

	// retryMu guards the in-flight retry map. attemptState tracks the
	// per-subscription attempt count + next-attempt instant so the
	// dispatcher can honour exponential backoff without persistent
	// state. On retry budget exhaustion the entry is deleted; on
	// successful delivery likewise.
	retryMu sync.Mutex
	attempt map[store.PushSubscriptionID]*subAttempt

	// subsActive holds the most recently observed total subscription
	// count, exported via the herold_webpush_subscriptions_active
	// gauge.
	subsActive atomic.Int64

	running atomic.Bool

	// kickCh nudges the Run loop awake — used by SendVerificationPing
	// so a freshly created subscription gets its ping in the next
	// tick rather than after a full PollInterval.
	kickCh chan struct{}
}

// subAttempt is the per-subscription in-memory retry tracker.
type subAttempt struct {
	count        int
	nextAttempt  time.Time
	last5xxClass bool
}

// New constructs a Dispatcher with the supplied options. The
// returned Dispatcher is inert until Run is called.
func New(opts Options) (*Dispatcher, error) {
	if opts.Store == nil {
		return nil, errors.New("webpush: nil Store")
	}
	if opts.VAPID == nil {
		return nil, errors.New("webpush: nil VAPID manager")
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPDoer == nil {
		opts.HTTPDoer = &http.Client{Timeout: orDefault(opts.HTTPTimeout, DefaultHTTPTimeout)}
	}
	subj := opts.Subject
	if subj == "" {
		host := opts.Hostname
		if host == "" {
			host = "localhost"
		}
		subj = "mailto:postmaster@" + host
	}
	jwtExp := orDefault(opts.JWTExpiry, DefaultJWTExpiry)
	if jwtExp > vapid.MaxJWTExpiry {
		jwtExp = vapid.MaxJWTExpiry
	}
	max4xx := opts.MaxAttempts4xx
	if max4xx <= 0 {
		max4xx = DefaultMaxAttempts4xx
	}
	max5xx := opts.MaxAttempts5xx
	if max5xx <= 0 {
		max5xx = DefaultMaxAttempts5xx
	}
	cursorKey := opts.CursorKey
	if cursorKey == "" {
		cursorKey = "webpush"
	}
	d := &Dispatcher{
		store:          opts.Store,
		vapid:          opts.VAPID,
		doer:           opts.HTTPDoer,
		clock:          opts.Clock,
		logger:         opts.Logger,
		subject:        subj,
		hostname:       opts.Hostname,
		pollInterval:   orDefault(opts.PollInterval, DefaultPollInterval),
		httpTimeout:    orDefault(opts.HTTPTimeout, DefaultHTTPTimeout),
		jwtExpiry:      jwtExp,
		maxAttempts4xx: max4xx,
		maxAttempts5xx: max5xx,
		cursorKey:      cursorKey,
		rl: newRateLimiter(opts.Clock,
			opts.RateLimitPerMinute, opts.RateLimitPerDay, opts.CooldownDuration),
		attempt: make(map[store.PushSubscriptionID]*subAttempt),
		kickCh:  make(chan struct{}, 1),
	}
	observe.RegisterWebPushMetrics(
		func() float64 { return float64(d.subsActive.Load()) },
		func() float64 { return float64(d.rl.CooldownsActive()) },
	)
	return d, nil
}

// Run drives the change-feed-poll loop until ctx cancels. Returns
// nil on cancellation; non-nil only on a fatal store error. The loop
// is single-goroutine; a second concurrent Run on the same Dispatcher
// returns an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	if !d.running.CompareAndSwap(false, true) {
		return errors.New("webpush: dispatcher already running")
	}
	defer d.running.Store(false)

	if !d.vapid.Configured() {
		d.logger.LogAttrs(ctx, slog.LevelInfo,
			"webpush: VAPID not configured; dispatcher idle")
		// Stay alive so the lifecycle errgroup does not see an early
		// return; the operator may load a key later. We just block on
		// ctx.
		<-ctx.Done()
		return nil
	}

	// Cursor hydration.
	if seq, err := d.store.Meta().GetFTSCursor(ctx, d.cursorKey); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("webpush: load cursor: %w", err)
		}
		return nil
	} else {
		d.cursor.Store(seq)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		processed, err := d.tick(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: tick error",
				slog.String("err", err.Error()))
		}
		if processed >= DefaultBatchSize {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-d.kickCh:
		case <-d.clock.After(d.pollInterval):
		}
	}
}

// Close is provided for symmetry with other lifecycle workers and so
// the JMAP push handler can call it on shutdown. The dispatcher's
// Run path drains on ctx cancellation; Close is a no-op stub today
// but keeps the lifecycle interface stable.
func (d *Dispatcher) Close(ctx context.Context) error {
	_ = ctx
	return nil
}

// tick reads one batch of change-feed entries, fans the relevant
// kinds out to push subscriptions, and returns the number of changes
// inspected so the Run loop can decide whether to drain or sleep.
func (d *Dispatcher) tick(ctx context.Context) (int, error) {
	changes, err := d.store.FTS().ReadChangeFeedForFTS(ctx, d.cursor.Load(), DefaultBatchSize)
	if err != nil {
		return 0, fmt.Errorf("webpush: read change feed: %w", err)
	}
	if len(changes) == 0 {
		return 0, nil
	}
	var maxSeq uint64
	for _, c := range changes {
		if c.Seq > maxSeq {
			maxSeq = c.Seq
		}
		if !isPushable(c.Kind) {
			continue
		}
		d.processChange(ctx, c)
	}
	if maxSeq > 0 {
		d.cursor.Store(maxSeq)
		if err := d.store.Meta().SetFTSCursor(ctx, d.cursorKey, maxSeq); err != nil {
			d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: persist cursor",
				slog.String("err", err.Error()))
		}
	}
	return len(changes), nil
}

// isPushable reports whether the change-feed kind is one the 3.8b
// dispatcher fans out. REQ-PROTO-125 enumerates Email / ChatMessage /
// CalendarEvent (plus the reaction-on-Email case which surfaces here
// as a regular Email update — see the TODO(3.9-coord) note in
// payload.go).
func isPushable(k store.EntityKind) bool {
	switch k {
	case store.EntityKindEmail,
		store.EntityKindChatMessage,
		store.EntityKindCalendarEvent:
		return true
	}
	return false
}

// processChange dispatches one change-feed entry to every eligible
// push subscription owned by the affected principal.
func (d *Dispatcher) processChange(ctx context.Context, ch store.FTSChange) {
	subs, err := d.store.Meta().ListPushSubscriptionsByPrincipal(ctx, ch.PrincipalID)
	if err != nil {
		d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: list subscriptions",
			slog.Uint64("principal", uint64(ch.PrincipalID)),
			slog.String("err", err.Error()))
		return
	}
	d.subsActive.Store(int64(len(subs)))

	if len(subs) == 0 {
		return
	}
	// Promote the StateChange into the typed shape BuildPayload
	// consumes. (We feed FTSChange's id-set here; the two structs
	// are byte-compatible on the relevant fields.)
	ev := store.StateChange{
		Seq:            store.ChangeSeq(ch.Seq),
		PrincipalID:    ch.PrincipalID,
		Kind:           ch.Kind,
		EntityID:       ch.EntityID,
		ParentEntityID: ch.ParentEntityID,
		Op:             ch.Op,
		ProducedAt:     ch.ProducedAt,
	}
	payload, err := BuildPayload(ctx, d.store, ev)
	if err != nil {
		if errors.Is(err, errUnsupportedKind) {
			return
		}
		d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: build payload",
			slog.Uint64("principal", uint64(ch.PrincipalID)),
			slog.String("kind", string(ch.Kind)),
			slog.String("err", err.Error()))
		return
	}

	for _, sub := range subs {
		if !sub.Verified {
			continue
		}
		if !subscriptionMatchesKind(sub, ch.Kind) {
			continue
		}
		if sub.Expires != nil && d.clock.Now().After(*sub.Expires) {
			continue
		}
		d.sendOne(ctx, sub, payload, ch.Kind)
	}
}

// subscriptionMatchesKind returns true when sub.Types is empty (subscribe
// to everything) or contains the JMAP datatype name corresponding to k.
func subscriptionMatchesKind(sub store.PushSubscription, k store.EntityKind) bool {
	if len(sub.Types) == 0 {
		return true
	}
	jmapName := jmapTypeName(k)
	for _, t := range sub.Types {
		if t == jmapName {
			return true
		}
	}
	return false
}

// jmapTypeName maps an internal store.EntityKind to the JMAP datatype
// name advertised on the wire (e.g. EntityKindEmail -> "Email"). The
// JMAP spec uses CamelCase type names; herold's internal kinds are
// snake_case strings.
func jmapTypeName(k store.EntityKind) string {
	switch k {
	case store.EntityKindEmail:
		return "Email"
	case store.EntityKindMailbox:
		return "Mailbox"
	case store.EntityKindChatMessage:
		return "ChatMessage"
	case store.EntityKindCalendarEvent:
		return "CalendarEvent"
	case store.EntityKindCalendar:
		return "Calendar"
	case store.EntityKindAddressBook:
		return "AddressBook"
	case store.EntityKindContact:
		return "Contact"
	case store.EntityKindEmailSubmission:
		return "EmailSubmission"
	case store.EntityKindIdentity:
		return "Identity"
	case store.EntityKindPushSubscription:
		return "PushSubscription"
	}
	return string(k)
}

// sendOne handles the rate-limit + retry + HTTP POST for one
// (subscription, payload) pair. It is the per-push hot path; a
// failure here is logged and accounted in metrics but does not
// propagate up to the Run loop (one bad subscription cannot stall
// the whole dispatcher).
func (d *Dispatcher) sendOne(
	ctx context.Context,
	sub store.PushSubscription,
	payload buildPayloadResult,
	kind store.EntityKind,
) {
	// Honour pending retry deadlines: if we recently logged a 5xx
	// for this subscription and the backoff window hasn't elapsed,
	// skip this fan-out. The retry-on-elapse path is driven by the
	// next Run tick, so a busy feed automatically retries when the
	// timer expires.
	d.retryMu.Lock()
	at := d.attempt[sub.ID]
	d.retryMu.Unlock()
	if at != nil && d.clock.Now().Before(at.nextAttempt) {
		return
	}

	switch out, entered := d.rl.Allow(sub.ID); out {
	case rateOK:
	case rateBucketExhausted:
		observe.WebPushDeliveriesTotal.WithLabelValues("rate_limited").Inc()
		return
	case rateDailyExhausted:
		observe.WebPushDeliveriesTotal.WithLabelValues("rate_limited").Inc()
		return
	case rateCooldown:
		observe.WebPushDeliveriesTotal.WithLabelValues("cooldown").Inc()
		if entered {
			d.logger.LogAttrs(ctx, slog.LevelWarn,
				"webpush: subscription entered cooldown (sustained excess)",
				slog.Uint64("principal", uint64(sub.PrincipalID)),
				slog.Uint64("subscription", uint64(sub.ID)),
				slog.String("kind", string(kind)),
			)
		}
		return
	}

	d.deliver(ctx, sub, payload.JSON, payload.CoalesceTag, urgencyForKind(kind))
}

// urgencyForKind picks the RFC 8030 §5.3 Urgency header value per
// event class. Chat is the "interactive" tier, mail is "normal",
// calendar is also "normal".
func urgencyForKind(k store.EntityKind) string {
	if k == store.EntityKindChatMessage {
		return "high"
	}
	return "normal"
}

// SendVerificationPing dispatches the RFC 8620 §7.2 verification
// ping for a freshly created subscription. The JMAP push handler
// fires this asynchronously after PushSubscription/set { create }
// returns, so the JMAP response is not blocked on the push gateway.
//
// Outcome handling:
//   - 201/200/204: success; no further action (the client will echo
//     the verificationCode via /set update to flip Verified).
//   - 410/404: the URL is bad — delete the subscription immediately.
//   - other: log and give up; the next dispatcher pass to this row
//     will fail the same way and the client's UI surfaces "push
//     unconfirmed" until the user re-subscribes.
func (d *Dispatcher) SendVerificationPing(ctx context.Context, sub store.PushSubscription) error {
	if !d.vapid.Configured() {
		return vapid.ErrNotConfigured
	}
	body, err := json.Marshal(struct {
		Type             string `json:"type"`
		VerificationCode string `json:"code"`
	}{
		Type:             "verification",
		VerificationCode: sub.VerificationCode,
	})
	if err != nil {
		return fmt.Errorf("webpush: marshal verification payload: %w", err)
	}
	envelope, err := Encrypt(body, sub.P256DH, sub.Auth, nil)
	if err != nil {
		return fmt.Errorf("webpush: encrypt verification: %w", err)
	}
	resp, err := d.post(ctx, sub, envelope, "", "high")
	if err != nil {
		d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: verification ping failed",
			slog.Uint64("subscription", uint64(sub.ID)),
			slog.String("err", err.Error()))
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		observe.WebPushDeliveriesTotal.WithLabelValues("success").Inc()
		return nil
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		observe.WebPushDeliveriesTotal.WithLabelValues("gone").Inc()
		_ = d.store.Meta().DeletePushSubscription(ctx, sub.ID)
		d.rl.Forget(sub.ID)
		return fmt.Errorf("webpush: gateway returned %d on verification ping; subscription deleted", resp.StatusCode)
	default:
		observe.WebPushDeliveriesTotal.WithLabelValues("rejected").Inc()
		return fmt.Errorf("webpush: gateway returned %d on verification ping", resp.StatusCode)
	}
}

// deliver runs the encrypt + POST path for a non-verification push.
// Outcome handling per REQ-PROTO-123 lives here.
func (d *Dispatcher) deliver(
	ctx context.Context,
	sub store.PushSubscription,
	payload []byte,
	coalesceTag string,
	urgency string,
) {
	envelope, err := Encrypt(payload, sub.P256DH, sub.Auth, nil)
	if err != nil {
		d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: encrypt",
			slog.Uint64("subscription", uint64(sub.ID)),
			slog.String("err", err.Error()))
		observe.WebPushDeliveriesTotal.WithLabelValues("rejected").Inc()
		return
	}
	startedAt := d.clock.Now()
	resp, err := d.post(ctx, sub, envelope, coalesceTag, urgency)
	if err != nil {
		d.logger.LogAttrs(ctx, slog.LevelWarn, "webpush: post",
			slog.Uint64("subscription", uint64(sub.ID)),
			slog.String("err", err.Error()))
		observe.WebPushDeliveriesTotal.WithLabelValues("rejected").Inc()
		d.scheduleRetry(sub, true)
		return
	}
	defer resp.Body.Close()
	observe.WebPushDeliverySeconds.Observe(d.clock.Now().Sub(startedAt).Seconds())

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		observe.WebPushDeliveriesTotal.WithLabelValues("success").Inc()
		d.clearRetry(sub.ID)
		// Best-effort touch of UpdatedAt by a no-op update; the
		// store does not yet expose a dedicated "touch" method, so
		// we call UpdatePushSubscription with the current row to
		// bump its UpdatedAt. A persistence-layer failure here is
		// not fatal — the success metric has already incremented.
		_ = d.store.Meta().UpdatePushSubscription(ctx, sub)
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		observe.WebPushDeliveriesTotal.WithLabelValues("gone").Inc()
		_ = d.store.Meta().DeletePushSubscription(ctx, sub.ID)
		d.clearRetry(sub.ID)
		d.rl.Forget(sub.ID)
	case resp.StatusCode >= 500:
		observe.WebPushDeliveriesTotal.WithLabelValues("retry").Inc()
		d.scheduleRetry(sub, true)
	case resp.StatusCode >= 400:
		// 4xx other than 410/404: bounded retry, then destroy.
		exhausted := d.scheduleRetry(sub, false)
		if exhausted {
			observe.WebPushDeliveriesTotal.WithLabelValues("rejected").Inc()
			_ = d.store.Meta().DeletePushSubscription(ctx, sub.ID)
			d.clearRetry(sub.ID)
			d.rl.Forget(sub.ID)
		} else {
			observe.WebPushDeliveriesTotal.WithLabelValues("retry").Inc()
		}
	default:
		// 1xx / 3xx: unexpected. Log and treat as non-success.
		observe.WebPushDeliveriesTotal.WithLabelValues("rejected").Inc()
	}
}

// post builds and sends one outbound HTTP request. It signs a fresh
// VAPID JWT per request (audience = origin of sub.URL) and attaches
// the RFC 8030 transport headers.
func (d *Dispatcher) post(
	ctx context.Context,
	sub store.PushSubscription,
	envelope []byte,
	coalesceTag, urgency string,
) (*http.Response, error) {
	audience, err := vapid.AudienceFromEndpoint(sub.URL)
	if err != nil {
		return nil, err
	}
	now := d.clock.Now()
	jwt, err := d.vapid.SignVAPIDJWT(audience, now, now.Add(d.jwtExpiry), d.subject)
	if err != nil {
		return nil, fmt.Errorf("webpush: sign jwt: %w", err)
	}
	pub, err := d.vapid.PublicKeyB64URL()
	if err != nil {
		return nil, fmt.Errorf("webpush: public key: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, d.httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.URL, bytes.NewReader(envelope))
	if err != nil {
		return nil, fmt.Errorf("webpush: new request: %w", err)
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", fmt.Sprintf("%d", DefaultTTLSeconds))
	req.Header.Set("Urgency", urgency)
	req.Header.Set("Authorization", fmt.Sprintf("vapid t=%s, k=%s", jwt, pub))
	if coalesceTag != "" {
		// REQ-PROTO-124 (3.8c): coalesce-tag becomes the Topic header
		// so the gateway / browser replaces a prior notification with
		// the same tag rather than stacking. 3.8b leaves the field
		// empty by default; the BuildPayload return value carries the
		// candidate tag through unchanged so 3.8c can flip the wire
		// header on without any further plumbing.
		// TODO(3.8c-coord): set req.Header.Set("Topic", coalesceTag)
		// once the coalescing window logic is in place.
		_ = coalesceTag
	}
	return d.doer.Do(req)
}

// scheduleRetry records a retry attempt for sub and returns true when
// the retry budget for the relevant class has been exhausted (and the
// caller should destroy the subscription).
//
// is5xx selects the per-class budget: 5xx gets 5 attempts with the
// (1, 4, 16, 64, 256) second backoff; 4xx gets 3 attempts with
// (1, 4, 16). Both schedules use base-4 exponential backoff capped at
// the per-class attempt count.
func (d *Dispatcher) scheduleRetry(sub store.PushSubscription, is5xx bool) (exhausted bool) {
	d.retryMu.Lock()
	defer d.retryMu.Unlock()
	at := d.attempt[sub.ID]
	if at == nil {
		at = &subAttempt{}
		d.attempt[sub.ID] = at
	}
	at.count++
	at.last5xxClass = is5xx
	limit := d.maxAttempts4xx
	if is5xx {
		limit = d.maxAttempts5xx
	}
	if at.count >= limit {
		return true
	}
	// Backoff: 4^(count-1) seconds. count=1 -> 1s; count=2 -> 4s; etc.
	delaySec := 1
	for i := 1; i < at.count; i++ {
		delaySec *= 4
	}
	at.nextAttempt = d.clock.Now().Add(time.Duration(delaySec) * time.Second)
	return false
}

// clearRetry forgets the in-memory retry state for sub. Called on
// success (the next failure starts fresh) and on destroy (no point
// keeping bookkeeping for a row that no longer exists).
func (d *Dispatcher) clearRetry(id store.PushSubscriptionID) {
	d.retryMu.Lock()
	delete(d.attempt, id)
	d.retryMu.Unlock()
}

// Kick wakes the Run loop if it is idle. Exposed so future callers
// (e.g. a 3.8c coalescing flush) can nudge the dispatcher without
// waiting a full PollInterval; today only Run reads kickCh, so the
// channel sees no traffic until 3.8c lands the producer.
func (d *Dispatcher) Kick() {
	select {
	case d.kickCh <- struct{}{}:
	default:
	}
}

func orDefault[T ~int64 | ~int](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}
