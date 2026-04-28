package categorise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultRecategoriseLimit caps the per-job message count when the
// caller does not pass an explicit limit.
const DefaultRecategoriseLimit = 1000

// JobState is the public lifecycle marker for a recategorisation job.
type JobState string

// JobState values.
const (
	// JobStateRunning marks an in-flight job whose progress callback
	// is still being invoked.
	JobStateRunning JobState = "running"
	// JobStateDone marks a finished job; Done == Total at the time of
	// the last progress callback.
	JobStateDone JobState = "done"
	// JobStateFailed marks a job that aborted with an error before
	// completion. Err carries the diagnostic.
	JobStateFailed JobState = "failed"
)

// JobStatus is the wire-form snapshot the admin REST surface returns
// for a polled job.
type JobStatus struct {
	ID    string   `json:"id"`
	State JobState `json:"state"`
	Done  int      `json:"done"`
	Total int      `json:"total"`
	Err   string   `json:"err,omitempty"`
}

// jobEntry is the in-memory tracking row.
type jobEntry struct {
	status    JobStatus
	updatedAt time.Time
}

// JobRegistry is a small in-process map of {jobID -> JobStatus} with
// time-based eviction. Suitable for a single server instance; cluster
// deployments would back this with the durable store. Safe for
// concurrent use.
type JobRegistry struct {
	mu      sync.Mutex
	jobs    map[string]*jobEntry
	maxAge  time.Duration
	maxSize int
}

// NewJobRegistry returns a JobRegistry with the given retention
// window. maxAge defaults to 24h; maxSize defaults to 256.
func NewJobRegistry(maxAge time.Duration, maxSize int) *JobRegistry {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	if maxSize <= 0 {
		maxSize = 256
	}
	return &JobRegistry{
		jobs:    map[string]*jobEntry{},
		maxAge:  maxAge,
		maxSize: maxSize,
	}
}

// Put records a job snapshot. Older entries beyond maxAge are evicted
// at every Put so the map size stays bounded; if the new insert
// pushes the map past maxSize, the oldest entries are dropped to
// keep len(r.jobs) <= maxSize after the call returns.
func (r *JobRegistry) Put(now time.Time, s JobStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[s.ID] = &jobEntry{status: s, updatedAt: now}
	r.evictLocked(now)
}

// Get returns the snapshot for id, or false if no such job is on
// record (either expired or never registered).
func (r *JobRegistry) Get(id string) (JobStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.jobs[id]
	if !ok {
		return JobStatus{}, false
	}
	return e.status, true
}

// evictLocked drops entries whose updatedAt is older than maxAge or,
// when the map is over maxSize, drops the oldest entries to make room.
// Caller holds r.mu.
func (r *JobRegistry) evictLocked(now time.Time) {
	cutoff := now.Add(-r.maxAge)
	for id, e := range r.jobs {
		if e.updatedAt.Before(cutoff) {
			delete(r.jobs, id)
		}
	}
	if len(r.jobs) <= r.maxSize {
		return
	}
	// Drop the oldest until we are at maxSize. Linear scan; this only
	// runs when an operator has spammed the endpoint and the size
	// (default 256) is small enough that O(n) is fine.
	for len(r.jobs) > r.maxSize {
		var oldestID string
		var oldestAt time.Time
		first := true
		for id, e := range r.jobs {
			if first || e.updatedAt.Before(oldestAt) {
				oldestID = id
				oldestAt = e.updatedAt
				first = false
			}
		}
		if oldestID == "" {
			break
		}
		delete(r.jobs, oldestID)
	}
}

// ProgressFunc is the callback shape RecategoriseRecent invokes after
// each message: it receives the running done/total counters so the
// caller can mirror them onto a JobRegistry or log line.
type ProgressFunc func(done, total int)

