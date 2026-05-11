package protochat

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// TestPublicationRace targets the structural race fixed in commit
// 978b13d: Server.handleUpgrade used to publish a fresh chatConn into
// Server.conns BEFORE chatConn.run() initialised c.ctx / c.cancel via
// context.WithCancel. A Server.Shutdown that snapshotted s.conns
// during that window then read c.cancel from inside chatConn.shutdown
// — racing the WithCancel write inside run(). The race surfaced under
// -race on CI 25631651900 (arm64 / linux) inside an unrelated test
// (TestShutdown_DrainsBlockedReadPump) that did not target it on
// purpose.
//
// This test reproduces the structural race by spawning a single
// dialer per iteration, waiting only until the conn has been
// published into s.conns (so Shutdown's snapshot is guaranteed to
// observe it), then immediately racing Server.Shutdown against the
// in-flight handleUpgrade goroutine. Many iterations run in parallel
// (sub-tests) to amplify the race-detector's sensitivity without
// stressing wall-clock budget.
//
// Why the single-dialer-per-iteration shape (not a single server with
// many concurrent dials):
//
//   - The cc.cancel race we care about is between handleUpgrade's
//     pre-run() window and Shutdown's snapshot-then-shutdown loop.
//     The race fires per-conn; many parallel conns do not amplify it
//     beyond what many parallel iterations already give us.
//   - The Server also holds an internal connWG that handleUpgrade
//     Add(1)s after publication and Shutdown Wait()s on. With many
//     concurrent dials per server, a dial whose Add(1) lands during
//     Shutdown's Wait wake-window panics with "sync: WaitGroup is
//     reused before previous Wait has returned" — a real but separate
//     concurrency hazard that masks the publication race we are here
//     to test. Per-iteration servers with one dial each sidestep it.
//
// Pass / fail signal:
//
//   - Under `-race` on the post-fix tree (current main) no race fires;
//     the test runs cleanly.
//   - Under `-race` on the pre-fix tree the Go runtime would have
//     produced a "DATA RACE" report on chatConn.cancel (or
//     chatConn.ctx) and failed the test via the testing framework's
//     intrinsic race-detector hook.
//   - We deliberately do not assert on Shutdown's return value or on
//     connection counts: the regression surface is the data race
//     itself, and the race detector is the assertion.
//
// Concurrency budget: iterations=64 sub-tests, each a fresh Server
// with 1 dial + 1 Shutdown. Empirically runs in ~1-2 s wall-clock
// under -race on Apple Silicon, well under the 5 s ceiling called
// out by the task brief and the -count=20 instruction.
func TestPublicationRace(t *testing.T) {
	const iterations = 64

	// Parallel sub-tests amplify the race detector's chance of
	// catching the unsynchronised write/read pair without stretching
	// the wall-clock budget. Each sub-test owns its own Server +
	// httptest.Server so cleanup is straightforward.
	for i := 0; i < iterations; i++ {
		i := i
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			runPublicationRaceIteration(t)
		})
	}
}

