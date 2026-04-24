# Wave 4 — standards review

Date: 2026-04-24. Scope: all code committed up through the Wave 3 boot-wiring commit.

Method: read-through of `internal/*`, `plugins/*`, `cmd/herold`, `test/*`, plus `go build`, `go vet`, `gofmt -l`, `staticcheck`, and `go test -race -count=1 ./...` (all green on the local sqlite path; postgres integration skipped locally — see Blocking #3). `STANDARDS.md` §1–§12 was the rubric.

## Blocking findings (must resolve before Phase 1 exit)

- `internal/admin/cmd_bootstrap.go:112-128` — rule §9 Security / rule §6 correctness — `generateAPIKey()` stores the **plaintext** API key in `APIKey.Hash` (`hash = plain`). `internal/protoadmin/auth.go:75-88` verifies presented tokens by SHA-256 hashing them and comparing against `APIKey.Hash`. Keys minted by `herold bootstrap` therefore never authenticate against the admin REST API — the primary bootstrap path is functionally broken, and the DB row doubles as a plaintext credential at rest. Fix: replace line 126 with `hash = protoadmin.HashAPIKey(plain)` (or extract the helper to a shared package). Blocking because (a) the bootstrap CLI flow cannot work end-to-end, and (b) STANDARDS §9 forbids storing authentication material unhashed.

- `internal/admin/server.go:284-313` + every protocol subsystem — rule §7 Observability / REQ-OPS-90/92 — `observe.Registry` is constructed, `MetricsHandler` is mounted on `/metrics`, but **no subsystem ever calls `observe.MustRegister`**. A live `/metrics` scrape on a Wave 3 build returns only default Go runtime/process collectors (and not even those: `observe.Registry` is explicitly *not* `prometheus.DefaultRegisterer`, and no runtime collector is registered with it). Every `herold_<subsystem>_*` metric promised in comments (`internal/storefts/worker.go:127` documents `herold_fts_indexing_lag_seconds` but never registers it) is absent. Fix: land the first wave of counters/gauges (SMTP session count, IMAP session count, delivery accept/reject, FTS lag, plugin up, audit-log append) before Phase 1 ships.

- `.github/workflows/ci.yml:72` vs `internal/storepg/storepg_test.go:25,83,96` — rule §1.8 / §8.4 — CI exports `HEROLD_TEST_PG_DSN` but the storepg test suite reads `HEROLD_PG_DSN`. `TestCompliance`, `TestMigrationIdempotency` always call `t.Skip("HEROLD_PG_DSN not set; ...")` in CI. Net effect: Postgres parity is unexercised on every PR despite a running `postgres:16` service container. STANDARDS §1.8 — "Code that works on only one is not mergeable" — is not actually enforced. Fix: read both env names, or align CI on `HEROLD_PG_DSN`.

- `internal/protosmtp/*.go`, `internal/protoimap/*.go`, `internal/sasl/*.go` — rule §8.2 — no `go test -fuzz` targets exist for the SMTP command parser, the IMAP parser (`parser.go`, `parser_fetch.go`, `parser_store_search.go`, 1 294 LOC of hand-written tokenizer), or the SASL mechanism parsers. STANDARDS §8.2 is explicit: "Every wire parser has a fuzz target." Current fuzz coverage is limited to `mailparse`, `maildkim`, `maildmarc`, `mailspf`, `mailarc`, `sieve`, `spam`, `sysconfig` (see coverage map). Fix: add `Fuzz*` entrypoints against the parsers before shipping Phase 1.

- `internal/admin/server.go:540-564` — rule §5 — listener serve goroutines (`go func() { _ = smtpServer.Serve(...) }()` for smtp/smtp-submission/imap/imaps/admin) are fire-and-forget: no `sync.WaitGroup` / `errgroup` tracking, no documented shutdown registration, no error propagation. In practice `Close()` on each protocol server drains its own sessions, but the outer `StartServer` has no way to know a listener goroutine has exited, to log its terminal error, or to fail startup when one dies early. STANDARDS §5 "Every background goroutine [must have] registration with the server's lifecycle manager so SIGTERM drains it." Fix: wrap with an `errgroup`, log the returned error from each `Serve`, and include them in the drain-on-cancel path.

- `internal/admin/server.go:173` — rule §5 "Deadlines on every network call" — `directoryoidc.New(...)` is handed `http.DefaultClient`, which has no timeout. OIDC discovery (`/.well-known/openid-configuration`) and JWKS fetches run over this client and can hang indefinitely against a slow IdP. Fix: pass an `&http.Client{Timeout: 10*time.Second}` (the OIDC integration tests already do this — `directoryoidc/rp_test.go:176`). Small diff, blocking because it is the only external I/O in the auth hot path.

- `internal/admin/server.go:286-313` — rule §5 — the metrics HTTP server is launched via bare `go func() { _ = srv.Serve(ln) }()` with no WaitGroup; `metricsShutdown` is called from defer but the goroutine it services is never waited on. Same pattern as Blocking #5; trivial fix once #5 is in.

## Non-blocking findings (should resolve before v1.0)

- `internal/admin/server.go:277-282` — FTS worker goroutine is the only one correctly bounded by a WaitGroup; the asymmetry with the listener/metrics goroutines above (Blocking #5/#7) is confusing.
- Eighteen `staticcheck` findings (`U1000` unused funcs/fields, `S1011`/`S1017` append idioms, `ST1012` sentinel-name). Zero are correctness bugs but STANDARDS §2 says "staticcheck must pass clean." Most notable: `internal/mailparse/errors.go:124` (`newError` dead) and `internal/protoimap/session.go:46,61,62` (`startTLSFn`, `lastSeenSeq`, `subscribedToChangeFeed` fields unused — implies incomplete STARTTLS / change-feed flows the tests do not exercise).
- `internal/protoimap/session_store_search.go:515-584` — IDLE change-feed is **polled at 200 ms** with no way to push updates. The polling loop is tied to `clk.After`, so it is testable, but it is wasteful and ignores the change feed's `ReadChangeFeed` cursor contract (it reloads the selected mailbox on every tick instead of streaming from a durable cursor). Acceptable for Phase 1 behaviour; revisit when NOTIFY (REQ-PROTO-34) lands in Phase 2.
- `internal/plugin/backoff.go:33` — `rand.New(rand.NewSource(time.Now().UnixNano()))`. Violates §5 wall-clock injection and §8.5 determinism (also flagged in the security review as severity-low). Inject via `clock.Clock` or use `crypto/rand` seeded PRNG.
- `internal/storesqlite/metadata.go:1109`, `internal/storepg/metadata.go:993` — `newUIDValidity` uses `math/rand.Uint32()` from the global source. Same §5/§8.5 issue; low-harm since UIDValidity is opaque, but non-deterministic under `go test`.
- `internal/admin/server.go:618-650` — `firstPluginOfType` and `resolvePluginOptions` are fine helpers but live in `admin/server.go`; consider a `plugin/config.go` home so `admin` does not grow a plugin-specific policy layer.
- `go.mod` declares Go 1.25.0, but CI (`.github/workflows/ci.yml:17`) pins `GO_VERSION: "1.23.x"`. STANDARDS §2 says "Go 1.23 as the floor at planning time; bump to the current stable at each phase kickoff." Either set the go.mod directive to 1.23 or upgrade CI to 1.25.
- `go.mod:13` — `github.com/mattn/go-sqlite3 v1.14.42` is a **direct** dependency (used only under `//go:build spike && sqlite_mattn` in `storesqlite/bench_driver_mattn_test.go`). Because the import is build-tag-gated, `go mod tidy` pulls it into the main module's direct list anyway. STANDARDS §1.12 / §3 are ambivalent here (a cgo build-tag is explicitly allowed "for benchmarking"); worth a `// benchmark-only` note next to the require line.
- `go.mod` direct-dep count is 19 — well under the 50-cap. No licence or freshness concerns in direct deps; indirect `github.com/pkg/errors` (archived) and `github.com/cention-sany/utf7` (2017, untouched) come via `enmime` and are outside our control until we fork per STANDARDS §3.
- `internal/protoadmin/doc.go` — one-line doc. STANDARDS §4 "Every package has a `doc.go` with a one-paragraph package comment." It's a paragraph but contains no REQ IDs; compare `internal/protoimap/doc.go` which cites `REQ-PROTO-20..31`. Low-harm style drift.
- `internal/protoevents`, `internal/protojmap`, `internal/protomanagesieve`, `internal/protosend`, `internal/protowebhook`, `internal/queue`, `internal/acme`, `internal/autodns` are empty placeholder packages (just `doc.go`). Deliberate per phasing; not a finding, but the package-existence audit should record them as "not-yet" rather than "partial" (see REQ-ID trace).
- `internal/admin/cmd_server.go:164` — PID file written at 0o644; world-readable PID is conventional but `/var/run/herold.pid` typically uses 0o644 under a dedicated user. Acceptable.
- `internal/protoadmin/middleware.go:25` (`requestID`) is exported-via-context but callable only from inside this package and currently has no callers. Either remove or use.
- `internal/protoimap/server.go:33-36` — `MaxConnections` default 0 = unlimited, no per-IP cap (security review also flagged this). Low-severity; add a cap before Phase 1 ships.
- `internal/sieve/parse.go:517` — staticcheck S1017 (`strings.TrimSuffix` idiom).
- `internal/storesqlite/fts.go:121-133` — five `S1011` spread-append findings.

## Questions / needs clarification

- Are the empty Phase 2 packages (`protoevents`, `protojmap`, `protomanagesieve`, `protosend`, `protowebhook`, `queue`, `acme`, `autodns`) intended to pass Wave 4 as-is? They have `doc.go` only and no tests. STANDARDS §4 "New packages under `internal/` only" is satisfied, but §8 "every non-trivial function has unit tests" is trivially satisfied by having no functions. Confirm the pre-landing package-squatting is the intent (it does catch import-cycle regressions early).
- REQ-EVT / REQ-HOOK / REQ-SEND are clearly Phase 2 per `docs/implementation/02-phasing.md`; should they be tracked in `docs/requirements/13-events.md` etc. with a "deferred-to-Phase-2" annotation so they don't look like orphans in the REQ trace?
- SASL channel binding: the security review flagged `-PLUS` as advertised-but-impossible. From a standards viewpoint, STANDARDS §1 rule 10 ("Wire extensions are advertised only if implemented") applies. Is the resolution (a) drop `-PLUS` from the capability list or (b) plumb `tls-server-end-point`? The two reviews should agree on the framing.
- CI Postgres DSN env name (Blocking #3): assuming this is a typo, confirm; if the intent was to run Postgres under a separate orchestrator, document that and replace the skipped suite with something that actively enforces backend parity.
- Metric taxonomy: are the metric names already nailed down somewhere, or is the first agent to land one free to pick `herold_smtp_sessions_active` etc.? The security review pointed out `/metrics` is empty; an owner needs to land the taxonomy or we will end up with inconsistent names across subsystems.

## Observations (no action required)

- `internal/store/store.go` — typed repository per STANDARDS §1 rule 7. No `Get/Put/Scan(key)` leakage. Method surface is ctx-first, named like the domain (`GetPrincipalByEmail`, `InsertMessage`, `ReadChangeFeed`).
- `internal/clock/clock.go` — `Clock` interface + `Real` + `FakeClock`. Injected through every subsystem constructor. Good.
- `internal/observe/secret.go` — slog handler strips secret-looking keys (security review confirmed this is tight).
- `internal/sysconfig` — no `Save`/`Write` function on `Config`. STANDARDS §1 rule 9 (system.toml not mutated at runtime) is respected.
- No `util/`, `common/`, `helpers/` packages anywhere. STANDARDS §4.
- No `time.Sleep` in non-test production paths. STANDARDS §5.
- No `go func()` loops that spawn unbounded goroutines without a semaphore or per-session budget. SMTP has a `connSem` plus per-IP map; IMAP has `sem` + `wg`; plugin supervisor has a bounded per-plugin lifecycle goroutine.
- Every wire-protocol handler with a session loop (SMTP at `session.go:135`, IMAP at `server.go:200`, admin at `middleware.go:92`) has a top-level recover. STANDARDS §6.
- Argon2id for password hashing (`internal/directory/password.go:38`). STANDARDS §9.
- TLS config via stdlib, TLS 1.2+, Mozilla Intermediate suites (`internal/tls/tls.go:103-110`). STANDARDS §9.
- 16 MiB frame cap on JSON-RPC, `DisallowUnknownFields` on decode, newline-delimited framing (`internal/plugin/codec.go:16`). STANDARDS §9 + architecture 07.
- Plugin process-boundary: first-party plugins live in `plugins/herold-*` as separate main packages. No in-process loader, no `plugin.Open`, no cgo-loaded .so. STANDARDS §1 rule 2.
- `cmd/herold/main.go` is 18 lines and delegates to `internal/admin`. Single-binary invariant (STANDARDS §1 rule 1) holds.
- `go build ./...` and `go vet ./...` are clean. `gofmt -l .` is clean. `go test -race ./...` is green on the default (sqlite) path.

## Coverage map

| package | tests present | fuzz target | note |
|---|---|---|---|
| `internal/acme` | n | na | empty package (Phase 2) |
| `internal/admin` | y | n | integration tests only; no end-to-end API-key round-trip |
| `internal/appconfig` | y | n | |
| `internal/autodns` | n | na | empty package (Phase 2) |
| `internal/clock` | y | n | |
| `internal/directory` | y | n | CRUD + rate-limit tests |
| `internal/directoryoidc` | y | n | link/unlink/verify |
| `internal/mailarc` | y | y | |
| `internal/mailauth` | y | n | Resolver fake in use |
| `internal/maildkim` | y | y | |
| `internal/maildmarc` | y | y | |
| `internal/mailparse` | y | y | |
| `internal/mailspf` | y | y | |
| `internal/observe` | y | n | unit tests, no metric-registration assertions (because there are none to assert) |
| `internal/plugin` | y | n | **no fuzz on JSON-RPC codec**; STANDARDS §8.2 |
| `internal/protoadmin` | y | n | **no fuzz target**; STANDARDS §8.2 |
| `internal/protoevents` | n | na | empty (Phase 2) |
| `internal/protoimap` | y | **n** | **no fuzz target on parser**; parser is 1 294 LOC of hand-written tokenizer |
| `internal/protojmap` | n | na | empty (Phase 2) |
| `internal/protomanagesieve` | n | na | empty (Phase 2) |
| `internal/protosend` | n | na | empty (Phase 2) |
| `internal/protosmtp` | y | **n** | **no fuzz target on command parser** |
| `internal/protowebhook` | n | na | empty (Phase 2) |
| `internal/queue` | n | na | empty (Phase 2) |
| `internal/sasl` | y | **n** | **no fuzz target** on mechanism parsers |
| `internal/sieve` | y | y | |
| `internal/spam` | y | y | |
| `internal/store` | y | n | |
| `internal/storeblobfs` | y | n | |
| `internal/storefts` | y | n | |
| `internal/storepg` | y | n | **integration suite silently skipped in CI** — see Blocking #3 |
| `internal/storesqlite` | y | n | |
| `internal/sysconfig` | y | y | |
| `internal/testharness` | y | n | harness only |
| `internal/tls` | y | n | |
| `plugins/herold-dns-*` | n | na | 15-line stub `main` each |
| `plugins/herold-echo` | n | na | 78-line demo |
| `plugins/herold-events-nats` | n | na | 15-line stub |
| `plugins/herold-spam-llm` | y | n | |
| `plugins/sdk` | y | n | |

Gaps that matter: **protosmtp, protoimap, protoadmin, sasl, plugin codec** — all wire parsers — have no fuzz targets. Per STANDARDS §8.2 this blocks Phase 1 exit.

## REQ-ID trace

Two notes on this matrix:
1. Code-side REQ citations are sparse (63 unique IDs cited in Go source vs 394 Phase-1-scope IDs in `docs/requirements/`). The evidence for "implemented" is often a function that does the thing, not a `// REQ-*` comment. The trace below is based on reading the code, not on greppable tags.
2. Many Phase 2 REQs are deliberately "deferred" per `docs/implementation/02-phasing.md`. They are marked `deferred` not `not-yet`.

Legend: **I** = implemented, **P** = partial, **D** = deferred to Phase 2+, **N** = not-yet (planned for Phase 1 but missing).

### REQ-PROTO (`docs/requirements/01-protocols.md`)

| REQ | status | note |
|---|---|---|
| REQ-PROTO-01..09 (SMTP listener shapes, ESMTP EHLO surface, SIZE, STARTTLS, 8BITMIME, SMTPUTF8, PIPELINING, REQUIRETLS, BDAT/CHUNKING) | I | `internal/protosmtp/session.go` + `server.go`; test coverage in `server_test.go` |
| REQ-PROTO-10..14 (DSN / ENHANCEDSTATUSCODES) | P | reply codes are enhanced in places but not comprehensively; no DSN emission on delivery failure in Phase 1 (delivery is local) |
| REQ-PROTO-15 (RBL hook at CONNECT) | I | `session.go:149-157`, cited in code |
| REQ-PROTO-20..31 (IMAP core, UIDPLUS, ESEARCH, IDLE, basic SEARCH, STATUS, NAMESPACE, ID, ENABLE) | I | `internal/protoimap` — cited in doc.go |
| REQ-PROTO-32..33 (CONDSTORE, QRESYNC) | D | Phase 2 |
| REQ-PROTO-34 (NOTIFY) | D | Phase 2 |
| REQ-PROTO-40..52 (JMAP Core / Mail) | D | Phase 2 |
| REQ-PROTO-60..68 (ManageSieve) | D | Phase 2 |
| REQ-PROTO-70..79 (Sieve interpreter) | P | `internal/sieve` exists with parser + interpreter + tests; integration into delivery pipeline is wired (`protosmtp/deliver.go`). Specific extensions (`mailbox`, `mailboxid`, `spamtestplus`) need verification per extension |
| REQ-PROTO-80+ | D | |

### REQ-AUTH (`docs/requirements/02-identity-and-auth.md`)

| REQ | status | note |
|---|---|---|
| REQ-AUTH-10 (principal model + password hashing) | I | Argon2id |
| REQ-AUTH-20..23 (TOTP enrollment + verify) | I | `internal/directory/totp.go` |
| REQ-AUTH-50..58 (SASL PLAIN, LOGIN, SCRAM-SHA-256) | P | PLAIN, LOGIN, SCRAM-SHA-256 work; **SCRAM-SHA-256-PLUS advertised but non-functional** (see security review) |
| REQ-AUTH-62 (OAUTHBEARER) | P | code path present; audience-confusion risk per security review |
| REQ-AUTH-70..99 | D | app passwords, WebAuthn — Phase 2/3 |

### REQ-STORE

| REQ | status | note |
|---|---|---|
| REQ-STORE-10..12 (metadata repo, typed surface, dual backend) | I | |
| REQ-STORE-20..25 (download rate limits) | I | `internal/protoimap/ratelimit.go` + IMAP FETCH throttle test |
| REQ-STORE-50..66 (quotas, FTS, change feed) | P | change feed + FTS worker wired; quotas enforced in `InsertMessage`; FTS attachment extractor for PDF/DOCX/XLSX likely simplified vs spec |
| REQ-STORE-70+ | D | backup/restore tool, migration tool — Phase 2 |

### REQ-FILT (`docs/requirements/06-filtering.md`)

| REQ | status | note |
|---|---|---|
| REQ-FILT-05..13 (spam classifier plugin invocation, X-Spam-Score headers) | I | `internal/spam/classifier.go`; `plugins/herold-spam-llm` |
| REQ-FILT-30..41 (Sieve core + common extensions) | P | parser + interpreter land; per-extension conformance unverified |
| REQ-FILT-52..80 | P | `body`, `envelope`, `variables`, `vacation`, `mailbox`, `mailboxid` referenced in code; coverage varies |

### REQ-OPS (`docs/requirements/09-operations.md`)

| REQ | status | note |
|---|---|---|
| REQ-OPS-01..06 (system.toml parsing, strict unknown-keys, reload) | I | `internal/sysconfig` + `admin.ReloadConfig` |
| REQ-OPS-20..32 (app-config dump/load, bootstrap) | I / P | dump/load present; bootstrap broken per Blocking #1 |
| REQ-OPS-41 (SIGHUP reload) | I | `admin/server.go:329-353` |
| REQ-OPS-72..84 (graceful shutdown, sd_notify) | I | |
| REQ-OPS-90..92 (metrics exposition) | **N** | `/metrics` mounted, no metrics registered — Blocking #2 |
| REQ-OPS-100..111 (schema migrations, forward-only, audit log) | I | `storepg/storepg.go:200-210` rejects downgrade |
| REQ-OPS-130 (audit log read API) | I | `protoadmin/server_endpoints.go` handles `/api/v1/audit` |

### REQ-ADM (`docs/requirements/08-admin-and-management.md`)

| REQ | status | note |
|---|---|---|
| REQ-ADM-01..06 (principal/domain/alias CRUD via REST) | I | `protoadmin` |
| REQ-ADM-03 (bootstrap) | P | REST bootstrap OK; CLI bootstrap broken (Blocking #1) |
| REQ-ADM-10 (API key rotation) | I | |
| REQ-ADM-100..102 (problem-details error format) | I | `protoadmin/problem.go` |
| REQ-ADM-200 (web UI) | D | Phase 2 |

### REQ-NFR (`docs/requirements/10-nonfunctional.md`)

| REQ | status | note |
|---|---|---|
| REQ-NFR-01 (100 msg/s sustained, 1 k IDLE) | N | not load-tested; Phase 2.5 per phasing |
| REQ-NFR-100 (reproducible build) | P | Makefile uses `-trimpath -buildvcs=true` |

### REQ-PLUG (`docs/requirements/11-plugins.md`)

| REQ | status | note |
|---|---|---|
| REQ-PLUG-05..10 (JSON-RPC stdio, out-of-process) | I | |
| REQ-PLUG-20..21 (manifest, ABI version, handshake) | I | `internal/plugin/protocol.go`, `supervisor.go` |
| REQ-PLUG-33 (signal-driven shutdown, health, restart backoff) | I | `supervisor.go` + `backoff.go` |

### REQ-SEC

| REQ | status | note |
|---|---|---|
| REQ-SEC-36, 37 (TLS 1.2 floor, Mozilla Intermediate) | I | `internal/tls/tls.go` |
| remainder | P | see security review for gaps |

### REQ-FLOW

| REQ | status | note |
|---|---|---|
| REQ-FLOW-03..22 (mail-auth verification pipeline, DMARC evaluation, ARC signing/verify stub) | P | verify path in place; ARC is structural-only per security review |

### REQ-EVT / REQ-HOOK / REQ-SEND

All **D** — Phase 2 per phasing doc.
