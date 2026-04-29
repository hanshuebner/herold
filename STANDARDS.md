# Herold — coding and development standards

Global rules every contributor (human or agent) follows. Authoritative over agent-level prompts; where an agent's instructions conflict with this file, this file wins.

Source of architectural intent: `docs/design/00-scope.md`, `docs/design/server/requirements/`, `docs/design/server/architecture/`, `docs/design/server/implementation/`. This document distills the rules that apply everywhere; it does not duplicate the rationale — read the source docs for that.

## 1. Non-negotiable architectural invariants

These are load-bearing decisions from the requirements and architecture documents. Code that violates them is rejected regardless of other merits.

1. **Single process, single binary.** All server subsystems link into `cmd/herold`. No microservices, no sidecars. (docs/design/server/architecture/01 §Design values 1)
2. **Out-of-process plugins over JSON-RPC 2.0 on stdio.** No in-process plugin loader, no `dlopen`, no Wasm, no cgo-loaded shared libraries. Process boundary is the security boundary. (docs/design/server/architecture/07)
3. **Storage-centric.** Every state change goes through the `store` transaction layer. Protocol handlers compute intents; the store commits them. No bypass paths. (docs/design/server/architecture/01 §Design values 3)
4. **No in-process pub/sub framework.** Cross-subsystem notification is either a direct function call or a durable store change-feed read. Do not introduce `chan interface{}` event buses or reflection dispatchers. External event fanout is the `protoevents` dispatcher talking to event-publisher plugins — that is the only event bus. (docs/design/server/architecture/01 §Design values 4)
5. **`context.Context` on every async boundary.** Every function that performs I/O, blocks, or spawns a goroutine takes `ctx context.Context` as the first parameter. Deadlines and cancellation propagate end-to-end.
6. **Bounded goroutines.** Never spawn unbounded goroutines. Use `golang.org/x/sync/errgroup` and `golang.org/x/sync/semaphore`. Every per-session goroutine has a hard budget (CPU, memory, time) enforceable from the session's `ctx`.
7. **Metadata store is a typed repository, not a raw KV.** Backends (SQLite, Postgres) implement a Go interface of typed methods (`GetPrincipalByEmail`, `InsertMessage`, ...). Do not reintroduce `Get/Put/Scan(key)` in the public surface. (docs/design/server/architecture/01 §Storage)
8. **Both SQLite and Postgres are first-class.** CI runs the full integration suite against both. Code that works on only one is not mergeable.
9. **System config is a TOML file, application config lives in the DB.** `/etc/herold/system.toml` is small, infra-owned, SIGHUP-reloaded. Domains, principals, aliases, Sieve scripts, DKIM keys, spam policy, webhooks, API keys live in the DB and are mutated via admin API/CLI. Never add a feature that writes to `system.toml` at runtime. (docs/design/server/requirements/09 §Config)
10. **Wire extensions are advertised only if implemented.** No stubs, no "feature-flag-off" capabilities. (REQ-PROTO-04 and equivalents.)
11. **No encryption at rest.** Operators use volume-level encryption. Do not introduce SQLCipher, envelope encryption, or per-blob crypto. (NG10, C14)
12. **No CGO in the default build.** Pure-Go SQLite driver (`modernc.org/sqlite`), pure-Go Postgres (`pgx`), pure-Go Bleve, pure-Go NATS. (docs/design/server/implementation/01 §Out of stack)

## 2. Language and toolchain

- **Go 1.23** as the floor at planning time; bump to the current stable at each phase kickoff.
- **`gofmt` + `goimports`** — enforced. A CI diff fails the build.
- **`go vet`, `staticcheck`, `golangci-lint` (subset)** — must pass clean.
- **`govulncheck`** — every PR. Reported CVEs in direct or transitive deps block merge until triaged (fix, upgrade, or explicit waiver with expiry).
- **Race detector** — every CI test run uses `-race`.
- **Reproducible builds** — release binaries built with `-trimpath -buildvcs=true` under a pinned toolchain.

