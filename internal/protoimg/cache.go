package protoimg

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// cacheEntry is the value stored under a URL hash. The bytes are owned
// by the cache: the handler copies them into a fresh slice on hit
// before serving so a concurrent eviction can never tear a response.
type cacheEntry struct {
	key          string
	bytes        []byte
	contentType  string
	etag         string
	lastModified time.Time
	expiresAt    time.Time
}

// imageCache is a dual-budget LRU. Entries are evicted whenever either
// the entry count exceeds maxEntries or the total byte footprint
// exceeds maxBytes.
//
// The implementation is the textbook (map + doubly-linked list) LRU.
// We keep the Mutex (not RWMutex) — every read mutates the recency
// list, so the writer-side lock is unavoidable on the hot path.
type imageCache struct {
	maxEntries int
	maxBytes   int64

	mu         sync.Mutex
	totalBytes int64
	entries    map[string]*list.Element // value type: *cacheEntry
	order      *list.List               // most-recent at Front
}

// newImageCache constructs an empty cache with the given budgets. A
// non-positive budget disables the corresponding limit; tests may run
// with a tiny entry budget to exercise eviction without huge fixtures.
func newImageCache(maxEntries int, maxBytes int64) *imageCache {
	return &imageCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

// hashURL returns the cache key for a URL. We hash so the in-memory
// map carries fixed-size keys regardless of upstream URL length and so
// a debug log of a key does not reveal the URL verbatim.
func hashURL(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// get returns the cached entry under key and whether it is still
// fresh per the supplied wall-clock now. An expired entry is left in
// place — the next put or sweep will evict it — because removing it
// here would force the lookup hot path to take a write-style step on
// every miss-by-expiry.
func (c *imageCache) get(key string, now time.Time) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	ce := el.Value.(*cacheEntry)
	if !now.Before(ce.expiresAt) {
		// Stale: evict so a subsequent miss does not log "hit but expired".
		c.order.Remove(el)
		delete(c.entries, key)
		c.totalBytes -= int64(len(ce.bytes))
		return cacheEntry{}, false
	}
	c.order.MoveToFront(el)
	// Return a shallow copy with a defensive copy of the bytes so the
	// caller cannot mutate cache state by writing to the slice.
	out := *ce
	out.bytes = append([]byte(nil), ce.bytes...)
	return out, true
}

// put inserts (or replaces) a cache entry, then evicts LRU entries
// until both budgets are satisfied. An entry larger than the byte
// budget is dropped immediately — caching a single oversized blob
// would keep evicting every neighbour on every insert.
func (c *imageCache) put(ce cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	size := int64(len(ce.bytes))
	if c.maxBytes > 0 && size > c.maxBytes {
		return
	}
	if existing, ok := c.entries[ce.key]; ok {
		old := existing.Value.(*cacheEntry)
		c.totalBytes -= int64(len(old.bytes))
		c.order.Remove(existing)
		delete(c.entries, ce.key)
	}
	stored := ce
	stored.bytes = append([]byte(nil), ce.bytes...)
	el := c.order.PushFront(&stored)
	c.entries[ce.key] = el
	c.totalBytes += size
	c.evictLocked()
}

// evictLocked drops least-recent entries until both budgets pass.
// Caller holds c.mu.
func (c *imageCache) evictLocked() {
	for {
		overCount := c.maxEntries > 0 && len(c.entries) > c.maxEntries
		overBytes := c.maxBytes > 0 && c.totalBytes > c.maxBytes
		if !overCount && !overBytes {
			return
		}
		back := c.order.Back()
		if back == nil {
			return
		}
		ce := back.Value.(*cacheEntry)
		c.order.Remove(back)
		delete(c.entries, ce.key)
		c.totalBytes -= int64(len(ce.bytes))
	}
}

// len returns the entry count. Test-only; the production path never
// inspects the cache size.
func (c *imageCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
