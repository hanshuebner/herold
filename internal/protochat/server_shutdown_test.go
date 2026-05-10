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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.srv.Shutdown(ctx)
	}()

	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown returned DeadlineExceeded after %v: drain stalled on blocked readPump", elapsed)
		}
		// nil or any non-DeadlineExceeded error is acceptable; the
		// regression we guard against is the drain timing out, not
		// the precise wall-clock latency. The 3s ctx deadline above
		// is the only meaningful gate: under the fix the typical
		// elapsed is ~1 ms and under the bug it would be 3 s. No
		// finer ceiling is asserted because under -race CPU
		// contention the goroutine-scheduling chain
		// (Shutdown -> shutdown(cc) -> SetReadDeadline ->
		// readFrame wakeup -> connWG.Done) accumulates dozens of ms
		// without a real regression.
		_ = elapsed
	case <-time.After(2500 * time.Millisecond):
		t.Fatalf("Shutdown did not return within 2.5s; readPump likely still blocked")
	}
}
