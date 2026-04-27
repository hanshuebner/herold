# 03 — Testing strategy

Mail servers are protocol-heavy and security-critical. Testing strategy has to cover conformance, security, performance, and operational confidence. What follows is what we commit to; what we refuse to commit to is at the end.

## Levels

### 1. Unit tests

- Every non-trivial pure function.
- Parser combinators (SMTP commands, IMAP commands, MIME, DKIM tags).
- State-machine transitions in isolation.
- Crypto primitives via test vectors.

Goal: fast, runs on every PR in < 30s.

### 2. Property tests

- Parsers: round-trip (parse → format → parse) for valid inputs.
- DKIM signing then verification.
- Sieve: parse → format → parse equivalence.
- Threading: message set → threads; adding a new message → incremental update matches batch update.
- State-change feed: monotonic; every mutation writes exactly one feed event per affected entity.

Library: `proptest` or `quickcheck`.

### 3. Integration tests

In-process end-to-end against a test server:

- SMTP receive → mailbox present.
- IMAP APPEND → SELECT → FETCH → flags correct.
- JMAP `Email/set` → `Email/get` → consistency.
- Sieve redirect at delivery → message in correct folder.
- Spam score above threshold → in Junk.
- DKIM signing outbound → message carries signature matching our public key.
- Queue retry: mock SMTP peer returns 4xx three times then 2xx → delivered on 4th attempt, right schedule.

Harness: spin up a server with a tempdir data directory, scripted client interactions, deterministic time (injected clock).

### 4. Protocol conformance

External test suites, run in CI:

