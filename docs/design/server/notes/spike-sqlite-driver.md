# Spike: SQLite driver — `modernc.org/sqlite` vs. `mattn/go-sqlite3`

- **Date**: 2026-04-24
- **Go**: 1.23 (module floor); toolchain 1.23.5 on the host. `modernc.org/sqlite v1.34.4` pinned (the current v1.49.x line requires Go 1.25, which is above the module floor).
- **Driver under test**:
  - `modernc.org/sqlite v1.34.4` — pure Go, no CGO.
  - `github.com/mattn/go-sqlite3 v1.14.42` — CGO binding to amalgamated SQLite 3.
- **Hardware**: Apple M3, 24 GB RAM, macOS darwin/arm64 (Darwin 25.4.0).
- **Harness**: `internal/storesqlite/bench_spike_test.go`, build-tag gated (`//go:build spike`). Driver selection via `sqlite_modernc` / `sqlite_mattn` tags:
  - `CGO_ENABLED=0 go test -tags 'spike sqlite_modernc' -run=^$ -bench=. -benchtime=3s ./internal/storesqlite/...`
  - `CGO_ENABLED=1 go test -tags 'spike sqlite_mattn' -run=^$ -bench=. -benchtime=3s ./internal/storesqlite/...`

All runs on WAL, `synchronous=NORMAL`, `busy_timeout=30000`, `foreign_keys=ON`, `cache_size=-65536`. Schema is the benchmark-shaped `messages(id, mailbox_id, uid, modseq, received_at, size, flags, blob_hash)` with a covering `(mailbox_id, uid)` index — matches the production shape from `docs/architecture/02-storage-architecture.md`.

## Benchmark results

### 1. Single-writer INSERT saturation (one row per transaction)

| Driver   | Saturation (inserts/s) | p50    | p95    | p99    |
|----------|-----------------------:|-------:|-------:|-------:|
| modernc  | 17,066                 |  47 us |  74 us | 133 us |
| mattn    | 17,855                 |  41 us |  52 us |  89 us |

Both drivers saturate two orders of magnitude above the 100 msg/s peak target (REQ, `docs/00-scope.md`). Mattn is ~5 % faster on throughput and ~30 % tighter on p99. Neither result threatens the workload.

### 2. 32 concurrent readers + single writer at 20 msg/s (FETCH-shaped)

Query: `SELECT id, uid, modseq, size, flags FROM messages WHERE mailbox_id = ? AND uid >= ? ORDER BY uid LIMIT 50` over a seeded 100k-row mailbox.

| Driver   | Reads/s | Writes/s | p50    | p95     | p99     |
|----------|--------:|---------:|-------:|--------:|--------:|
| modernc  |   4,294 |    19.83 | 7.2 ms | 12.0 ms | 14.8 ms |
| mattn    |  12,191 |    19.89 | 0.6 ms |  2.3 ms | 57.2 ms |

The headline gap: mattn delivers ~2.8x reader throughput and ~12x lower p50. Modernc's p99 is smoother (14.8 ms vs. 57.2 ms) because mattn's cgo-transition cost shows up as a long tail under contention; modernc's latency distribution is flatter but slower overall. For IMAP FETCH at our target concurrency (up to 1k IMAP IDLE sessions, but not 1k simultaneous FETCHes on a hot mailbox), both are comfortably inside budget.

### 3. Large scan — full 1M-row mailbox scan, ordered by UID

| Driver   | Rows/s     | Scan time |
|----------|-----------:|----------:|
| modernc  |    984,607 |   1015 ms |
| mattn    |  1,229,295 |    813 ms |

Mattn is ~25 % faster on a full-table sequential scan. For our workload this path is rare: online reindex (REQ-STORE-08), `herold diag fsck` (REQ-STORE-110), rebuilds.

## Binary size — `cmd/herold` with driver linked

Measured with a throwaway `cmd/herold/sqlite_size_check*.go` file, build-tag-gated, deleted after measurement.

| Build                                       | Size    |
|---------------------------------------------|--------:|
| Baseline (no SQLite driver)                 | 2.24 MB |
| `+mattn` (CGO=1, darwin/arm64)              | 5.66 MB |
| `+modernc` (CGO=0, darwin/arm64)            | 8.27 MB |
| `+modernc` (CGO=0, linux/arm64 cross)       | 8.13 MB |
| `+modernc` (CGO=0, linux/amd64 cross)       | 8.52 MB |

The ~2.6 MB modernc premium is modernc's translated SQLite + its `modernc.org/libc` runtime. Well under the 30–60 MB range budgeted in `docs/implementation/01-tech-stack.md §Binary size`. Not a decision factor.

## Cross-compilation posture

`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/herold`:

| Driver  | Build | Runtime |
|---------|:-----:|:-------:|
| modernc |  ok   |  works  |
| mattn   |  ok   |  panics at first query: `"Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work. This is a stub"` |

`GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build ./cmd/herold` from darwin/arm64 without a Linux cross-toolchain:

| Driver  | Result |
|---------|--------|
| modernc | n/a — pure Go, CGO setting irrelevant |
| mattn   | **fails**: `# runtime/cgo … linux_syscall.c: undeclared function 'setresgid'`. Requires musl-cross or zig-cc to cross, with the corresponding toolchain pin in CI. |

This is the tech-stack rule's concrete teeth: mattn breaks the clean `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build` invocation that `docs/implementation/01-tech-stack.md §Out of stack` and `STANDARDS.md §1.12` codify.

## Analysis

The benchmarks are not a surprise in the direction the tech stack worried about: modernc keeps up. Single-writer delivery saturation is 170x the 100 msg/s peak target on either driver; scan speed is within 25 %; concurrent-reader throughput is where mattn wins more materially (~2.8x) but modernc is still at 4,294 reads/s over a live-writer mailbox — well above any realistic IMAP FETCH fan-out at our scale. The one notable concern for modernc was its p95 under reader contention (12 ms), which is visible but inside the p95 ceiling we'd accept for an interactive FETCH. Conversely, mattn's p99 tail (57 ms) is worse than modernc's (15 ms) at the same load — CGO transition cost under contention is real and shows up where IMAP clients would feel it.

The cross-compilation story is decisive. Mattn requires either (a) giving up `CGO_ENABLED=0` in the default build (violates `STANDARDS.md §1.12`), (b) maintaining a musl-cross or zig-cc toolchain in CI for every target triple we ship, or (c) abandoning cross-compilation entirely and building on native runners per platform. Any of the three is a real operational cost. Modernc pays a ~2.6 MB binary tax and loses ~25 % on the cold-scan path we rarely hit; it asks for nothing in return.

## Recommendation

**Stay with `modernc.org/sqlite` as the default build.** The 100 msg/s target is two orders of magnitude below modernc's saturation. The CGO-free cross-compilation win is non-negotiable per `STANDARDS.md §1.12`, and the performance gap on the two paths where mattn wins (concurrent FETCH throughput, full-table scan) is not material at our scale.

Keep `github.com/mattn/go-sqlite3` available behind a `cgo` build tag (as `STANDARDS.md §3` already contemplates: "A `cgo` build tag may exist for benchmarking but is not shipped"). The harness in `internal/storesqlite/bench_spike_test.go` is the first real user of that tag. If a later spike uncovers a workload where modernc's concurrent-reader latency actually hurts real IMAP clients, revisit — but that revisit should be driven by operator-visible pain, not a synthetic benchmark.

No change to `docs/implementation/01-tech-stack.md §SQLite` needed. The current wording ("Alternative: `mattn/go-sqlite3` if benchmarks surprise us") stands; benchmarks did not surprise us.