## 3. Dependency discipline

- **≤ 50 direct non-stdlib dependencies** in `go.mod` for the default build (docs/design/server/implementation/01 §Dependency budget).
- Each new direct dependency justified in the PR description: license, activity, author trust, vendored diff size. License must be MIT / BSD-2/3 / Apache-2.0 / ISC.
- Prefer `stdlib > emersion/go-* > other third-party > fork` in that order.
- Forks live under `internal/third_party/<upstream>/` with an upstream-pin comment. Do not vendor without recording provenance.
- No ORM. `database/sql` + light helpers; SQL is code we own.
- No cgo in the default build. A `cgo` build tag may exist for benchmarking but is not shipped.

## 4. Project layout

Follow `docs/design/server/implementation/01-tech-stack.md` §Project layout verbatim. Summary:

```
cmd/herold/              single binary entrypoint (server + CLI merged)
internal/                all non-plugin code; not importable externally
  store, storesqlite, storepg, storeblobfs, storefts
  protosmtp, protoimap, protojmap, protomanagesieve, protoadmin
  protosend, protowebhook, protoevents
  directory, directoryoidc
  mailparse, maildkim, mailspf, maildmarc, mailarc
  sieve, spam, queue, tls, acme, autodns
  plugin, observe, sysconfig, appconfig, admin
  webspa                 Go-side embedder for the Suite + admin SPAs
plugins/                 first-party plugins, each its own main package
web/                     pnpm workspace; in-tree Svelte SPAs
  apps/suite             consumer Suite SPA (mail / chat / cal / contacts)
  apps/admin             operator admin SPA (Phase 2)
  packages/design-system shared tokens + base CSS
test/interop, test/e2e   cross-package scenarios
deploy/                  debian, rpm, docker, k8s
```

- New packages under `internal/` only (until we intentionally publish a library).
- Packages map one-to-one with responsibility areas; do not create `util`, `common`, or `helpers` grab-bags.
- Every package has a `doc.go` with a one-paragraph package comment.
- Public identifiers have doc comments in the Go style (start with the identifier name).

### 4.1 Web workspace (`web/`)

- Stack is locked: Svelte 5 (runes), Vite 6, pnpm 10, Bits UI, Carbon-derived design tokens, IBM Plex. Anything outside this stack is a STANDARDS.md change, not a local choice.
- TypeScript everywhere; no JS-only files.
- npm package namespace is `@herold/*`. Internal workspace deps use the `workspace:*` protocol — never floating versions.
- pnpm install always runs with `--frozen-lockfile` in CI; lockfile bumps are explicit PR commits.
- The `nofrontend` build tag is a hard contract: every Go change to `internal/webspa` MUST compile under both `go build ./...` and `go build -tags nofrontend ./...`. The build-tag split (`embed_default.go` vs `embed_stub.go`) is exercised by the `web` and `test` jobs in `.github/workflows/ci.yml`.
- The committed `internal/webspa/dist/{suite,admin}/index.html` placeholders satisfy `//go:embed` for source-only builds. Real SPA artefacts are produced by `make build-web` and are not committed; `web/apps/*/dist/` and `internal/webspa/dist/*/assets/` are in `.gitignore`.
- Frontend code is content-blind on the wire: it never sends or stores message bodies, addresses, or search queries unencrypted to anything other than the same-origin herold backend.

## 5. Concurrency and I/O

- `context.Context` as the first parameter of every function that performs I/O or may block.
- `errgroup.WithContext` for fan-out with bounded failure semantics. `semaphore.Weighted` for bounded concurrency.
- No background goroutine without (a) a `ctx` it watches, (b) a documented shutdown path, (c) registration with the server's lifecycle manager so `SIGTERM` drains it.
- No `time.Sleep` in production code paths — use `time.NewTimer`/`time.After` with `ctx` selection. Tests use an injected clock.
- No wall-clock reads in deterministic code. Time is injected via a `Clock` interface in `internal/observe` (or equivalent).
- Deadlines on every network call (dial, read, write). No infinite hangs.

