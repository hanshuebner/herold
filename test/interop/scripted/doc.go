// Package scripted runs Go-side conformance suites against in-process
// herold servers. The two scenarios cover what the Python/Docker
// interop runner cannot exercise quickly:
//
//   - smtpcorpus_test runs a fixed RFC 5321/6152/3030/4954 corpus
//     against a live protosmtp server constructed via the testharness.
//     Each scripted exchange is a list of "C: <line>" / "S: <pattern>"
//     pairs that the runner asserts against the wire output. The
//     corpus stays self-contained — no external binary required — so
//     `go test ./test/interop/scripted/...` runs in CI even on machines
//     without imaptest or Postfix.
//   - imaptest_test invokes Dovecot's imaptest if it is on PATH, against
//     a protoimap server attached to the testharness. The test skips
//     cleanly when the binary is absent so the regular CI conformance
//     job still passes on minimal runners; the nightly interop job
//     sets up the binary and exercises the full surface.
//
// The CI workflow's `conformance` job runs `go test ./test/interop/...`,
// so any conformance work that lands here is automatically picked up.
package scripted