// RecategoriseRecent re-runs the classifier on the most recent
// `limit` messages in the principal's INBOX (REQ-FILT-220). The
// caller drives concurrency: this method is synchronous; callers
// wanting an async job spawn a goroutine and feed progress through a
// JobRegistry.
//
// Returns the count of messages actually processed (== len(targets))
// even when individual classification calls fail; failures NEVER
// abort the loop because REQ-FILT-230 says categorisation is
// best-effort. The error return carries store-layer failures (mailbox
// lookup, message read) that prevent the loop from running at all.
func (c *Categoriser) RecategoriseRecent(
	ctx context.Context,
	principal store.PrincipalID,
	limit int,
	progress ProgressFunc,
) (int, error) {
	if c == nil {
		return 0, errors.New("categorise: nil Categoriser")
	}
	if limit <= 0 {
		limit = DefaultRecategoriseLimit
	}
	inbox, err := c.store.Meta().GetMailboxByName(ctx, principal, "INBOX")
	if err != nil {
		return 0, fmt.Errorf("locate INBOX: %w", err)
	}
	// ListMessages returns ascending UID order; we paginate to the
	// tail then reverse to get the newest-first slice the operator
	// expects to land in.
	msgs, err := c.store.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{
		Limit:        limit,
		WithEnvelope: true,
	})
	if err != nil {
		return 0, fmt.Errorf("list messages: %w", err)
	}
	total := len(msgs)
	for i, m := range msgs {
		if err := ctx.Err(); err != nil {
			return i, err
		}
		// Re-parse the stored blob; the categoriser needs the body
		// excerpt and headers, neither of which the Message row
		// carries directly.
		parsed, perr := c.loadAndParse(ctx, m)
		if perr != nil {
			c.logger.WarnContext(ctx, "recategorise: parse failed",
				slog.Uint64("message_id", uint64(m.ID)),
				slog.String("err", perr.Error()))
			if progress != nil {
				progress(i+1, total)
			}
			continue
		}
		newCat, _ := c.Categorise(ctx, principal, parsed, nil, spam.Ham)
		if err := c.applyCategoryKeyword(ctx, m, newCat); err != nil {
			c.logger.WarnContext(ctx, "recategorise: apply keyword failed",
				slog.Uint64("message_id", uint64(m.ID)),
				slog.String("err", err.Error()))
		}
		if progress != nil {
			progress(i+1, total)
		}
	}
	return total, nil
}

// loadAndParse fetches the message body from the blob store and
// parses it. We only need a Message struct; the cap on body size is
// the categoriser's own DefaultBodyExcerptBytes via collectTextBody.
func (c *Categoriser) loadAndParse(ctx context.Context, m store.Message) (mailparse.Message, error) {
	rc, err := c.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		return mailparse.Message{}, fmt.Errorf("get blob: %w", err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return mailparse.Message{}, fmt.Errorf("read blob: %w", err)
	}
	parsed, err := mailparse.Parse(&buf, mailparse.NewParseOptions())
	if err != nil {
		return mailparse.Message{}, fmt.Errorf("parse: %w", err)
	}
	return parsed, nil
}

// applyCategoryKeyword reconciles the desired category against the
// keywords on the message: clears any existing $category-* keywords
// and adds the new one (if any). Idempotent: setting the same
// category twice produces no extra updates.
func (c *Categoriser) applyCategoryKeyword(ctx context.Context, m store.Message, newCat string) error {
	var clear []string
	var have string
	for _, k := range m.Keywords {
		if strings.HasPrefix(k, CategoryKeywordPrefix) {
			if have == "" {
				have = strings.TrimPrefix(k, CategoryKeywordPrefix)
			}
			clear = append(clear, k)
		}
	}
	desired := ""
	if newCat != "" {
		desired = CategoryKeywordPrefix + newCat
	}
	if have == newCat {
		return nil
	}
	var add []string
	if desired != "" {
		add = []string{desired}
		// Do not clear the desired keyword if it already happens to
		// be present (shouldn't due to dedup above, but defensive).
		filtered := make([]string, 0, len(clear))
		for _, k := range clear {
			if k != desired {
				filtered = append(filtered, k)
			}
		}
		clear = filtered
	}
	_, err := c.store.Meta().UpdateMessageFlags(ctx, m.ID, m.MailboxID, 0, 0, add, clear, 0)
	return err
}

// CategoryKeywordPrefix is the wire-form prefix the categoriser
// applies to the picked category name (REQ-FILT-201). Exported so the
// delivery glue and admin tooling can build/parse keywords without
// hardcoding the string.
const CategoryKeywordPrefix = "$category-"

// CategoryFromKeywords returns the bare category name carried by the
// first "$category-*" keyword in keys, or "" when none is present.
// Helper for admin tooling and tests.
func CategoryFromKeywords(keys []string) string {
	for _, k := range keys {
		if strings.HasPrefix(k, CategoryKeywordPrefix) {
			return strings.TrimPrefix(k, CategoryKeywordPrefix)
		}
	}
	return ""
}

// _ keeps the mailauth import alive even when callers do not pass an
// AuthResults pointer through Categorise. Keeping it referenced here
// reminds the next reader why the symbol exists in the signature.
var _ = (*mailauth.AuthResults)(nil)
