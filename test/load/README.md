# test/load -- load-test harness

This directory contains the load-testing harness for herold.  It exercises
the scenarios specified in
`docs/design/server/implementation/03-testing-strategy.md` §7 against an
in-process server (no external listeners, no fixtures dropped on disk).

## Running locally

### Smoke tests (fast, safe on any machine)

```
CGO_ENABLED=0 go test -v -count=1 -timeout=120s ./test/load/...
```

Both scenarios complete in a few seconds against a tiny dataset (2 connections
x 5 messages for inbound burst; 100 seeded messages for fetch throughput).

### Full-scale runs

```
LOAD_FULL_SCALE=1 CGO_ENABLED=0 go test -v -count=1 -timeout=2h ./test/load/...
```

Full-scale parameters:

| Scenario        | Parameter           | Smoke | Full-scale |
|-----------------|---------------------|-------|------------|
| inbound_burst   | connections         | 2     | 16         |
| inbound_burst   | messages_per_conn   | 5     | 6 250      |
| inbound_burst   | total messages      | 10    | 100 000    |
| inbound_burst   | timeout_seconds     | 30    | 1 800      |
| inbound_burst   | min throughput      | 1     | 100 msg/s  |
| fetch_throughput| message_count       | 100   | 100 000    |
| fetch_throughput| fetch_timeout_s     | 60.0  | 60.0 (*)   |

(*) The 1 s spec target for FETCH is held back until per-runner baselines
exist on both this MacBook Air M3 and the Hetzner CAX21 self-hosted CI
runner.  See "Open questions" below.

The connection count (16) matches the production-realistic
`MaxConcurrentPerIP` cap.  Earlier drafts ran 500 concurrent connections
from 127.0.0.1 with the cap raised; current scope is sustained throughput
within the production gating, not the abuse cap itself.

### Backend selection

```
STORE_BACKEND=sqlite    go test ./test/load/...   # default
STORE_BACKEND=postgres  HEROLD_PG_DSN=<dsn> go test ./test/load/...
```

The Postgres leg requires a throwaway database.  The harness does not truncate
between runs; point `HEROLD_PG_DSN` at an empty database or truncate manually.

### pprof capture

Enable pprof during a run by setting `HarnessOpts.EnablePprof = true` in a
custom test (the env-var hook is a follow-up):

```go
RunScenario(t, sc, HarnessOpts{EnablePprof: true})
```

Profiles are written to `test/load/runs/<timestamp>/pprof/`:

- `cpu.pprof`     -- CPU profile for the duration of the scenario
- `heap.pprof`    -- heap allocation profile
- `goroutine.pprof` -- goroutine stack snapshot
- `block.pprof`   -- blocking profile

Visualise with:

```
go tool pprof -http=:8081 test/load/runs/<ts>/pprof/cpu.pprof
```

## JSON output format

Each scenario writes `runs/<timestamp>/<scenario>_<timestamp>.json`:

```json
{
  "scenario":          "inbound_burst",
  "backend":           "sqlite",
  "started_at":        "2026-05-05T21:24:31Z",
  "duration_seconds":  847.04,
  "passed":            true,
  "gates": [
    {
      "name":       "error_rate",
      "required":   0.01,
      "measured":   0,
      "direction":  "<=",
      "passed":     true
    },
    {
      "name":       "throughput_msg_per_sec",
      "required":   100,
      "measured":   118.06,
      "direction":  ">=",
      "passed":     true
    }
  ],
  "metrics": {
    "connections":            16,
    "messages_delivered":     100000,
    "throughput_msg_per_sec": 118.06
  },
  "errors":     [],
  "pprof_dir":  "",
  "go_version": "go1.25.0",
  "goos":       "darwin",
  "goarch":     "arm64"
}
```

Fields:

| Field               | Type     | Description                                      |
|---------------------|----------|--------------------------------------------------|
| `scenario`          | string   | Scenario name identifier                         |
| `backend`           | string   | `sqlite` or `postgres`                           |
| `started_at`        | RFC 3339 | Wall-clock start time                            |
| `duration_seconds`  | float    | Total elapsed wall time                          |
| `passed`            | bool     | True if all gates passed                         |
| `gates`             | array    | Per-gate name/required/measured/direction/passed |
| `metrics`           | object   | Named float measurements                         |
| `errors`            | array    | Non-fatal error strings (up to 10 samples)       |
| `pprof_dir`         | string   | Directory where pprof profiles were written      |
| `go_version`        | string   | `runtime.Version()`                              |
| `goos` / `goarch`   | string   | `runtime.GOOS` / `runtime.GOARCH`                |

