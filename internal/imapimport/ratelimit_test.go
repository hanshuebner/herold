package imapimport

// ratelimit_test.go covers the rate-limit-specific second-connection fallback
// and backoff-exponent bump (REQ-IMAP-IMP-21/73). When the upstream rejects an
// extra connection with a too-many-connections / rate-limit response the worker
// forces single-connection mode for its lifetime and raises the backoff
// exponent; repeated rate-limiting still flips the account to errored.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// newRateLimitTestWorker builds a minimal worker against a real in-process
// server so handleAttemptFailure / status helpers operate normally. The worker
// dials through a fakeDialer pointing at the returned test server; tests that
// need a custom dialer replace w.opts.dialer.
func newRateLimitTestWorker(t *testing.T, maxFailures int) (*accountWorker, *testharness.Server, store.IMAPImportAccount, *testIMAPServer) {
	t.Helper()
	ts := startTestIMAPServer(t)
	ts.addUser("rl", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "rl@example.test",
		username:            "rl",
		credentialPlaintext: "pw",
	}, nil)
	w := newAccountWorker(accountWorkerOpts{
		account:                acc,
		store:                  ha.Store,
		dataKey:                testDataKey(t),
		cfg:                    sysconfig.IMAPImportConfig{},
		log:                    newTestLogger(t),
		clk:                    clock.NewReal(),
		dialer:                 &fakeDialer{ts: ts},
		categoriser:            noopCategoriser{},
		maxConsecutiveFailures: maxFailures,
	})
	return w, ha, acc, ts
}

// TestBackoffDurationRateLimitShift verifies the extra rate-limit exponent
// materially raises the backoff delay over the plain consecutive-failure delay
// (REQ-IMAP-IMP-73).
func TestBackoffDurationRateLimitShift(t *testing.T) {
	// With shift 0 the first failure backs off near backoffBase (2s, ±25%).
	base := backoffDurationShift(1, 0)
	if base > 3*time.Second {
		t.Errorf("base backoff = %v; want roughly %v", base, backoffBase)
	}
	// With a +4 exponent the first failure backs off near 32s (2s<<4), so well
	// above 16s even after the -25% jitter floor.
	shifted := backoffDurationShift(1, 4)
	if shifted < 16*time.Second {
		t.Errorf("shifted backoff = %v; want >= 16s (raised exponent)", shifted)
	}
	if shifted > backoffCap+backoffBase {
		t.Errorf("shifted backoff = %v; want <= cap %v", shifted, backoffCap+backoffBase)
	}
	// A negative shift is clamped to zero (no panic / underflow).
	if got := backoffDurationShift(1, -3); got <= 0 {
		t.Errorf("negative shift backoff = %v; want > 0", got)
	}
}

// TestNoteSecondaryConnErrorRateLimit verifies that a rate-limit second/
// write-back connection failure forces single-connection mode and accumulates
// the backoff exponent, while a plain failure does neither (REQ-IMAP-IMP-21/73).
func TestNoteSecondaryConnErrorRateLimit(t *testing.T) {
	w, _, _, _ := newRateLimitTestWorker(t, 0)

	// A non-rate-limit error (e.g. a server that simply caps at one connection)
	// leaves forceSingleConn / shift untouched — runIDLELoop still falls back to
	// single mode, but it is not a throttling signal.
	w.noteSecondaryConnError(errors.New("connection refused"))
	if w.forceSingleConn || w.rateLimitBackoffShift != 0 {
		t.Fatalf("non-rate-limit error mutated state: forceSingle=%v shift=%d",
			w.forceSingleConn, w.rateLimitBackoffShift)
	}

	// A rate-limit error forces single-conn mode and bumps the exponent.
	w.noteSecondaryConnError(errors.New("[NO] Too many simultaneous connections"))
	if !w.forceSingleConn {
		t.Error("forceSingleConn = false; want true after rate-limit error")
	}
	if w.rateLimitBackoffShift != 1 {
		t.Errorf("rateLimitBackoffShift = %d; want 1", w.rateLimitBackoffShift)
	}

	// Repeated rate-limiting keeps accumulating but is capped.
	for i := 0; i < maxRateLimitBackoffShift+5; i++ {
		w.noteSecondaryConnError(errors.New("rate limited"))
	}
	if w.rateLimitBackoffShift != maxRateLimitBackoffShift {
		t.Errorf("rateLimitBackoffShift = %d; want cap %d",
			w.rateLimitBackoffShift, maxRateLimitBackoffShift)
	}
}

// TestRateLimitFlipsToErrored verifies that a rate-limit connection failure at
// the consecutive-failure limit forces single-conn mode, raises the backoff
// exponent, and flips the account to errored (REQ-IMAP-IMP-73 + REQ-IMAP-IMP-25).
func TestRateLimitFlipsToErrored(t *testing.T) {
	// maxFailures = 1 so a single failure trips the errored flip, which returns
	// before the backoff sleep — keeping the test deterministic and fast.
	w, ha, acc, _ := newRateLimitTestWorker(t, 1)
	ctx := context.Background()

	rlErr := errors.New("imapimport: dial failed: [NO] Too many connections, rate-limited")
	stop := w.handleAttemptFailure(ctx, rlErr)
	if !stop {
		t.Fatal("handleAttemptFailure returned stop=false; want true at the failure limit")
	}
	if !w.forceSingleConn {
		t.Error("forceSingleConn = false; want true after a rate-limit failure")
	}
	if w.rateLimitBackoffShift != 1 {
		t.Errorf("rateLimitBackoffShift = %d; want 1", w.rateLimitBackoffShift)
	}
	cur, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if cur.State != store.IMAPImportAccountStateErrored {
		t.Errorf("account state = %q; want %q", cur.State, store.IMAPImportAccountStateErrored)
	}
}

// TestForceSingleConnSkipsExtraConnections verifies that once forceSingleConn
// is set the worker opens only the primary connection — neither the second
// sync connection nor the write-back connection — and reports single-conn mode
// in its live status (REQ-IMAP-IMP-21/73).
func TestForceSingleConnSkipsExtraConnections(t *testing.T) {
	w, _, _, ts := newRateLimitTestWorker(t, 0)
	// Wrap the fake dialer in a counter so we can assert no extra connections
	// are opened once single-conn mode is forced.
	cd := &countingDialer{inner: &fakeDialer{ts: ts}}
	w.opts.dialer = cd
	w.forceSingleConn = true

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.attempt(ctx) }()

	// Poll for the worker to settle into single-conn mode in the running loop;
	// read ConnMode while still connected (it is cleared on disconnect).
	var connMode string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if connMode = w.status.snapshot().ConnMode; connMode != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if opened, _ := cd.counts(); opened != 1 {
		t.Errorf("Dial calls = %d; want 1 (primary only — no second or write-back conn)", opened)
	}
	if connMode != "single" {
		t.Errorf("ConnMode = %q; want \"single\"", connMode)
	}
}
