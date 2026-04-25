package protochat

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/hanshuebner/herold/internal/store"
)

// ConnID is the broadcaster-assigned per-connection token. Stable
// across the connection lifetime; reused only after Unregister.
type ConnID uint64

// Sender is the broadcaster-facing handle each connection registers.
// Send must be non-blocking: a slow client must not stall the
// broadcaster's fanout loop. Implementations enqueue to a bounded
// channel and return ErrFull if the queue is saturated.
type Sender interface {
	Send(f ServerFrame) error
	Principal() store.PrincipalID
}

// ErrFull is returned by Sender.Send when the per-connection write
// queue is full. The broadcaster logs and drops; the connection's
// own write-pump will close it on the next backpressure tick.
var ErrFull = errors.New("protochat: send queue full")

// MembershipResolver answers "is principal a member of conversation".
// Track B's chat-store path provides the production implementation;
// tests substitute a deterministic in-memory map. Returning ok=false
// rejects the originator's frame with "not_a_member"; returning err
// closes the operation as a transient failure logged warn.
type MembershipResolver func(ctx context.Context, conv string, pid store.PrincipalID) (ok bool, err error)

// MembersResolver returns the principal ids that are members of a
// conversation. Used by EmitToConversation to fan out a frame to
// every member's connections.
type MembersResolver func(ctx context.Context, conv string) ([]store.PrincipalID, error)

// Broadcaster is the in-process pub-sub for ephemeral chat events.
// One instance lives in the parent server and is shared by the
// protochat HTTP handler and (later) track D's video-call package.
type Broadcaster struct {
	logger *slog.Logger

	mu      sync.RWMutex
	nextID  uint64
	byID    map[ConnID]*broadcastSub
	byPrinc map[store.PrincipalID]map[ConnID]*broadcastSub

	memberLookup MembersResolver

	// stats
	delivered atomic.Uint64
	dropped   atomic.Uint64
}

// broadcastSub is the broadcaster's per-connection record. Subs is
// the connection's subscribed-conversation set (used by typing /
// presence dispatch); the broadcaster reads it under mu.RLock so
// fanout doesn't need to lock the per-connection state.
type broadcastSub struct {
	id        ConnID
	principal store.PrincipalID
	sender    Sender

	subsMu sync.RWMutex
	subs   map[string]struct{}
}

// NewBroadcaster constructs a broadcaster. The MembersResolver
// callback is consulted by EmitToConversation; passing nil makes
// EmitToConversation a no-op (logged warn) so a misconfigured wiring
// fails closed.
func NewBroadcaster(logger *slog.Logger, members MembersResolver) *Broadcaster {
	if logger == nil {
		logger = slog.Default()
	}
	return &Broadcaster{
		logger:       logger,
		byID:         make(map[ConnID]*broadcastSub),
		byPrinc:      make(map[store.PrincipalID]map[ConnID]*broadcastSub),
		memberLookup: members,
	}
}

// Register subscribes sender to events for principal. The returned
// id is stable for the connection's lifetime and must be passed to
// Unregister on cleanup.
func (b *Broadcaster) Register(principal store.PrincipalID, sender Sender) ConnID {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := ConnID(b.nextID)
	sub := &broadcastSub{
		id:        id,
		principal: principal,
		sender:    sender,
		subs:      make(map[string]struct{}),
	}
	b.byID[id] = sub
	if _, ok := b.byPrinc[principal]; !ok {
		b.byPrinc[principal] = make(map[ConnID]*broadcastSub)
	}
	b.byPrinc[principal][id] = sub
	return id
}

// Unregister removes the sub. Idempotent — calling on an unknown id
// is a no-op so the connection's deferred cleanup can run unguarded.
func (b *Broadcaster) Unregister(id ConnID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub, ok := b.byID[id]
	if !ok {
		return
	}
	delete(b.byID, id)
	if m, ok := b.byPrinc[sub.principal]; ok {
		delete(m, id)
		if len(m) == 0 {
			delete(b.byPrinc, sub.principal)
		}
	}
}

