package protochat

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestShutdown_DrainsBlockedReadPump exercises the regression where a
// silent client (no peer FIN, no application-layer activity) pinned
// chatConn.readPump inside readFrame past the operator's shutdown
// drain window. The fix nudges the in-flight readFrame call by
// shortening the read deadline inside chatConn.shutdown's closeOnce,
// so connWG.Wait() in Server.Shutdown can complete promptly.
//
// Without the fix, Shutdown returns context.DeadlineExceeded; with
// the fix, it returns nil well under the supplied deadline.
func TestShutdown_DrainsBlockedReadPump(t *testing.T) {
	h := newHarness(t)

	// Establish a real WebSocket connection. The test client
	// deliberately performs no further reads / writes, so the
	// server-side readPump is parked inside readFrame until either
	// the wire activity wakes it or shutdown nudges the deadline.
	c := h.connect(1)
	_ = c // keep the conn open; Cleanup closes it at test exit.

	// Wait briefly until the connection is registered on the server
	// so Shutdown's snapshot covers it. Without this we could race
	// past the connsMu.Lock snapshot and witness an empty set.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.srv.connsMu.Lock()
		n := len(h.srv.conns)
		h.srv.connsMu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection did not register on server")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Shutdown's ctx deadline is the meaningful gate for the regression:
	// under the bug it sits on connWG.Wait() until the ctx fires. Set
	// it to 3 s so the bug surfaces as DeadlineExceeded with a clear
	// elapsed≈3 s; the fix exits in well under 100 ms.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.srv.Shutdown(ctx)
	}()

	// The select-after deadline must be strictly larger than the
	// Shutdown ctx deadline so a slow-but-correct drain cannot trip the
	// wrong branch. chatConn.shutdown writes a close frame with a 2 s
	// write deadline, and under -race on CI runner contention the
	// goroutine-scheduling chain (Shutdown -> shutdown(cc) ->
	// SetReadDeadline -> readFrame wakeup -> wg.Wait -> connWG.Done)
	// accumulates dozens to a couple hundred ms on top. 6 s gives
	// generous headroom above the 3 s ctx without papering over a real
	// regression (CI run 26324597014 surfaced the prior 2.5 s ceiling
	// as a margin flake).
	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown returned DeadlineExceeded after %v: drain stalled on blocked readPump", elapsed)
		}
		_ = elapsed
	case <-time.After(6 * time.Second):
		t.Fatalf("Shutdown did not return within 6s; readPump likely still blocked")
	}
}
