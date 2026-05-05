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
// each, 30 s timeout.  Suitable for CI on every PR.
func TestInboundBurst_Smoke(t *testing.T) {
	conns := 2
	msgs := 5
	timeout := 30
	if fullScale() {
		conns = 500
		msgs = 10000
		timeout = 60
	}

	sc := &InboundBurstScenario{
		Connections:     conns,
		MessagesPerConn: msgs,
		TimeoutSeconds:  timeout,
		MaxErrorRate:    0.01,
	}
	RunScenario(t, sc, HarnessOpts{
		MaxSMTPConns: conns + 10,
	})
}

// TestFetchThroughput_Smoke runs a minimal fetch-throughput scenario:
// 100 messages seeded, then FETCH 1:* (FLAGS UID) measured.  Suitable for
// CI on every PR.  Full-scale: 100 000 messages with a 1 s gate.
func TestFetchThroughput_Smoke(t *testing.T) {
	count := 100
	gate := 60.0
	if fullScale() {
		count = 100000
		gate = 1.0
	}

	sc := &FetchThroughputScenario{
		MessageCount:        count,
		FetchTimeoutSeconds: gate,
	}
	RunScenario(t, sc, HarnessOpts{})
}
