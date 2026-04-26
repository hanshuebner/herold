// Package testharness spins up an in-process herold server for tests: a
// tempdir data directory, scripted clients, fake DNS, injected SMTP peers,
// and injected time and randomness. Every Phase 1 integration test that
// exercises multiple subsystems together uses this harness; no test dials
// the network directly except through harness-bound listeners.
//
// Ownership: conformance-fuzz-engineer.
//
// Cleanup: Start registers teardown via testing.TB.Cleanup and does not
// return a func(). The redundant return value (present in earlier drafts)
// encouraged callers to forget t.Cleanup; requiring t.Cleanup is simpler
// and cannot be bypassed by a caller who forgets to defer. See
// docs/design/implementation/03-testing-strategy.md.
//
// Rules enforced by this package:
//
//   - Deterministic: no wall-clock reads, no real DNS, no real filesystem
//     outside t.TempDir().
//   - Bounded goroutines: every goroutine Start launches is tied to the
//     server's context and joined on teardown; NumGoroutine returns to the
//     baseline after Close.
//   - internal-only: depends on internal/store and internal/clock only;
//     must not import any other Wave 0 package while those are in flux.
//
// Sub-packages:
//
//   - fakedns:    in-memory DNSResolver covering MX/A/AAAA/TXT/TLSA.
//   - fakeplugin: in-process FakePlugin registry.
//   - fakestore:  in-memory Store implementation (default when Options.Store is nil).
//   - smtppeer:   scripted in-process SMTP responder.
//   - corpus:     deterministic synthetic fixtures.
package testharness