- **IMAP**: `imaptest` (Dovecot's) against our server. Cover baseline + CONDSTORE + QRESYNC + UTF-8.
- **SMTP**: scripted test suite; additionally, automated interop test against Postfix and Exim in Docker — send from ours to them and back.
- **JMAP**: public JMAP compliance harness (Fastmail's) where available.
- **Sieve**: Pigeonhole's test corpus against our interpreter.
- **DKIM**: standard DKIM test vectors.
- **DMARC/ARC**: published test vectors.

### 5. Interop

Schedule a recurring (weekly) automated interop run against real third-party servers (in a staging environment with throwaway domains):

- Send to and receive from: Gmail, Outlook, iCloud, Yahoo, Fastmail, Zoho, ProtonMail.
- Verify SPF/DKIM/DMARC alignment per counterparty.
- Log deliverability (did our mail land in Inbox vs. Spam at each provider?).

This isn't a correctness test — it's a deliverability canary. Real senders care.

### 6. Fuzzing

Go's native fuzzing (`go test -fuzz=Fuzz<Name>`, since Go 1.18). Each fuzz target lives next to its package as a `Fuzz*` function with a seed corpus under `testdata/fuzz/`. Targets:

- SMTP command parser.
- SMTP address parser.
- IMAP command parser.
- IMAP literal / continuation logic.
- MIME parser.
- RFC 5322 address / header parser.
- DKIM signature parser.
- DMARC/SPF/ARC record parsers.
- Sieve parser and interpreter.
- Config parser.

Run:
- Short campaign (minutes) on every PR.
- Long campaign (hours) nightly.
- Week-long campaign pre-release.

Any crash → tracked, reproduced, fixed. Never skipped.

### 7. Load / stress

Scenarios:

- **Inbound burst**: 500 concurrent SMTP connections, 10k messages each within 60 seconds. Verify: no memory leak, queue grows and drains, no connection starvation.
- **IDLE scale**: 2k concurrent IDLE sessions, one message delivered per mailbox per second. Verify: notifications delivered within 1s, memory bounded.
- **FETCH throughput**: one session, 100k messages, `FETCH 1:* (FLAGS UID)`. Verify: < 1s on our hardware.
- **Queue retry storm**: 100k deferrable messages, remote 4xx'ing for an hour then recovering. Verify: ordered retries, no thundering herd on recovery.
- **Mixed workload**: composite of SMTP in, SMTP out, IMAP, JMAP, admin queries.

Tools: custom Go harness for mail protocols (none of the standard load testers covers SMTP/IMAP/JMAP well). Output: `pprof` flame graphs (CPU + heap + goroutine + block), memory profile, metrics dashboard.

### 8. Chaos / fault injection

- `kill -9` mid-DATA → no corruption, message not lost if 250 OK was sent, message dropped if not.
- Disk full → SMTP defers (4xx), no partial blob writes.
- Store corruption (simulated bad blocks in SQLite) → recovery path, clean error.
- DNS timeout on SPF lookup → scored as `temperror`, not a crash.
- Certificate expiry during live operation → new connections fail with clear error, existing survive until close.

### 9. Upgrade / migration tests

- Bring up vN-1, populate data, shut down.
- Bring up vN against same data dir → migrations run → data accessible.
- Downgrade explicitly rejected (forward-only per REQ-OPS-100).

### 10. Security review

Cover:

- Every use of `unsafe.Pointer` or `cgo` justified in a comment, reviewed in PR (default: none outside the crypto/TLS stdlib).
- Cryptographic code paths (DKIM, TLS, password, session tokens): explicit review checklist.
- Input validation on every protocol surface.
- Authentication paths, session management, token revocation.

External review budgeted before v1.0 GA (a week of a qualified firm's time, or equivalent community review).

## Test data

### Corpora

- **Synthetic email corpus**: ~100k messages generated with realistic header distributions, MIME structures, languages, sizes. Used for indexing, storage, and sync tests.
- **Real-world public corpora**: Enron, TREC spam. For spam training and search evaluation.
- **DKIM signature corpus**: messages with valid/invalid signatures from known senders.
- **Malformed corpus**: fuzzer corpora; targeted malformed messages (boundary edge cases, encoded-word absurdities).

### Fixtures

- Deterministic time (injected clock).
- Deterministic IDs (seed-able UUID/snowflake generator in test mode).
- Fake DNS responder for SPF/MX/DKIM/MTA-STS resolution.
- In-process SMTP peer for queue tests (configurable to return arbitrary codes).

## CI matrix

**Platforms tested per PR:**

| Platform | Purpose | Status |
|---|---|---|
| **Linux amd64** | Primary production target | required |
| **Linux arm64** | Cloud ARM deployments (AWS Graviton, Ampere, Hetzner ARM) | required |
| **macOS arm64 (Apple Silicon)** | **Primary developer platform** — every contributor uses this day-to-day; CI must catch "works on Linux, broken on mac" early | required |
| **macOS amd64** | Intel Macs, diminishing but still extant | best-effort (may be retired when GitHub runners drop it) |

macOS CI parity is a **first-class concern**, not an afterthought: filesystem case-insensitivity (default HFS+/APFS), SIGCHLD handling under Darwin's BSD-flavoured syscalls, sandbox-exec plugin paths, and TLS cert-store quirks differ from Linux and have historically broken "runs on Linux" projects the moment a developer tried them locally.

**Per PR, on every platform above:**

- `go build ./...`, `go test ./...` (with `-race` on Linux; race detector is heavy on macOS, run without unless flagged).
- `go vet`, `staticcheck`.
- `gofmt -l` (fail if any diff), `goimports -l`.
- `govulncheck` against `go.sum` (once, not per-platform).
- Fuzz short campaigns (`go test -fuzz -fuzztime=30s` per target) — Linux only, to keep PR latency manageable.
- Integration + conformance tests against both SQLite and Postgres — Linux primary, macOS with testcontainers where practical.

**Nightly:**

- Long fuzz campaigns (Linux amd64).
- Extended load tests (Linux amd64).
- Interop run against third-party servers (Linux amd64).
- macOS arm64 full integration run (catches drift that per-PR short runs might miss).
- SBOM diff.

**Platforms NOT in the CI matrix** but best-effort-supported if contributors show up:
- FreeBSD, OpenBSD: build tested locally by interested contributors; no CI.
- Windows: development-only, no production support; no CI. Build may work; tests may not (path semantics, signals, fork/exec).

## Code coverage

Not a blocking target (reviews + property tests + conformance are better guarantees). Track coverage via `go test -coverprofile`, render with `go tool cover`. Don't gate on percentages.

## Deterministic tests

A hard rule: any test that isn't deterministic gets fixed or deleted. Flaky tests destroy the value of CI. Inject time, randomness, and I/O; never reach to wall-clock or real filesystem in unit tests.

## Testing what we don't want to test

We will *not* ship tests that merely mirror the implementation ("test-then-implement-and-test-the-same-thing"). Tests should assert *behavior* from the spec or operator-visible contract, not the shape of our code.

## Tooling

- **`go test`** — built-in runner; test + race detector (`-race`) + coverage + fuzz.
- **`gotestsum`** — prettier output, parallel execution, retries, JUnit XML for CI.
- **`go test -fuzz`** — native fuzzing.
- **`go test -bench` + `benchstat`** — micro-benchmarks and stat-significant comparisons across commits.
- **`github.com/go-gremlins/gremlins`** — mutation testing on hot paths (parsers, auth). Not blocking, informative.
- **`staticcheck` + `go vet`** — linting.
- **`govulncheck`** — dependency CVE check, in CI per PR.
- **`testcontainers-go`** — Postgres integration tests.

## What we will NOT test

- Third-party code paths (Go stdlib, pgx internals, Bleve internals) — trust the maintainers.
- Performance regressions as CI gates. Performance is tracked as an out-of-CI concern with explicit benchmark runs at phase boundaries. In-CI perf checks produce flaky noise.
- UI snapshots (no UI in phase 1–2).
- Upstream provider behavior (Gmail might change DMARC handling). We test ours; the interop canary warns us when theirs changes.

## Exit criteria per phase

Every phase document lists explicit exit criteria — those are primarily tests. When each criterion is green, the phase exits.

## Documentation tests

Every documented CLI command, REST endpoint, and config example MUST be executable in a test. Broken examples in docs are bugs.
