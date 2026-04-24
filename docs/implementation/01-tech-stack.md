# 01 — Tech stack

*(Revised 2026-04-24: language changed from Rust to Go. Postgres promoted to first-class backend alongside SQLite. Plugins are child processes. NATS default for event-publisher plugin.)*

## Language: Go

Picked. Rationale in `../README.md` and the language-revisit discussion in conversation history. In short:

- Compile times in the 1–5 s range for clean builds; sub-second incremental. This is the decisive factor.
- Standard library covers: `net/smtp`, `net/mail`, `net/http`, `crypto/tls`, `crypto/ed25519`, `encoding/json`, `context` — cohesive and long-maintained.
- Goroutines handle 1k concurrent IMAP IDLE without thread-pool gymnastics.
- Mature email-adjacent libraries (emersion's go-smtp, go-imap v2, go-msgauth) under permissive licenses we can fork if they stop being maintained.
- Single static binary, clean cross-compilation, `pprof` built in.
- We're writing our own libraries for anything that matters — Stalwart's richer Rust crate ecosystem was a reason for Rust; we've given that reason up.

Go version baseline: **Go 1.23** at time of planning. Use whatever is current at v1.0 release.

## Runtime and concurrency

- Goroutines for concurrency. No external runtime.
- `context.Context` propagated through every async boundary.
- `errgroup` + `semaphore` from `golang.org/x/sync` for bounded concurrency.

## Project layout

Single Go module. Subdirectories are packages.

```
herold/
  go.mod
  go.sum
  cmd/
    herold/              # main binary: server + CLI
  internal/
    store/                 # metadata + blob + FTS abstraction
    storesqlite/
    storepg/
    storeblobfs/
    storefts/              # Bleve wrapper
    protosmtp/
    protoimap/
    protojmap/
    protomanagesieve/
    protoadmin/
    protosend/             # HTTP send API
    protowebhook/          # incoming-mail webhook dispatcher
    protoevents/           # event publisher dispatcher (talks to plugins)
    directory/             # internal directory only (LDAP out of scope)
    directoryoidc/         # per-user OIDC federation (RP only)
    mailparse/
    maildkim/
    mailspf/
    maildmarc/
    mailarc/
    sieve/
    spam/
    queue/
    tls/
    acme/
    autodns/               # publishes DKIM/MTA-STS/TLSRPT/DMARC/DANE via DNS plugin
    plugin/                # supervisor, JSON-RPC client
    observe/
    sysconfig/
    appconfig/
    admin/
  plugins/
    herold-dns-cloudflare/
    herold-dns-route53/
    herold-dns-manual/
    herold-spam-llm/
    herold-events-nats/
  deploy/
    debian/
    rpm/
    docker/
    k8s/
  docs/
  test/
    interop/
    e2e/
```

All `internal/` packages compile into the single `cmd/herold` binary. Plugins are separate `main` packages, each producing its own binary.

## Library choices

These are working picks. Some are likely-final; some are "pick between these two at phase 0 after a spike."

### HTTP

- **`net/http` stdlib** primary. Routing via **`github.com/go-chi/chi/v5`** (small, stdlib-compatible, ages well).
- Rejected: `gin` (middleware wrappers around stdlib we don't need), `echo` (same), `fasthttp` (non-stdlib HTTP impl, ecosystem fragmentation, unnecessary given our modest throughput).

### TLS

- **`crypto/tls`** stdlib. Covers our needs cleanly.

### SMTP

- Client: **`net/smtp`** stdlib for basics, extend where needed (STARTTLS reuse, pipelining, BDAT). Likely fork/reimplement to get clean control.
- Server: **write our own SMTP state machine**. `emersion/go-smtp` is a reasonable reference implementation we may study; we're writing ours to ensure clean SMTPUTF8, BDAT, DSN, REQUIRETLS handling without working around a library's assumptions.

### IMAP

- **Write our own.** `emersion/go-imap/v2` is the best open-source Go IMAP library and we'll study it for design. CONDSTORE/QRESYNC correctness is load-bearing and we want full ownership. Forking + cleaning up vs. writing from scratch — decide at phase 1 kickoff after a spike.

### JMAP

- **Write our own.** No mature Go JMAP server library exists. We implement against RFC 8620 + 8621 directly, informed by Cyrus's and Fastmail's public behaviors.

### Sieve

- **Write our own** parser + interpreter. Language is small enough (RFC 5228 + the extension set we care about); sandboxing requires control of the execution model. Test against Pigeonhole's corpus.

### ManageSieve

- **Write our own.** Trivial protocol. Small effort.

### Email auth: DKIM, SPF, DMARC, ARC

- `emersion/go-msgauth` is MIT-licensed, widely used, covers all four. Use it directly, subject to a code audit on integration. If problems arise, fork + clean.
- Cryptographic primitives via stdlib (`crypto/rsa`, `crypto/ed25519`, `crypto/sha256`).

### MIME parsing

- `net/mail` stdlib is limited. `jhillyerd/enmime` or write our own — start with `enmime` for phase 1, revisit if we hit correctness issues on edge-case messages.

### DNS

- **`github.com/miekg/dns`** — the standard serious Go DNS library. Handles TXT, MX, TLSA, DNSSEC.

### SQLite

- **`modernc.org/sqlite`** (pure Go, no CGO, no system SQLite dependency). Slower than CGO-based `mattn/go-sqlite3` but no build complexity and easier cross-compilation. At 100 msg/s, the performance delta doesn't matter.
- Alternative: `mattn/go-sqlite3` if benchmarks surprise us.

### PostgreSQL

- **`github.com/jackc/pgx/v5`** — the de-facto serious Postgres client in Go. Connection pooling, binary protocol, LISTEN/NOTIFY support (which we'd use for state-change fanout across nothing in v1, but available).

### Full-text search

- **`github.com/blevesearch/bleve/v2`** — Go-native full-text search. Analogous to Tantivy/Lucene. Has its quirks (older API, less performant than Tantivy) but mature and pure Go.
- For attachment text extraction:
  - PDF: `github.com/ledongthuc/pdf` or `rsc.io/pdf`.
  - DOCX/XLSX/PPTX: unzip + XML extract; one small dependency or write our own (file formats are simple).
  - Plain text / HTML: stdlib + `golang.org/x/net/html`.

### CLI

- **`github.com/spf13/cobra`** — mature, ubiquitous.
- Or `github.com/urfave/cli` as lighter alternative.

### Logging

- **`log/slog`** stdlib (structured logging, added in Go 1.21). JSON handler built in.

### Metrics

- **`github.com/prometheus/client_golang/prometheus`** — standard.

### Tracing

- **`go.opentelemetry.io/otel`** for OTLP export, optional.

### Config

- **`github.com/pelletier/go-toml/v2`** for TOML parsing. Strict mode available.

### Password hashing

- **`golang.org/x/crypto/argon2`** — stdlib-grade, maintained.

### TOTP

- **`github.com/pquerna/otp`**.

### JWT / OIDC

- **`github.com/coreos/go-oidc/v3`** + **`golang.org/x/oauth2`** for OIDC RP side.
- **`github.com/golang-jwt/jwt/v5`** for signing/verifying tokens we issue (session tokens).

### NATS (for default event-publisher plugin)

- **`github.com/nats-io/nats.go`** — official client.

### UUIDs / IDs

- **`github.com/google/uuid`** for UUIDv7.
- Or internal snowflake-style IDs (small helper).

### Testing

- Stdlib `testing` + `testing/quick` (property tests).
- **`github.com/stretchr/testify`** for assertions (optional; some teams resist).
- **`testing.T.Run` + `fuzz`** — Go 1.18+ native fuzzing for parser targets.
- **`github.com/testcontainers/testcontainers-go`** for Postgres integration tests.

### Build / release

- Plain `go build`. Cross-compilation via `GOOS=linux GOARCH=arm64 go build` etc.
- Reproducible builds via `-trimpath` + `-buildvcs=true` + pinned toolchain.
- Release artifacts signed (sigstore/cosign).
- Go's linker produces binaries around 15–30 MB.

## Plugin binaries

Each plugin is a separate Go `main` package producing its own binary. Plugins share a small **plugin SDK** library (internal or published module) that implements:
- JSON-RPC 2.0 over stdio.
- Handshake, configure, health, shutdown.
- log/metric/notify callbacks to the server.

Writing a plugin in Go is then ~50 lines of boilerplate + business logic. Plugins in other languages (Python, Rust, shell) use the JSON-RPC contract directly.

## Feature flags / build tags

Go's equivalent of Cargo features. We'll use a small number of **build tags** to exclude heavy dependencies when not needed:

- `!postgres` — omits the Postgres driver (keeps binary smaller for SQLite-only users).

Default build includes Postgres.

## Dependency budget

Approximate target: ≤ 50 direct non-stdlib dependencies in `go.mod` for default build. Each new one reviewed for:
- License compatibility.
- Activity (last commit, issue/PR turnaround).
- Vendored diff size.
- Trust (author reputation, audit history).

`go mod tidy` + `govulncheck` in CI.

## Binary size

No hard target. Go binary for a feature-complete mail server lands in the 30–60 MB range with Bleve, Postgres, NATS, and email-auth libraries linked. That's fine for our operator audience.

## License

- Project license: **MIT**.
- All dependencies must be compatible; the dependency review step enforces this (MIT / BSD / Apache-2.0 / ISC are all compatible).

## Out of stack

- No CGO in the default build (keeps cross-compilation simple). `modernc.org/sqlite` gives us CGO-free SQLite; pgx is pure Go; NATS is pure Go.
- No embedded scripting beyond Sieve.
- No Redis, no Memcached.
- No gRPC / Protobuf internally.
- No ORM (`database/sql` + light helpers; we own the SQL).

## Testing infrastructure

Covered in `implementation/03-testing-strategy.md`.

## Why not Rust after all

Summary for posterity:
- Rust compile times dominated the developer-experience discussion.
- Our scale target (100 msg/s peak, 1k mailboxes) doesn't need Rust's runtime advantages.
- We're writing our own libraries for everything that matters (SMTP state machine, IMAP, JMAP, Sieve, plugin SDK) — Rust's crate-ecosystem edge is moot.
- Operator experience (single static binary, cross-compile, `pprof`) is better in Go out of the box.

If someone forks this project for a multi-tenant hosted deployment at scale, Rust might be the right language for that project. For ours, Go.
