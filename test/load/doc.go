// Package load contains the load-testing harness for herold.
//
// It lives under test/load/ inside the main module (github.com/hanshuebner/herold)
// so it can import the in-process testharness and all internal packages without
// a separate module or CGO.  The nightly CI job runs the full suite with
//
//	go test -count=1 -timeout=2h ./test/load/...
//
// Each scenario implements the Scenario interface: a Run method that
// receives a live harness, drives load against it, and returns a RunResult.
// The runner serialises the result to JSON under test/load/runs/<timestamp>/.
//
// Backend selection:
//
//	STORE_BACKEND=sqlite   (default) — pure-Go SQLite via modernc.org/sqlite
//	STORE_BACKEND=postgres           — Postgres, requires HEROLD_PG_DSN
//
// Build constraint: CGO_ENABLED=0 always.  The mattn/go-sqlite3 benchmark
// shim uses the "spike" build tag; nothing here touches it.
package load
