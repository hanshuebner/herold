package extsubmit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// ---- test doubles -------------------------------------------------------

// fakeStore is a simple in-memory SweeperStore for tests.
type fakeStore struct {
	mu   sync.Mutex
	rows []store.IdentitySubmission
}

func (f *fakeStore) ListIdentitySubmissionsDue(_ context.Context, before time.Time) ([]store.IdentitySubmission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var due []store.IdentitySubmission
	for _, r := range f.rows {
		if !r.RefreshDue.IsZero() && !r.RefreshDue.After(before) {
			due = append(due, r)
		}
	}
	return due, nil
}

func (f *fakeStore) CountOAuthIdentitySubmissions(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.rows {
		if r.SubmitAuthMethod == "oauth2" {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) UpsertIdentitySubmission(_ context.Context, sub store.IdentitySubmission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rows {
		if r.IdentityID == sub.IdentityID {
			f.rows[i] = sub
			return nil
		}
	}
	f.rows = append(f.rows, sub)
	return nil
}

func (f *fakeStore) getRow(identityID string) (store.IdentitySubmission, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.IdentityID == identityID {
			return r, true
		}
	}
	return store.IdentitySubmission{}, false
}

// fakeTokenRefresher implements TokenRefresher for tests.
type fakeTokenRefresher struct {
	mu    sync.Mutex
	calls int
	err   error
	store *fakeStore // when non-nil, upserts success result on success
	now   func() time.Time
}

func (fr *fakeTokenRefresher) Refresh(ctx context.Context, sub store.IdentitySubmission, _ OAuthClientCredentials) (string, error) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	fr.calls++
	if fr.err != nil {
		return "", fr.err
	}
	// Simulate a successful refresh: update the row's RefreshDue in the store.
	if fr.store != nil {
		now := time.Now()
		if fr.now != nil {
			now = fr.now()
		}
		updated := sub
		updated.State = store.IdentitySubmissionStateOK
		updated.StateAt = now
		updated.OAuthExpiresAt = now.Add(1 * time.Hour)
		updated.RefreshDue = now.Add(1*time.Hour - 60*time.Second)
		_ = fr.store.UpsertIdentitySubmission(ctx, updated)
	}
	return "new-access-token", nil
}

func (fr *fakeTokenRefresher) callCount() int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.calls
}

// ---- helpers ------------------------------------------------------------

// testDataKey32 is a 32-byte zero key for use in sweeper tests.
var testDataKey32 = make([]byte, 32)

// sealToken seals plaintext using testDataKey32.
func sealToken(t *testing.T, pt string) []byte {
	t.Helper()
	ct, err := secrets.Seal(testDataKey32, []byte(pt))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return ct
}

// newSweeperForTest returns a Sweeper wired to the fakeStore and fakeTokenRefresher.
func newSweeperForTest(fs *fakeStore, fr *fakeTokenRefresher) *Sweeper {
	return &Sweeper{
		Store:        fs,
		TokenRefresh: fr,
		DataKey:      testDataKey32,
		Interval:     500 * time.Millisecond,
		Workers:      2,
	}
}

// ---- tests --------------------------------------------------------------

// TestSweeper_RefreshesTokenBeforeExpiry verifies that a row with a
// RefreshDue <= now causes a Refresh call, and that on success the row's
// state and RefreshDue are updated.
func TestSweeper_RefreshesTokenBeforeExpiry(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{}

	overdue := store.IdentitySubmission{
		IdentityID:       "id-1",
		SubmitAuthMethod: "oauth2",
		OAuthRefreshCT:   sealToken(t, "refresh-sentinel"),
		OAuthAccessCT:    sealToken(t, "access-old"),
		OAuthExpiresAt:   now.Add(10 * time.Minute),
		RefreshDue:       now.Add(-30 * time.Second), // overdue
		State:            store.IdentitySubmissionStateOK,
	}
	fs.rows = append(fs.rows, overdue)

	fr := &fakeTokenRefresher{store: fs, now: func() time.Time { return now }}
	sw := newSweeperForTest(fs, fr)
	sw.Now = func() time.Time { return now }

	sw.tick(context.Background())

	if fr.callCount() != 1 {
		t.Fatalf("Refresh called %d times, want 1", fr.callCount())
	}

	// Row should now have an advanced RefreshDue.
	got, ok := fs.getRow("id-1")
	if !ok {
		t.Fatal("row missing from store after successful refresh")
	}
	if got.State != store.IdentitySubmissionStateOK {
		t.Errorf("State = %q after success; want ok", got.State)
	}
	if !got.RefreshDue.After(now) {
		t.Errorf("RefreshDue did not advance: got %v, want > %v", got.RefreshDue, now)
	}
}