## 6. Error handling

- Return errors; do not panic on recoverable conditions. `panic` is reserved for programmer bugs (impossible states).
- Wrap with context using `fmt.Errorf("...: %w", err)`. Never swallow errors silently.
- Sentinel errors declared at package scope; checked with `errors.Is` / `errors.As`.
- Protocol-level errors map cleanly to the protocol's error vocabulary (SMTP reply codes, IMAP tagged NO/BAD, JMAP error types, HTTP status + JSON problem details).
- Every wire-protocol handler has a top-level recover that logs and closes the connection — one panic cannot crash the server.

## 7. Logging, metrics, tracing

- Structured logs via `log/slog`. JSON handler in production. Attach request ID, session ID, principal ID, remote addr.
- **Multi-sink logging is the model.** Operators configure one or more sinks (stderr `console`/`auto` for humans, file `json` for forensics, ...) each with its own level, per-module overrides, and activity filter. See REQ-OPS-80..86 in `docs/design/server/requirements/09-operations.md`. Code MUST NOT assume a single destination, MUST NOT call `slog.SetDefault` outside the bootstrap path, and MUST NOT bypass the configured logger to write directly to stderr (`fmt.Fprintln(os.Stderr, ...)` for diagnostics is a bug).
- **Activity tagging is mandatory.** Every log record emitted from a wire-protocol layer (`protosmtp`, `protoimap`, `protojmap`, `protomanagesieve`, `protoadmin`, `protosend`, `protowebhook`), the queue/delivery path, the plugin supervisor, and the auth/directory layer MUST carry an `activity` attribute drawn from the closed enum `{user, audit, system, poll, access, internal}` defined in REQ-OPS-86. The level chosen must be consistent with the activity: `access` and `poll` default to `debug`; caller-initiated state changes are `user` at `info`; auth and permission events are `audit` at `info` (failures at `warn`). The pre-scoped logger pattern (`log.With("subsystem", "protojmap", "activity", "user")`) is preferred over per-call attrs so activity is uniform across a request's lifecycle. Records emitted from a covered package without an `activity` attribute are a CI failure (REQ-OPS-86a) and a `reviewer` block.
- Prometheus metrics via `prometheus/client_golang`. Metric names `herold_<subsystem>_<what>_<unit>` (e.g., `herold_smtp_sessions_active`). Cardinality reviewed in PRs; no unbounded label values.
- OTLP tracing optional; spans on every wire request, queue operation, plugin invocation.
- A single event emits exactly one log line, zero or more metric updates, at most one span. Log fields and span attributes agree on names and values.
- `MetricsBind` defaults to loopback (`127.0.0.1:9090`). The `/metrics` handler does not perform authentication. If exposed publicly, operators MUST front it with TLS + auth at a reverse proxy. `sysconfig.Validate` emits a `slog` warning when `metrics_bind` resolves to a non-loopback address; the warning is informational (some operators deliberately publish behind a trusted proxy) and does not block startup.

## 8. Testing — full coverage is the standard

The testing strategy in `docs/design/server/implementation/03-testing-strategy.md` is the authoritative rubric. Summary of enforceable rules:

