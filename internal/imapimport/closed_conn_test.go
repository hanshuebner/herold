package imapimport

// closed_conn_test.go covers the "use of closed network connection" transient
// error path (re #208). This error is the standard Go net-package error
// returned when a read/write happens on a connection that was already closed
// elsewhere (e.g. a secondary/write-back connection torn down concurrently
// with an in-flight command on the primary). It is not an unreachable-host or
// auth failure and must not be surfaced to the operator or counted toward the
// consecutive-failure limit that flips the account to errored.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// TestClassifyErrorKindClosedConn verifies that a "use of closed network
// connection" error (in either its read or write form) is classified as the
// dedicated "closed_conn" kind, distinct from the generic "network" bucket
// used for dial timeouts / connection refused / resets.
func TestClassifyErrorKindClosedConn(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("read tcp 127.0.0.1:52341->10.0.0.5:993: use of closed network connection"), "closed_conn"},
		{errors.New("write tcp 127.0.0.1:52341->10.0.0.5:993: use of closed network connection"), "closed_conn"},
		{errors.New("imapimport: syncAllFolders: use of closed network connection"), "closed_conn"},
	}
	for _, tc := range cases {
		if got := classifyErrorKind(tc.err); got != tc.want {
			t.Errorf("classifyErrorKind(%q) = %q; want %q", tc.err, got, tc.want)
		}
	}
}

// TestHandleAttemptFailureClosedConnNotSurfaced verifies that a "use of
// closed network connection" error, injected repeatedly (well past the
// configured consecutive-failure limit), is handled internally: the worker
// never stops, the consecutive-failure counter and last-error snapshot are
// left untouched, and the account row is never flipped to errored.
func TestHandleAttemptFailureClosedConnNotSurfaced(t *testing.T) {
	// maxFailures = 1 so that ANY accounting of this error would trip the
	// errored flip on the very first call — proving the error truly bypasses
	// the counter rather than merely being under some higher limit.
	w, ha, acc, _ := newRateLimitTestWorker(t, 1)
	// Swap in a FakeClock so the fixed reconnect delay between iterations
	// resolves instantly instead of sleeping in real time.
	fc := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w.opts.clk = fc
	ctx := context.Background()

	closedErr := errors.New("imapimport: syncAllFolders: read tcp 127.0.0.1:52341->10.0.0.5:993: use of closed network connection")

	const iterations = 5
	stops := make([]bool, iterations)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			stops[i] = w.handleAttemptFailure(ctx, closedErr)
		}
	}()
	// Pump the fake clock until all iterations have run their reconnect wait.
	for {
		select {
		case <-done:
			goto verify
		default:
			fc.Advance(closedConnRetryDelay + time.Second)
			time.Sleep(time.Millisecond)
		}
	}

verify:
	for i, stop := range stops {
		if stop {
			t.Fatalf("handleAttemptFailure returned stop=true on closed-conn error (iteration %d); want false (internally retried)", i)
		}
	}

	if w.consecutive != 0 {
		t.Errorf("consecutive = %d; want 0 (closed-conn error must not count toward the failure limit)", w.consecutive)
	}
	snap := w.status.snapshot()
	if snap.LastError != "" {
		t.Errorf("LastError = %q; want empty (closed-conn error must not surface to the operator)", snap.LastError)
	}
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d; want 0", snap.ConsecutiveFailures)
	}

	cur, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if cur.State == store.IMAPImportAccountStateErrored {
		t.Errorf("account state = %q; want NOT errored after repeated closed-conn errors", cur.State)
	}
}