// HasConnection reports whether at least one connection is currently
// registered for principal. Used by the presence tracker to decide
// whether to emit an "offline" event after the disconnect grace
// window — if the principal reconnected within the grace window, we
// suppress the offline emission.
func (b *Broadcaster) HasConnection(principal store.PrincipalID) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	m, ok := b.byPrinc[principal]
	return ok && len(m) > 0
}

// addSubscriptions records that the connection identified by id has
// subscribed to the supplied conversation ids. Idempotent.
func (b *Broadcaster) addSubscriptions(id ConnID, convs []string) {
	b.mu.RLock()
	sub, ok := b.byID[id]
	b.mu.RUnlock()
	if !ok {
		return
	}
	sub.subsMu.Lock()
	for _, c := range convs {
		sub.subs[c] = struct{}{}
	}
	sub.subsMu.Unlock()
}

// removeSubscriptions removes ids' interest in the supplied
// conversations. Idempotent.
func (b *Broadcaster) removeSubscriptions(id ConnID, convs []string) {
	b.mu.RLock()
	sub, ok := b.byID[id]
	b.mu.RUnlock()
	if !ok {
		return
	}
	sub.subsMu.Lock()
	for _, c := range convs {
		delete(sub.subs, c)
	}
	sub.subsMu.Unlock()
}

// Emit fans out a ServerFrame to every connection registered for
// recipient. Drops are non-fatal: a full queue logs warn and counts
// against the dropped metric. Emit MUST NOT block on slow consumers.
func (b *Broadcaster) Emit(recipient store.PrincipalID, f ServerFrame) {
	b.mu.RLock()
	subs := b.byPrinc[recipient]
	// Snapshot the senders so we hold the broadcaster lock for the
	// shortest possible time; senders are non-blocking but Send may
	// still allocate.
	out := make([]Sender, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.sender)
	}
	b.mu.RUnlock()
	for _, s := range out {
		if err := s.Send(f); err != nil {
			b.dropped.Add(1)
			b.logger.Warn("protochat.broadcast.drop",
				"recipient", uint64(recipient),
				"type", f.Type,
				"err", err.Error())
			continue
		}
		b.delivered.Add(1)
	}
}

// EmitToConversation resolves the conversation's members through the
// configured MembersResolver and forwards f to every member's
// connections. The originating principal (if any) is excluded by
// passing it via excludeSelf; passing zero includes everyone.
func (b *Broadcaster) EmitToConversation(ctx context.Context, conv string, excludeSelf store.PrincipalID, f ServerFrame) {
	if b.memberLookup == nil {
		b.logger.Warn("protochat.broadcast.no_members_resolver", "conv", conv)
		return
	}
	pids, err := b.memberLookup(ctx, conv)
	if err != nil {
		b.logger.Warn("protochat.broadcast.members_lookup_failed",
			"conv", conv, "err", err.Error())
		return
	}
	for _, pid := range pids {
		if pid == excludeSelf {
			continue
		}
		b.Emit(pid, f)
	}
}

// EmitToSubscribers fans out f to every connection that has
// subscribe-d to conv. Used for presence updates: typing fans out to
// the conversation's members, but presence fans out to everyone who
// has explicitly subscribed.
func (b *Broadcaster) EmitToSubscribers(conv string, f ServerFrame) {
	b.mu.RLock()
	out := make([]Sender, 0, 8)
	for _, sub := range b.byID {
		sub.subsMu.RLock()
		_, has := sub.subs[conv]
		sub.subsMu.RUnlock()
		if has {
			out = append(out, sub.sender)
		}
	}
	b.mu.RUnlock()
	for _, s := range out {
		if err := s.Send(f); err != nil {
			b.dropped.Add(1)
			b.logger.Warn("protochat.broadcast.drop",
				"type", f.Type,
				"err", err.Error())
			continue
		}
		b.delivered.Add(1)
	}
}

// Stats returns a snapshot of broadcaster counters. Used by tests
// and by the metrics collector when one lands.
func (b *Broadcaster) Stats() (delivered, dropped uint64) {
	return b.delivered.Load(), b.dropped.Load()
}
