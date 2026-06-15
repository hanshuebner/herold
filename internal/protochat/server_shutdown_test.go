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
	// under the bug it sits on connWG.Wait() until the ctx fires. The
	// fix exits in well under 100 ms, while the bug pins the drain at the
	// full ctx deadline -- so a generous deadline still surfaces the
	// regression cleanly (elapsed ≈ ctx), it just takes longer to report.
	// The earlier 3 s value was a margin flake on the -race "test (arm64
	// / sqlite)" CI lane: a correct drain rides chatConn.shutdown's 2 s
	// close-frame write deadline plus scheduling overhead, which under
	// runner contention crept to 3.0028 s and tripped the wrong branch.
	// 10 s clears the 2 s internal deadline with ample headroom without
	// blunting regression detection.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	// accumulates dozens to a couple hundred ms on top. Kept strictly
	// above the 10 s ctx so the meaningful DeadlineExceeded branch wins
	// over this backstop (CI runs surfaced prior 2.5 s/3 s ceilings as
	// margin flakes).
	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown returned DeadlineExceeded after %v: drain stalled on blocked readPump", elapsed)
		}
		_ = elapsed
	case <-time.After(15 * time.Second):
		t.Fatalf("Shutdown did not return within 15s; readPump likely still blocked")
	}
}
