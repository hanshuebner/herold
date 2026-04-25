package protoevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// PluginInvoker is the supervisor surface the dispatcher consumes. It
// matches the shape used by spam.PluginInvoker and autodns.PluginInvoker
// so production wiring layers a tiny adapter over *plugin.Manager and
// tests substitute a fake. Decoupling here lets us unit-test the
// dispatcher without spinning up the supervisor.
type PluginInvoker interface {
	Call(ctx context.Context, plugin, method string, params any, result any) error
}

// MethodEventsPublish is the JSON-RPC method name expected by event-publisher
// plugins (mirrors plugins/sdk.MethodEventsPublish to avoid a cycle on the
// SDK package from internal code).
const MethodEventsPublish = "events.publish"

// CursorKeyPrefix is the prefix used in the cursors table for per-plugin
// change-feed cursors. The full key is "events.<plugin-name>".
const CursorKeyPrefix = "events."

// Options configures a Dispatcher.
type Options struct {
	// Store is the metadata + change-feed handle. Required for the
	// change-feed-driven derivation path; nil disables it but Emit still
	// works for operational events.
	Store store.Store
	// Plugins is the supervisor adapter the dispatcher uses to invoke
	// events.publish. Required when PluginNames is non-empty.
	Plugins PluginInvoker
	// Logger is the structured logger; required.
	Logger *slog.Logger
	// Clock is the time source; required.
	Clock clock.Clock
	// PluginNames lists the configured event-publisher plugin names.
	// The dispatcher fans every event to every plugin (REQ-EVT-22 —
	// plugin-side filtering happens through events.subscribe upstream).
	PluginNames []string
	// ChangeFeedPrincipals enumerates which principals the change-feed
	// reader paths cover. Phase 2 wires this from a discovery loop; the
	// minimal in-memory test harness supplies it directly.
	ChangeFeedPrincipals []store.PrincipalID
	// BufferSize bounds the in-process Emit channel. Zero defers to
	// the default of 1024.
	BufferSize int
	// PollInterval is how often the change-feed reader polls per
	// principal. Zero defers to 5 seconds.
	PollInterval time.Duration
	// PublishTimeout is the per-call deadline applied to events.publish
	// when the caller's ctx has none. Zero defers to 5 seconds.
	PublishTimeout time.Duration
	// MaxRetries caps the number of publish retries per (plugin, event)
	// pair before the dispatcher emits an EventPublishFailed envelope.
	// Zero defers to 3.
	MaxRetries int
	// RetryBackoff is the base back-off between retries. Zero defers to
	// 200ms; the dispatcher applies a doubling schedule capped at 5s.
	RetryBackoff time.Duration
	// Server hostname recorded in the publish-failed payload to make it
	// distinguishable across nodes. Optional.
	Hostname string
}

const (
	defaultBufferSize     = 1024
	defaultPollInterval   = 5 * time.Second
	defaultPublishTimeout = 5 * time.Second
	defaultMaxRetries     = 3
	defaultRetryBackoff   = 200 * time.Millisecond
	maxRetryBackoff       = 5 * time.Second
)

// Dispatcher is the typed event fan-out engine. One Dispatcher per
// server. Construct with New and run with Run; emit events with Emit.
//
// The dispatcher does NOT persist events in a queue. Operational
// (Emit-driven) events are best-effort: dropped on buffer overflow or
// process exit. Critical events flow through the change feed instead,
// which is durable.
type Dispatcher struct {
	opts Options

	in chan Event
	id idGen
}

