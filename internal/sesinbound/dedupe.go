package sesinbound

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// SeenStore is the persistence interface for SES MessageId deduplication.
// The store backend (SQLite or Postgres) implements this via the
// ses_seen_messages table; tests supply a stub.
type SeenStore interface {
	// IsSESSeen returns true if the message ID has been recorded within
	// the retention window.
	IsSESSeen(ctx context.Context, messageID string) (bool, error)
	// InsertSESSeen records the message ID as seen with the given
	// timestamp.  Duplicate inserts are silently ignored (UPSERT).
	InsertSESSeen(ctx context.Context, messageID string, seenAt time.Time) error
	// GCOldSESSeen deletes rows whose seen_at is older than cutoff.
	GCOldSESSeen(ctx context.Context, cutoff time.Time) error
}

// deduper combines an in-process LRU cache with a durable SeenStore.
// The LRU is the fast path; the store is consulted on LRU miss so a
// restart does not lose dedupe state.
type deduper struct {
	mu      sync.Mutex
	maxSize int
	lru     *list.List
	items   map[string]*list.Element
	store   SeenStore
	ttl     time.Duration
}

type lruItem struct {
	id     string
	seenAt time.Time
}

// newDeduper creates a deduper backed by store.  maxSize bounds the
// in-process LRU; ttl is the minimum dedupe window (≥24 h per
// REQ-HOOK-SES-02).
func newDeduper(store SeenStore, maxSize int, ttl time.Duration) *deduper {
	if maxSize <= 0 {
		maxSize = 8192
	}
	if ttl <= 0 {
		ttl = 25 * time.Hour // 25 h to cover the 24 h minimum with margin
	}
	return &deduper{
		maxSize: maxSize,
		lru:     list.New(),
		items:   make(map[string]*list.Element),
		store:   store,
		ttl:     ttl,
	}
}

// IsSeen returns true if messageID has been seen before.  On a LRU hit
// it skips the store lookup.  On a LRU miss it consults the durable store.
func (d *deduper) IsSeen(ctx context.Context, messageID string) (bool, error) {
	d.mu.Lock()
	if el, ok := d.items[messageID]; ok {
		d.lru.MoveToFront(el)
		d.mu.Unlock()
		return true, nil
	}
	d.mu.Unlock()

	// LRU miss: ask the durable store.
	return d.store.IsSESSeen(ctx, messageID)
}

// MarkSeen records messageID as seen in both the LRU and the durable
// store.  The store insert is fire-and-soft-fail: a database error is
// returned to the caller (who should treat it as a soft error and
// continue, since losing dedupe durability is not silent data loss).
func (d *deduper) MarkSeen(ctx context.Context, messageID string, at time.Time) error {
	d.mu.Lock()
	if _, ok := d.items[messageID]; !ok {
		if d.lru.Len() >= d.maxSize {
			// Evict the LRU tail.
			back := d.lru.Back()
			if back != nil {
				item := back.Value.(*lruItem)
				delete(d.items, item.id)
				d.lru.Remove(back)
			}
		}
		el := d.lru.PushFront(&lruItem{id: messageID, seenAt: at})
		d.items[messageID] = el
	}
	d.mu.Unlock()

	return d.store.InsertSESSeen(ctx, messageID, at)
}

// GC removes entries older than ttl from the durable store.  Intended
// to be called periodically; errors are logged by the caller.
func (d *deduper) GC(ctx context.Context, now time.Time) error {
	return d.store.GCOldSESSeen(ctx, now.Add(-d.ttl))
}