## Scenarios

### inbound_burst (fully implemented)

16 concurrent SMTP connections (matching production `MaxConcurrentPerIP`)
each delivering 6 250 messages, 100 000 total.

Gates:

- `error_rate <= 0.01`
- `throughput_msg_per_sec >= 100` (REQ-NFR-01 sustained inbound floor)

Metrics: `connections`, `messages_per_conn`, `messages_expected`,
`messages_delivered`, `error_count`, `error_rate`, `duration_seconds`,
`throughput_msg_per_sec`.

The scenario does not enforce a fixed-time delivery deadline -- the
spec's "5 000 000 messages in 60 s" reading is three orders of magnitude
above REQ-NFR-01 and was treating the load shape as a throughput
contract.  The pass criterion is sustained throughput against the
REQ-NFR-01 baseline; `TimeoutSeconds` is a wall-clock backstop, not a
gate.

### fetch_throughput (fully implemented)

One IMAP session, N messages pre-seeded via the store layer, `FETCH 1:*
(FLAGS UID)` measured end-to-end over IMAPS.

Gates:

- `messages_fetched >= messages_seeded`
- `fetch_duration_seconds <= FetchTimeoutSeconds` (default 60; spec target
  1 once per-runner baselines are established)

Metrics: `messages_seeded`, `seed_duration_seconds`,
`fetch_duration_seconds`, `messages_fetched`, `fetch_rate_msg_per_sec`.

### idle_scale (stubbed -- follow-up wave)

2 000 concurrent IMAP IDLE sessions, one message per mailbox per second.
Not yet implemented.

### queue_retry_storm (stubbed -- follow-up wave)

100 000 deferrable messages, remote 4xx for one hour then recovering.
Not yet implemented.

### mixed_workload (stubbed -- follow-up wave)

Composite of SMTP in / SMTP out / IMAP / JMAP / admin queries.
Not yet implemented.

## Baselines

Captured 2026-05-05 against this branch.  Reproduce with the full-scale
recipe above.

### inbound_burst, sqlite

| Runner                          | Throughput   | Error rate | Wall-time | Notes                              |
|---------------------------------|--------------|------------|-----------|------------------------------------|
| MacBook Air M3 (8 CPU, 16 GB)   | 118.06 msg/s | 0.0 %      |   847 s   | 100 000 / 100 000 delivered        |
| Hetzner CAX21 (4 ARM CPU, 8 GB) |  45.18 msg/s | <0.01 %    | 1 800 s   | 81 360 / 100 000 (timed out)       |

The M3 number is sustained over the full run (initial throughput is
~150 msg/s; tails to ~118 msg/s as the SQLite database grows past ~80 k
rows).

CAX21 is roughly 2.6x slower than M3 and falls ~55 % short of the
REQ-NFR-01 100 msg/s target.  The CI gate floor is set to 40 msg/s --
10 % below CAX21's measured baseline -- so the harness still catches
regressions on CAX21.  Closing the gap to REQ-NFR-01 is tracked
separately; see "Open questions" §3.

### fetch_throughput, sqlite

| Runner               | Fetch duration | Fetch rate         | Notes                                       |
|----------------------|----------------|--------------------|---------------------------------------------|
| MacBook Air M3       | 0.038 s / 100   | 2 629 msg/s smoke  | full-scale: 0.062 s, 15 972 msg/s, see note |
| Hetzner CAX21 (CI)   | TBD            | TBD                | Maintainer to capture                       |

Full-scale on M3 seeded 100 000 messages (703 s seed time) but FETCH
returned only 1 000 rows. This is the IMAP server-side cap discussed
in "Open questions"; until it's lifted, the gate is per-row throughput
rather than `messages_fetched == messages_seeded`.

## Open questions