// New constructs a Dispatcher. Logger and Clock are required.
func New(opts Options) (*Dispatcher, error) {
	if opts.Logger == nil {
		return nil, errors.New("protoevents: Options.Logger required")
	}
	if opts.Clock == nil {
		return nil, errors.New("protoevents: Options.Clock required")
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = defaultBufferSize
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.PublishTimeout <= 0 {
		opts.PublishTimeout = defaultPublishTimeout
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = defaultMaxRetries
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = defaultRetryBackoff
	}
	return &Dispatcher{
		opts: opts,
		in:   make(chan Event, opts.BufferSize),
	}, nil
}

// Emit synchronously enqueues ev for dispatch. Returns when the event is
// in the buffer; actual fan-out is asynchronous. If the buffer is full,
// blocks up to ctx.Deadline before returning ctx.Err.
//
// Producers MUST treat the channel as best-effort. A non-nil error from
// Emit is non-fatal: log and continue. Critical events go through the
// change feed.
func (d *Dispatcher) Emit(ctx context.Context, ev Event) error {
	if ev.ID == "" {
		ev.ID = d.id.next(d.opts.Clock.Now())
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = d.opts.Clock.Now()
	}
	select {
	case d.in <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryEmit attempts a non-blocking enqueue. Returns false when the
// buffer is full, never blocks. Useful from hot paths (mail-flow) where
// the producer cannot afford to wait.
func (d *Dispatcher) TryEmit(ev Event) bool {
	if ev.ID == "" {
		ev.ID = d.id.next(d.opts.Clock.Now())
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = d.opts.Clock.Now()
	}
	select {
	case d.in <- ev:
		return true
	default:
		return false
	}
}

// Run starts the dispatcher loop. Blocks until ctx is cancelled. The
// loop reads from the in-process Emit channel and the per-principal
// change feed concurrently and fans out to every registered publisher
// plugin.
func (d *Dispatcher) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runEmitLoop(ctx)
	}()

	if d.opts.Store != nil {
		for _, pid := range d.opts.ChangeFeedPrincipals {
			pid := pid
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.runChangeFeedLoop(ctx, pid)
			}()
		}
	}

	wg.Wait()
	return ctx.Err()
}

// runEmitLoop drains the internal Emit channel until ctx is cancelled.
// Each event is fanned out to every registered plugin; the call is
// performed inline (one event at a time) so the buffer applies natural
// back-pressure.
func (d *Dispatcher) runEmitLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-d.in:
			d.dispatch(ctx, ev)
		}
	}
}

// runChangeFeedLoop drives the per-principal change-feed-derived event
// path. The loop polls store.ReadChangeFeed(pid, cursor), maps each row
// to its mail-flow event, and feeds the result into Emit. Cursor
// persistence uses the cursors table keyed "events.changefeed.<pid>"
// so a restart resumes from the last emitted change.
func (d *Dispatcher) runChangeFeedLoop(ctx context.Context, pid store.PrincipalID) {
	cursorKey := fmt.Sprintf("events.changefeed.%d", pid)
	meta := d.opts.Store.Meta()
	cur, err := meta.GetFTSCursor(ctx, cursorKey)
	if err != nil {
		d.opts.Logger.Warn("protoevents.changefeed.cursor_load_failed",
			"err", err, "pid", pid)
	}
	cursor := store.ChangeSeq(cur)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		changes, err := meta.ReadChangeFeed(ctx, pid, cursor, 256)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			d.opts.Logger.Warn("protoevents.changefeed.read_failed",
				"err", err, "pid", pid)
			select {
			case <-ctx.Done():
				return
			case <-d.opts.Clock.After(d.opts.PollInterval):
			}
			continue
		}
		for _, c := range changes {
			ev, ok := changeToEvent(c)
			if ok {
				if err := d.Emit(ctx, ev); err != nil {
					return
				}
			}
			if c.Seq > cursor {
				cursor = c.Seq
			}
		}
		if len(changes) > 0 {
			if err := meta.SetFTSCursor(ctx, cursorKey, uint64(cursor)); err != nil {
				d.opts.Logger.Warn("protoevents.changefeed.cursor_save_failed",
					"err", err, "pid", pid)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-d.opts.Clock.After(d.opts.PollInterval):
		}
	}
}

// changeToEvent maps a StateChange row onto the corresponding mail-flow
// Event. Returns false for kinds the dispatcher does not surface as
// first-class events (mailbox renames, identity edits, etc. — those are
// observed via JMAP push, not the event bus).
func changeToEvent(c store.StateChange) (Event, bool) {
	if c.Kind != store.EntityKindEmail {
		return Event{}, false
	}
	switch c.Op {
	case store.ChangeOpCreated:
		pid := c.PrincipalID
		payload, _ := MarshalPayload(MailReceivedPayload{
			MessageID: fmt.Sprintf("%d", c.EntityID),
		})
		return Event{
			Kind:        EventMailReceived,
			Subject:     fmt.Sprintf("%d", pid),
			PrincipalID: &pid,
			OccurredAt:  c.ProducedAt,
			Payload:     payload,
		}, true
	case store.ChangeOpDestroyed:
		// Email expunge is not an externally meaningful event in v1.
		// Operators care about delivery, not local destruction.
		return Event{}, false
	default:
		return Event{}, false
	}
}

