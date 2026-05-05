# test/load -- load-test harness

This directory contains the load-testing harness for herold.  It exercises
the scenarios specified in
`docs/design/server/implementation/03-testing-strategy.md` §7.

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

Full-scale parameters match the spec in `03-testing-strategy.md`:

| Scenario        | Parameter           | Smoke | Full-scale |
|-----------------|---------------------|-------|------------|
| inbound_burst   | connections         | 2     | 500        |
| inbound_burst   | messages_per_conn   | 5     | 10 000     |
| inbound_burst   | timeout_seconds     | 30    | 60         |
| fetch_throughput| message_count       | 100   | 100 000    |
| fetch_throughput| fetch_timeout_s     | 60.0  | 1.0 (*)    |

(*) The 1 s fetch gate is relaxed to 60 s until the CI self-hosted runner
hardware is characterised.  See "Open questions" below.

### Backend selection

```
STORE_BACKEND=sqlite    go test ./test/load/...   # default
STORE_BACKEND=postgres  HEROLD_PG_DSN=<dsn> go test ./test/load/...
```

The Postgres leg requires a throwaway database.  The harness does not truncate
between runs; point `HEROLD_PG_DSN` at an empty database or truncate manually.

### pprof capture

Enable pprof during a run by setting `HarnessOpts.EnablePprof = true` in a
custom test or by exporting `LOAD_PPROF=1` (planned for a follow-up wave --
currently not wired to the env var; edit `load_test.go` directly):

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
  "started_at":        "2026-05-05T16:13:19Z",
  "duration_seconds":  0.24,
  "passed":            true,
  "gates": [
    {
      "name":       "messages_delivered",
      "required":   10,
      "measured":   10,
      "direction":  ">=",
      "passed":     true
    }
  ],
  "metrics": {
    "connections":            2,
    "messages_delivered":     10,
    "throughput_msg_per_sec": 41.8
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

500 concurrent SMTP connections, 10 000 messages each, within 60 s.

Gates:
- `messages_delivered >= connections * messages_per_conn`
- `error_rate <= 0.01`
- `duration_seconds <= 60`

Metrics reported: `connections`, `messages_per_conn`, `messages_expected`,
`messages_delivered`, `error_count`, `error_rate`, `duration_seconds`,
`throughput_msg_per_sec`.

### fetch_throughput (fully implemented)

One IMAP session, N messages pre-seeded via the store layer, `FETCH 1:*
(FLAGS UID)` measured end-to-end over IMAPS.

Gates:
- `messages_fetched >= messages_seeded`
- `fetch_duration_seconds <= FetchTimeoutSeconds` (default 60; target 1 after
  hardware characterisation)

Metrics reported: `messages_seeded`, `seed_duration_seconds`,
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

## Open questions

1. **Reference hardware for fetch_throughput gate.** The spec says "< 1 s on
   our hardware" but the CI self-hosted runner spec is not documented.  The
   gate is relaxed to 60 s until a baseline run on the self-hosted runner
   establishes the real number.  Once established, update
   `FetchTimeoutSeconds` in `TestFetchThroughput_Smoke` (full-scale branch)
   to 1.0 and document the runner hardware in this file.

2. **inbound_burst full-scale at 500x10 000 against SQLite WAL.**  The smoke
   test confirms the protocol path works; the full-scale run at 500 concurrent
   connections will stress the SQLite WAL lock contention.  Expected outcome:
   the error rate gate (1 %) will be the binding constraint.  If it fails,
   inspect the `error_count` metric and the error samples in the JSON for
   "SMTP 4xx" vs "connection refused" vs "context deadline exceeded" to
   diagnose whether the bottleneck is store contention, goroutine limits, or
   network buffers.

3. **inbound_burst gate thresholds vs. REQ-NFR-01.**  REQ-NFR-01 requires
   100 msg/s sustained inbound.  At 500 connections x 10 000 messages / 60 s
   the implied rate is 83 333 msg/s -- three orders of magnitude above the
   requirement.  The spec's scenario parameters may be aspirational.  The
   current gate (`delivered >= N*M`) is strict: every message must land.  If
   the hardware cannot deliver all 5 000 000 messages in 60 s, relax the gate
   to a throughput floor (e.g. `throughput_msg_per_sec >= 100`) rather than
   demanding 100 % delivery within the time budget.

4. **Postgres leg not yet exercised under full load.**  The smoke tests run
   only SQLite by default.  The first nightly full-scale run should include
   `STORE_BACKEND=postgres` to measure the Postgres WAL path.

5. **llm-transparency WARN logs.**  The smoke run emits WARN lines like
   `llm-transparency: lookup message ID for record: store: not found`.  These
   come from the LLM transparency subsystem attempting to match an outbound
   transparency record for every delivered message; because load test messages
   are injected without going through the outbound queue, no transparency
   records exist.  The WARNs are noise at load-test scale but indicate a
   real behaviour: if the transparency subsystem adds per-delivery overhead
   at load scale, it will appear in the CPU profile.  No action needed now.