1. **Every non-trivial pure function has unit tests.** "Non-trivial" excludes one-line passthroughs and gofmt-shaped accessors.
2. **Every wire parser has a fuzz target.** Seed corpus under `testdata/fuzz/`. `go test -fuzz -fuzztime=30s` must run clean on CI per PR.
3. **Every state machine transition is exercised.** SMTP, IMAP, JMAP, Sieve parsers and executors are covered by both example-based and property-based tests.
4. **Every integration test runs against both SQLite and Postgres.** A matrix CI job covers both.
5. **Tests are deterministic.** Wall-clock time, real randomness, real DNS, real filesystem paths outside `t.TempDir()` are bugs. Inject `Clock`, `RandSource`, `Resolver`, `FS`.
6. **External conformance suites run in CI** — imaptest (IMAP), scripted interop (SMTP vs Postfix + Exim in Docker), Pigeonhole (Sieve), published DKIM/DMARC/ARC vectors. A red conformance run blocks merge.
7. **Documentation examples are executable tests.** Every CLI example, REST example, config snippet shown in user-facing docs is exercised in a test. Broken docs are bugs.
8. **No test mirrors implementation shape.** Assert behavior against the spec or the operator-visible contract, not the code path.
9. **Coverage is tracked, not gated.** We track `go test -coverprofile` and look at trends; we do not set a percentage gate. Reviewers reject PRs whose new code lacks corresponding tests regardless of the aggregate number.
10. **Flaky tests are bugs.** A flaky test is either deterministic within one commit or deleted. No retry-until-green merges.

## 9. Security

- Every use of `unsafe.Pointer` is justified in a comment and reviewed explicitly. Default target: zero uses outside stdlib-equivalent wrappers.
- No `cgo` in default builds. A `cgo` build tag for optional benchmarking only.
- Input validation on every protocol surface. Size limits, line-length limits, structural limits documented and enforced.
- Password hashing: Argon2id (`golang.org/x/crypto/argon2`). No MD5/SHA1/bcrypt for passwords.
- TLS via stdlib; TLS 1.0/1.1 rejected; Mozilla "intermediate" cipher suites by default.
- Secrets never logged. Field allow-list in structured log fields; `slog` handlers strip known-secret keys.
- No inline secrets in `system.toml`. Env var, file, or external KMS only.
- Every feature parsing untrusted input has a security-review note in the PR description.
- **Env var naming and documentation rules**: `HEROLD_<SUBSYSTEM>_<PURPOSE>` (SCREAMING_SNAKE_CASE); every operator-facing var documented in `docs/user/operate.md`; each classified as Required or Optional with a default. See `docs/design/server/architecture/09-environment-variables.md` for the full design rule.

## 10. Backwards compatibility and versioning

- Plugin ABI version is a hard contract; bump major for breaking changes. Server refuses to load incompatible plugins. (docs/design/server/requirements/11)
- Database schema migrations are forward-only; downgrade is explicitly rejected (REQ-OPS-100).
- Admin REST API versioned in the URL path (`/api/v1/...`). Once v1 ships, v1 is frozen; new behavior is v2.
- Wire protocols conform to the RFCs cited in `docs/design/server/requirements/01-protocols.md`. Deviations are documented with a rationale comment next to the code and a changelog entry.

## 11. Review discipline

- PRs under ~500 net changed lines where possible. Larger changes split by subsystem.
- Every PR: description lists affected REQ IDs (`REQ-PROTO-30`, `REQ-STORE-20`, etc.), threat-model note if wire surface or auth, and the test plan that was run.
- Two reviewer roles: the subsystem reviewer (implementor peer) and the cross-cutting `reviewer` (style + coverage). Security-sensitive PRs additionally require `security-reviewer`.
- No merge without green CI on all matrix jobs.

## 12. What we refuse

- Emojis in code, commits, CLI output, logs, or docs. Plain ASCII.
- Premature abstractions. Three similar lines is fine; an abstraction must earn its keep with a second real caller.
- "Future-proof" interfaces for hypothetical requirements. Add the interface when the second user arrives.
- Clever code in hot paths. Clarity > cleverness; document any deviation.
- Vendored unmaintained dependencies. If an upstream is dead and we depend on it, fork it into `internal/third_party/` with a clean-up plan.
- Long block comments explaining *what* code does. Comments explain *why* only.