// dispatch fans ev out to every registered plugin. Per-plugin failures
// are retried up to MaxRetries; on permanent failure an
// EventPublishFailed envelope is emitted with RetryBudgetExhausted set
// (publishers MUST NOT re-emit on receipt).
func (d *Dispatcher) dispatch(ctx context.Context, ev Event) {
	if len(d.opts.PluginNames) == 0 {
		return
	}
	if d.opts.Plugins == nil {
		d.opts.Logger.Warn("protoevents.dispatch.no_invoker", "kind", ev.Kind)
		return
	}
	for _, name := range d.opts.PluginNames {
		d.dispatchOne(ctx, name, ev)
	}
}

// dispatchOne handles publication of one event to one plugin. Errors
// surface as logs + retries; once the retry budget is exhausted, an
// EventPublishFailed envelope is enqueued (subject to the same
// best-effort semantics — buffer-full conditions drop it silently
// rather than blocking).
func (d *Dispatcher) dispatchOne(ctx context.Context, plugin string, ev Event) {
	backoff := d.opts.RetryBackoff
	var lastErr error
	for attempt := 0; attempt <= d.opts.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, d.opts.PublishTimeout)
		err := d.opts.Plugins.Call(callCtx, plugin, MethodEventsPublish, eventToParams(ev), nil)
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		d.opts.Logger.Warn("protoevents.publish.failed",
			"plugin", plugin, "kind", ev.Kind, "id", ev.ID,
			"attempt", attempt+1, "err", err)
		if attempt == d.opts.MaxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-d.opts.Clock.After(backoff):
		}
		backoff *= 2
		if backoff > maxRetryBackoff {
			backoff = maxRetryBackoff
		}
	}
	// Permanent failure path. Avoid an emit loop: when the originating
	// event itself is an EventPublishFailed, do not re-emit.
	if ev.Kind == EventPublishFailed || ev.RetryBudgetExhausted {
		d.opts.Logger.Error("protoevents.publish.dropped_after_retries",
			"plugin", plugin, "kind", ev.Kind, "id", ev.ID, "err", lastErr)
		return
	}
	payload, _ := MarshalPayload(PublishFailedPayload{
		PluginName: plugin,
		OrigID:     ev.ID,
		OrigKind:   string(ev.Kind),
		Reason:     errString(lastErr),
	})
	failEv := Event{
		Kind:                 EventPublishFailed,
		Subject:              plugin,
		OccurredAt:           d.opts.Clock.Now(),
		Payload:              payload,
		RetryBudgetExhausted: true,
	}
	if !d.TryEmit(failEv) {
		d.opts.Logger.Error("protoevents.publish.failed_event_dropped",
			"plugin", plugin, "id", ev.ID)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// eventToParams shapes the wire payload sent to plugins as the params
// of events.publish. Mirrors plugins/sdk.EventsPublishParams.Event so
// the SDK's struct unmarshal works on the receiving side.
func eventToParams(ev Event) map[string]any {
	out := map[string]any{
		"id":          ev.ID,
		"kind":        string(ev.Kind),
		"occurred_at": ev.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	if ev.Subject != "" {
		out["subject"] = ev.Subject
	}
	if ev.PrincipalID != nil {
		out["principal_id"] = uint64(*ev.PrincipalID)
	}
	if len(ev.Payload) > 0 {
		var raw any
		if err := json.Unmarshal(ev.Payload, &raw); err == nil {
			out["payload"] = raw
		} else {
			out["payload"] = string(ev.Payload)
		}
	}
	if ev.RetryBudgetExhausted {
		out["retry_budget_exhausted"] = true
	}
	return map[string]any{"event": out}
}
