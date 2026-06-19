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
// drain window. chatConn.shutdown calls CloseRead() on the underlying
// *net.TCPConn, which unblocks the parked Read deterministically at the
// socket layer, so connWG.Wait() in Server.Shutdown completes promptly
// even under heavy scheduling contention.
//
// The earlier fix relied solely on SetReadDeadline(time.Unix(1,0)) to
// nudge the read. That nudge is a poll-deadline re-arm the runtime may
// defer under -race contention, so the read stalled until the peer's
// close landed a TCP RST -- a 10 s drain stall on the CI "test (arm64 /
// sqlite)" lane. CloseRead removes that timing dependency.
//
// Without a working nudge, Shutdown returns context.DeadlineExceeded;
// with the fix it returns nil well under the supplied deadline.
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
	// under the bug it sits on connWG.Wait() until the ctx fires, so the
	// drain elapsed ~= the ctx deadline. With the CloseRead fix the
	// parked Read unblocks at the socket layer and the drain completes in
	// milliseconds, deterministically, regardless of scheduling pressure.
	// 5 s clears chatConn.shutdown's 2 s close-frame write deadline with
	// ample headroom while still surfacing the 10 s-class stall cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.srv.Shutdown(ctx)
	}()

	// The select-after deadline is strictly larger than the Shutdown ctx
	// deadline so a slow-but-correct drain cannot trip the wrong branch;
	// it is purely a backstop against a wedged Shutdown goroutine.
	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown returned DeadlineExceeded after %v: drain stalled on blocked readPump", elapsed)
		}
		_ = elapsed
	case <-time.After(10 * time.Second):
		t.Fatalf("Shutdown did not return within 10s; readPump likely still blocked")
	}
}