// runPublicationRaceIteration builds a fresh protochat Server, opens
// one WebSocket connection, waits until handleUpgrade has published
// the conn into Server.conns, then immediately races Server.Shutdown
// against the still-running handleUpgrade goroutine.
//
// On the PRE-FIX code path, handleUpgrade has not yet entered cc.run()
// at the moment Shutdown snapshots Server.conns, so the WithCancel
// that initialises cc.ctx / cc.cancel has not happened. Shutdown's
// cc.shutdown call then reads cc.cancel concurrently with run()'s
// WithCancel write — a data race the runtime surfaces under -race.
//
// On the POST-FIX code path, cc.ctx / cc.cancel are initialised
// BEFORE the publication into Server.conns, with the connsMu acquire
// providing a happens-before edge for Shutdown's snapshot. No race.
func runPublicationRaceIteration(t *testing.T) {
	t.Helper()

	clk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	mem := newFakeMembership()
	bcast := NewBroadcaster(slog.New(slog.NewTextHandler(io.Discard, nil)), mem.ListMembers)
	srv := New(Options{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:           clk,
		SessionResolver: testResolver,
		Broadcaster:     bcast,
		Membership:      mem.IsMember,
		PeersResolver:   mem.ListPeers,
		PingInterval:    24 * time.Hour,
		PongTimeout:     48 * time.Hour,
		WriteTimeout:    24 * time.Hour,
		MaxFrameBytes:   4096,
		PerPrincipalCap: 4,
		MaxConnections:  16,
		WriteQueueSize:  16,
		TypingAutoStop:  10 * time.Second,
		PresenceGrace:   30 * time.Second,
		AllowedOrigins:  nil,
	})
	mux := http.NewServeMux()
	mux.Handle("/chat/ws", srv.Handler())
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	addr := strings.TrimPrefix(httpSrv.URL, "http://")

	// Spawn the dial in a background goroutine so the main path can
	// race Shutdown against it. We deliberately do NOT wait for the
	// dial to complete the handshake before calling Shutdown — the
	// race window is between handleUpgrade's publication into s.conns
	// and run()'s WithCancel write, and waiting for handshake
	// completion (which signals run() has been entered on the pre-fix
	// code) would close that window.
	var dialWG sync.WaitGroup
	dialWG.Add(1)
	connCh := make(chan *testClient, 1)
	go func() {
		defer dialWG.Done()
		c, _, err := dialTestClient(addr, map[string]string{
			pidHeader: "1",
		})
		if err != nil || c == nil {
			// Shutdown firing mid-upgrade can cause the dial to error
			// out (EOF on the upgrade read). That is not a regression
			// in the publication race itself; the race detector is
			// the only assertion we make.
			return
		}
		connCh <- c
	}()

	// Wait until handleUpgrade has at least reached the publication
	// site (s.conns[cc] = struct{}{}). This guarantees Shutdown's
	// snapshot observes cc rather than running on an empty conn set.
	//
	// Poll interval is 5 ms (deliberately not tighter): the publication
	// site at line ~418 is followed within ~µs by s.connWG.Add(1) at
	// line ~461. If Shutdown's connWG.Wait races with that Add — i.e.
	// Wait enters with count=0 and Add fires shortly after — the race
	// detector flags an unrelated WaitGroup-misuse race on Server.connWG
	// that has nothing to do with the cc.cancel publication path we
	// target. A 5 ms grace lets Add land before Shutdown starts,
	// isolating the cc.cancel race from the connWG race for both
	// pre-fix and post-fix runs. The grace does NOT close the cc.cancel
	// race window because the race detector tracks happens-before edges,
	// not wall-clock latency: in pre-fix code the WithCancel write
	// inside run() never synchronises with Shutdown's cc.shutdown read
	// (no shared mutex or channel covers cc.cancel), so the race fires
	// regardless of when Shutdown finally calls cc.shutdown.
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.connsMu.Lock()
		n := len(srv.conns)
		srv.connsMu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection never published into s.conns within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Small post-publication grace so handleUpgrade's s.connWG.Add(1)
	// (line ~461) lands before Shutdown's s.connWG.Wait() in the
	// helper goroutine spawned at server.go:251. See the rationale on
	// the poll-interval choice above.
	time.Sleep(5 * time.Millisecond)

	// Race Shutdown against the still-running handleUpgrade. The 500
	// ms ctx deadline is well above the typical drain wall-clock under
	// -race (~milliseconds) while still bounding a structurally-stuck
	// shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = srv.Shutdown(ctx)

	// Drain the dial goroutine and close any client conn it produced
	// so the iteration leaves no goroutines pinned across t.Cleanup.
	dialWG.Wait()
	select {
	case c := <-connCh:
		c.Close()
	default:
	}
}