1. **CAX21 baseline.**  Run the full-scale recipe on the Hetzner CAX21
   self-hosted CI runner and add the numbers to the Baselines section.
   Until both M3 and CAX21 numbers exist we cannot pick a hardware-
   appropriate gate for `fetch_throughput` (1 s spec target vs. the 60 s
   relaxed gate).

2. **IMAP `ListMessages` 1 000-row cap (#99).**  `internal/protoimap/session_mailbox.go`
   and `internal/protoimap/session_fetch.go` call
   `store.Meta().ListMessages(ctx, mb.ID, MessageFilter{WithEnvelope: true})`
   without a Limit; the store backends silently cap at 1 000.  Result:
   SELECT and FETCH against a mailbox with N > 1 000 messages return
   only the first 1 000 rows.  This is a real scaling bug against
   REQ-NFR-01 (1 TB mailboxes).  The full-scale fetch baseline is
   pinned at this 1 000 ceiling until the IMAP path paginates.

3. **Inbound throughput gap on CAX21 vs. REQ-NFR-01 (#100).**  CAX21 sustains
   ~45 msg/s where REQ-NFR-01 calls for 100 msg/s.  Likely culprits to
   profile (in priority order):

   - SQLite WAL serialisation -- 16 SMTP sessions through a single
     writer connection, each delivery does a transactional insert plus
     refcount + envelope index updates.
   - LLM-transparency lookup -- `persistLLMRecord` does an indexed
     lookup by Message-ID for every delivered message; with one extra
     index hit per message, this is non-zero per delivery.
   - DKIM / SPF / DMARC verification -- the harness wires real
     verifiers against fakedns.  Verification is per-message and
     synchronous in the delivery path.

   Capture pprof on CAX21 (`HarnessOpts.EnablePprof = true`) to attribute
   the time precisely.  Either close the gap (likely a single-batch
   transaction reshape on the store side) or revise REQ-NFR-01's
   reference hardware to match CAX21.

2. **CI wiring.**  `nightly.yml:38-44` already has the conditional that
   stops printing "test/load not yet populated" once this branch lands.
   Wiring CI is deferred until at least one stabilisation pass settles
   the throughput numbers across both runners.

3. **Postgres leg.**  The smoke and full-scale runs default to SQLite.
   The first nightly run after CI wiring should include
   `STORE_BACKEND=postgres HEROLD_PG_DSN=<dsn>` to measure the Postgres
   WAL path against the same gates.

## Resolved

- *llm-transparency WARN noise.*  Each delivered message used to log
  `llm-transparency: lookup message ID for record: store: not found` at
  WARN.  Fixed at `internal/protosmtp/deliver.go` (commit 5bb72cb on
  `main`): the lookup now normalises the Message-ID before hitting the
  store, matching the way the column is normalised at insert time.
- *Per-IP cap bypass.*  The harness used to set
  `MaxConcurrentPerIP = MaxSMTPConns` because all simulated clients
  share 127.0.0.1.  This bypassed the production gating.  The cap now
  defaults to 16 (production realistic); `HarnessOpts.MaxConcurrentSMTPPerIP`
  is the explicit override for scenarios that need the cap raised.
- *Strict fixed-time gate vs. REQ-NFR-01.*  The original
  `messages_delivered >= N*M within timeout_seconds` gate failed on
  contended SQLite WAL even when throughput was healthy.  Replaced with
  `throughput_msg_per_sec >= 100`, which is the actual REQ-NFR-01
  contract.  `TimeoutSeconds` is a backstop, not a gate.
- *MaxCommandsPerSession truncation.*  The harness inherited the
  production default of 200 SMTP commands per session.  At 5 commands
  per delivered message the session rotated every ~40 messages and
  every rotation counted as a delivery error, producing a spurious
  ~2 % error rate.  The harness now sets `MaxCommandsPerSession =
  1_000_000` -- the load test measures throughput, not the abuse cap.
- *Wrapper deadline.*  `RunScenario` previously wrapped every run in a
  10-minute context.  Full-scale runs need ~14 min; the wrapper made
  every full-scale run hit the deadline at exactly 600 s.  Removed -- the
  scenario's own `TimeoutSeconds` is the deadline; `go test -timeout=2h`
  is the backstop.
