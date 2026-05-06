// Package load_test exercises the load-test scenarios.
//
// Smoke tests (default, runnable in CI):
//
//	go test -v ./test/load/...
//
// These use tiny parameters (2 connections, 5 messages) so they finish in
// seconds and are safe to run on any machine.
//
// Full-scale runs (nightly CI):
//
//	LOAD_FULL_SCALE=1 go test -v -timeout=2h ./test/load/...
//
// Backend selection:
//
//	STORE_BACKEND=sqlite    (default)
//	STORE_BACKEND=postgres  (requires HEROLD_PG_DSN)
package load

import (
	"os"
	"testing"
)

// fullScale returns true when the test is configured for full-scale runs.
func fullScale() bool {
	return os.Getenv("LOAD_FULL_SCALE") == "1"
}

// TestInboundBurst_Smoke runs a minimal inbound-burst scenario to verify
// the harness wiring end-to-end: 2 concurrent SMTP connections, 5 messages
// each.  Suitable for CI on every PR.
//
// Full-scale runs default to 16 concurrent connections (matching the
// production-realistic MaxConcurrentPerIP cap) delivering enough messages
// to give the throughput-floor gate a stable measurement window.
func TestInboundBurst_Smoke(t *testing.T) {
	conns := 2
	msgs := 5
	timeout := 30
	minThroughput := 1.0
	if fullScale() {
		// 16 conns × 6 250 = 100 000 messages. Bound the wall-time at
		// 1 800 s so a slow CI runner still finishes; the pass/fail gate
		// is throughput, not the deadline.
		conns = 16
		msgs = 6250
		timeout = 1800
		minThroughput = 100.0 // REQ-NFR-01 sustained inbound floor.
	}

	sc := &InboundBurstScenario{
		Connections:            conns,
		MessagesPerConn:        msgs,
		TimeoutSeconds:         timeout,
		MaxErrorRate:           0.01,
		MinThroughputMsgPerSec: minThroughput,
	}
	RunScenario(t, sc, HarnessOpts{
		// Match the per-IP cap to the conn count so the harness's
		// 127.0.0.1-only client never spins on 421 retries during steady
		// state. Production keeps MaxConcurrentPerIP at 16.
		MaxSMTPConns:           conns + 4,
		MaxConcurrentSMTPPerIP: conns,
	})
}

// TestFetchThroughput_Smoke runs a minimal fetch-throughput scenario:
// 100 messages seeded, then FETCH 1:* (FLAGS UID) measured.  Suitable for
// CI on every PR.  Full-scale: 100 000 messages with a 1 s gate.
//
// Until the IMAP-side ListMessages-1000-row cap is fixed (separate
// REQ-NFR-01 follow-up), full-scale runs return only the first 1 000
// messages even though 100 000 were seeded. The gate is therefore on
// the per-row throughput, not on `messages_fetched == messages_seeded`.
func TestFetchThroughput_Smoke(t *testing.T) {
	count := 100
	gate := 60.0
	minRate := 0.0
	if fullScale() {
		count = 100000
		gate = 1.0
		minRate = 1000.0 // smoke target while the 1 000-row cap is in place
	}

	sc := &FetchThroughputScenario{
		MessageCount:          count,
		FetchTimeoutSeconds:   gate,
		MinFetchRateMsgPerSec: minRate,
	}
	RunScenario(t, sc, HarnessOpts{})
}