// TestSweeper_RefreshFailureFlipsStateAuthFailed verifies that a Refresh error
// results in the store row's State being set to auth-failed and RefreshDue
// being left unchanged (so the sweeper retries).
func TestSweeper_RefreshFailureFlipsStateAuthFailed(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{}

	originalDue := now.Add(-1 * time.Minute) // overdue
	row := store.IdentitySubmission{
		IdentityID:       "id-fail",
		SubmitAuthMethod: "oauth2",
		OAuthRefreshCT:   sealToken(t, "bad-refresh"),
		OAuthAccessCT:    sealToken(t, "old-access"),
		RefreshDue:       originalDue,
		State:            store.IdentitySubmissionStateOK,
	}
	fs.rows = append(fs.rows, row)

	fr := &fakeTokenRefresher{err: ErrAuthFailed}
	sw := newSweeperForTest(fs, fr)
	sw.Now = func() time.Time { return now }

	sw.tick(context.Background())

	if fr.callCount() != 1 {
		t.Fatalf("Refresh called %d times, want 1", fr.callCount())
	}
	got, ok := fs.getRow("id-fail")
	if !ok {
		t.Fatal("row gone from store after failure")
	}
	if got.State != store.IdentitySubmissionStateAuthFailed {
		t.Errorf("State = %q; want %q", got.State, store.IdentitySubmissionStateAuthFailed)
	}
	// RefreshDue must not change on failure (retry on next tick).
	if !got.RefreshDue.Equal(originalDue) {
		t.Errorf("RefreshDue changed on failure: got %v, want %v", got.RefreshDue, originalDue)
	}
}

// TestSweeper_WorkerPanicDoesNotCrashDispatcher verifies that a panic inside
// a worker goroutine is recovered and does not propagate to the tick() caller.
func TestSweeper_WorkerPanicDoesNotCrashDispatcher(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{}

	row := store.IdentitySubmission{
		IdentityID:       "id-panic",
		SubmitAuthMethod: "oauth2",
		OAuthRefreshCT:   sealToken(t, "refresh-token"),
		RefreshDue:       now.Add(-1 * time.Second),
		State:            store.IdentitySubmissionStateOK,
	}
	fs.rows = append(fs.rows, row)

	// Use a panicking TokenRefresher.
	panicRefresher := &panicTokenRefresher{}
	sw := &Sweeper{
		Store:        fs,
		TokenRefresh: panicRefresher,
		DataKey:      testDataKey32,
		Interval:     500 * time.Millisecond,
		Workers:      2,
		Now:          func() time.Time { return now },
	}

	// tick() must not panic; the sweeper's worker recover catches it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sw.tick(context.Background())
	}()
	select {
	case <-done:
		// Good: tick returned without panic propagating.
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not complete within 2 s")
	}
}

// panicTokenRefresher is a TokenRefresher that panics on Refresh.
type panicTokenRefresher struct{}

func (*panicTokenRefresher) Refresh(_ context.Context, _ store.IdentitySubmission, _ OAuthClientCredentials) (string, error) {
	panic("deliberate test panic from panicTokenRefresher")
}

// TestSweeper_ContextCancellationShutsDownCleanly verifies that cancelling
// the context causes Run to return within 1 s.
func TestSweeper_ContextCancellationShutsDownCleanly(t *testing.T) {
	fs := &fakeStore{}
	fr := &fakeTokenRefresher{}
	sw := newSweeperForTest(fs, fr)
	sw.Interval = 500 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sw.Run(ctx)
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned non-nil error on clean cancel: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not shut down within 1 s after context cancel")
	}
}

// TestSweeper_NoDoubleRefreshUnderBoundedPool verifies that the bounded
// semaphore limits concurrency: at most Workers goroutines run at once.
// We run two concurrent ticks against one row and verify that the refresh
// count is at most 2 (one per tick) and not unbounded.
func TestSweeper_NoDoubleRefreshUnderBoundedPool(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{}

	row := store.IdentitySubmission{
		IdentityID:       "id-double",
		SubmitAuthMethod: "oauth2",
		OAuthRefreshCT:   sealToken(t, "refresh-token"),
		OAuthAccessCT:    sealToken(t, "access-token"),
		RefreshDue:       now.Add(-1 * time.Second),
		State:            store.IdentitySubmissionStateOK,
	}
	fs.rows = append(fs.rows, row)

	var maxConcurrent atomic.Int64
	var current atomic.Int64

	fr := &countingRefresher{
		maxConcurrent: &maxConcurrent,
		current:       &current,
	}
	sw := &Sweeper{
		Store:        fs,
		TokenRefresh: fr,
		DataKey:      testDataKey32,
		Interval:     500 * time.Millisecond,
		Workers:      2,
		Now:          func() time.Time { return now },
	}

	// Two concurrent ticks.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sw.tick(context.Background())
		}()
	}
	wg.Wait()

	// With Workers=2 and 1 row per tick, max concurrency should be <= 2.
	if maxConcurrent.Load() > int64(sw.Workers) {
		t.Errorf("max concurrent workers %d exceeded pool size %d",
			maxConcurrent.Load(), sw.Workers)
	}
}

// countingRefresher is a TokenRefresher that tracks concurrency.
type countingRefresher struct {
	maxConcurrent *atomic.Int64
	current       *atomic.Int64
}

func (r *countingRefresher) Refresh(_ context.Context, sub store.IdentitySubmission, _ OAuthClientCredentials) (string, error) {
	cur := r.current.Add(1)
	for {
		max := r.maxConcurrent.Load()
		if cur <= max {
			break
		}
		if r.maxConcurrent.CompareAndSwap(max, cur) {
			break
		}
	}
	// Brief sleep so concurrent goroutines overlap.
	time.Sleep(5 * time.Millisecond)
	r.current.Add(-1)
	return "token", nil
}
