package extsubmit

import (
	"context"
	"fmt"
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

func (f *fakeStore) GetIdentitySubmission(_ context.Context, identityID string) (store.IdentitySubmission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.IdentityID == identityID {
			return r, nil
		}
	}
	return store.IdentitySubmission{}, store.ErrNotFound
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

// TestClassifyRefreshError_DecryptFailure verifies that classifyRefreshError
// returns "decrypt" when the error wraps secrets.ErrBadCiphertext (re #156).
func TestClassifyRefreshError_DecryptFailure(t *testing.T) {
	// Direct ErrBadCiphertext.
	got := classifyRefreshError(secrets.ErrBadCiphertext)
	if got != "decrypt" {
		t.Errorf("classifyRefreshError(ErrBadCiphertext) = %q; want %q", got, "decrypt")
	}

	// Wrapped, as returned by Refresher.Refresh: "extsubmit: open refresh token: secrets: ..."
	wrapped := fmt.Errorf("extsubmit: open refresh token: %w", secrets.ErrBadCiphertext)
	got = classifyRefreshError(wrapped)
	if got != "decrypt" {
		t.Errorf("classifyRefreshError(wrapped ErrBadCiphertext) = %q; want %q", got, "decrypt")
	}
}

// TestSweeper_UndecryptableRefreshToken_ClearsRefreshDue verifies that when
// the sweeper cannot decrypt the stored OAuth refresh token (e.g. after key
// rotation), it writes state=auth-failed AND clears RefreshDue so the row no
// longer re-enters ListIdentitySubmissionsDue on subsequent ticks (re #156:
// infinite 1/min retry loop).
func TestSweeper_UndecryptableRefreshToken_ClearsRefreshDue(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{}

	originalDue := now.Add(-1 * time.Minute) // overdue
	row := store.IdentitySubmission{
		IdentityID:       "id-undecryptable",
		SubmitAuthMethod: "oauth2",
		OAuthRefreshCT:   sealToken(t, "refresh-token"),
		OAuthAccessCT:    sealToken(t, "access-token"),
		RefreshDue:       originalDue,
		State:            store.IdentitySubmissionStateOK,
	}
	fs.rows = append(fs.rows, row)

	// Simulate the error Refresher.Refresh returns when secrets.Open fails on
	// the refresh token ciphertext (ErrBadCiphertext wrapped with context).
	decryptErr := fmt.Errorf("extsubmit: open refresh token: %w", secrets.ErrBadCiphertext)
	fr := &fakeTokenRefresher{err: decryptErr}
	sw := newSweeperForTest(fs, fr)
	sw.Now = func() time.Time { return now }

	sw.tick(context.Background())

	if fr.callCount() != 1 {
		t.Fatalf("Refresh called %d times on first tick; want 1", fr.callCount())
	}

	got, ok := fs.getRow("id-undecryptable")
	if !ok {
		t.Fatal("row gone from store after first tick")
	}
	if got.State != store.IdentitySubmissionStateAuthFailed {
		t.Errorf("State = %q after decrypt failure; want auth-failed", got.State)
	}
	// RefreshDue must be cleared so the row does not re-enter ListIdentitySubmissionsDue.
	if !got.RefreshDue.IsZero() {
		t.Errorf("RefreshDue = %v after decrypt failure; want zero (must not reappear in due list)", got.RefreshDue)
	}

	// A second tick must not call Refresh again — the row is no longer due.
	sw.tick(context.Background())
	if fr.callCount() != 1 {
		t.Errorf("Refresh called %d times total; want 1 (row must not reappear after RefreshDue cleared)", fr.callCount())
	}
}

// TestSweeper_UndecryptableClientSecret_ClearsRefreshDue verifies that when
// the sweeper cannot decrypt OAuthClientSecretCT, it writes state=auth-failed
// AND clears RefreshDue so the row does not loop (re #156).
func TestSweeper_UndecryptableClientSecret_ClearsRefreshDue(t *testing.T) {
	now := time.Now()
	fs := &fakeStore{}

	// Seal the client secret with a different key so secrets.Open with
	// testDataKey32 returns ErrBadCiphertext (simulates key rotation / corruption).
	wrongKey := make([]byte, 32)
	for i := range wrongKey {
		wrongKey[i] = 0xFF
	}
	wrongCT, err := secrets.Seal(wrongKey, []byte("client-secret"))
	if err != nil {
		t.Fatalf("seal with wrong key: %v", err)
	}

	originalDue := now.Add(-1 * time.Minute)
	row := store.IdentitySubmission{
		IdentityID:          "id-bad-client-secret",
		SubmitAuthMethod:    "oauth2",
		OAuthRefreshCT:      sealToken(t, "refresh-token"),
		OAuthAccessCT:       sealToken(t, "access-token"),
		OAuthClientSecretCT: wrongCT, // undecryptable with testDataKey32
		RefreshDue:          originalDue,
		State:               store.IdentitySubmissionStateOK,
	}
	fs.rows = append(fs.rows, row)

	fr := &fakeTokenRefresher{} // should never be called
	sw := newSweeperForTest(fs, fr)
	sw.Now = func() time.Time { return now }

	sw.tick(context.Background())

	// Refresh must not have been called (sweeper returns before calling TokenRefresh).
	if fr.callCount() != 0 {
		t.Errorf("Refresh called %d times; want 0 (should bail on client secret decrypt failure)", fr.callCount())
	}

	got, ok := fs.getRow("id-bad-client-secret")
	if !ok {
		t.Fatal("row gone from store after first tick")
	}
	if got.State != store.IdentitySubmissionStateAuthFailed {
		t.Errorf("State = %q after client-secret decrypt failure; want auth-failed", got.State)
	}
	if !got.RefreshDue.IsZero() {
		t.Errorf("RefreshDue = %v; want zero (must not reappear in due list)", got.RefreshDue)
	}

	// A second tick must not call Refresh again.
	sw.tick(context.Background())
	if fr.callCount() != 0 {
		t.Errorf("Refresh called after row should have been cleared; want 0")
	}
}

// TestSweeper_DoesNotOverwriteFresherOKStateOnRefreshFailure verifies the fix
// for the race condition described in issue #131: when the sweeper worker
// holds a stale row (state captured before OAuth re-auth) and the refresh
// fails (old refresh token revoked), it must not overwrite a fresher state=ok
// row that handleOAuthCallback wrote to the store while the worker was running.
func TestSweeper_DoesNotOverwriteFresherOKStateOnRefreshFailure(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Minute) // OAuth callback wrote ok at t1

	fs := &fakeStore{}

	// Stale row captured by the sweeper at t0 (state=auth-failed, stale tokens).
	staleRow := store.IdentitySubmission{
		IdentityID:       "id-race",
		SubmitAuthMethod: "oauth2",
		OAuthRefreshCT:   sealToken(t, "old-revoked-refresh"),
		OAuthAccessCT:    sealToken(t, "old-access"),
		RefreshDue:       t0.Add(-1 * time.Minute),
		State:            store.IdentitySubmissionStateAuthFailed,
		StateAt:          t0,
	}

	// Current row in the store already has ok state written by the OAuth callback.
	fresherRow := staleRow
	fresherRow.OAuthRefreshCT = sealToken(t, "new-refresh")
	fresherRow.OAuthAccessCT = sealToken(t, "new-access")
	fresherRow.State = store.IdentitySubmissionStateOK
	fresherRow.StateAt = t1
	fs.rows = append(fs.rows, fresherRow)

	// Refresh fails (old token was revoked).
	fr := &fakeTokenRefresher{err: ErrAuthFailed}
	sw := &Sweeper{
		Store:        fs,
		TokenRefresh: fr,
		DataKey:      testDataKey32,
		Workers:      1,
		Now:          func() time.Time { return t1 },
	}

	// Invoke refreshRow directly with the stale row captured at t0.
	sw.refreshRow(context.Background(), staleRow)

	// The store row must still be state=ok (the fresher state from the callback
	// must not have been overwritten by the sweeper's auth-failed write).
	got, ok := fs.getRow("id-race")
	if !ok {
		t.Fatal("row missing from store")
	}
	if got.State != store.IdentitySubmissionStateOK {
		t.Errorf("State = %q after race; want ok (sweeper must not overwrite fresher callback state)", got.State)
	}
}
