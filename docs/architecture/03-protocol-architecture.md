# 03 — Protocol architecture

How wire protocols are served. The shape common to SMTP, IMAP, JMAP, ManageSieve, and the admin HTTP API.

## Listener → session lifecycle

All listeners follow the same structure:

```
  bind(addr) ─► accept-loop ─► per-connection task
                                      │
                               ┌──────┴──────┐
                               │ pre-session │  rate limits, IP filter, PROXY-proto
                               └──────┬──────┘
                                      │
                               ┌──────┴──────┐
                               │ TLS accept  │  optional (implicit / STARTTLS)
                               └──────┬──────┘
                                      │
                               ┌──────┴──────┐
                               │   session   │  per-protocol state machine
                               └──────┬──────┘
                                      │
                               ┌──────┴──────┐
                               │   cleanup   │  audit, metrics, release resources
                               └─────────────┘
```

Each accepted connection spawns a goroutine. The goroutine runs the protocol state machine directly with blocking-looking code; the Go runtime multiplexes goroutines onto its OS-thread pool (roughly `GOMAXPROCS`). No per-protocol worker pool.

## Shared session services

Every session handler is constructed with a handle to shared services:

- `store` — metadata + blob + FTS
- `directory` — auth and lookups
- `queue` — outbound queue handle (submission only)
- `spam` — scoring (delivery path only)
- `certs` — cert store for TLS handshake + SNI
- `observe` — logger/metrics/tracer
- `cfg` — read-only runtime config snapshot

Handles are cheap (reference-counted). Services are shared state; sessions do not own them.

## Per-protocol session state

### SMTP (relay and submission share one implementation)

State machine: `Connected → Greeted → MailFromSet → RcptAdding → DataStreaming → Completed`.

- Buffered reader over the (possibly TLS-wrapped) stream.
- Command parser: line-at-a-time up to DATA; BDAT is length-prefixed.
- Message parser: streaming; chunks to blob writer directly; header parser runs inline and stops at the header/body boundary for content-inspection hooks (DKIM verify uses the header portion early).
- DKIM verification happens incrementally as the body streams in (body hash accumulator).
- On RSET: discard partial envelope; keep TLS + auth.
- On QUIT: half-close gracefully.
- On idle: drop after configurable idle timeout (default 300s).

Submission adds:
- AUTH state before MAIL FROM accepted (unless listener config allows post-MAIL AUTH, which we disallow).
- Post-DATA: message handed to queue (outbound), not to filter+delivery (inbound).

### IMAP

State machine per RFC: `NotAuthenticated → Authenticated → Selected → Logout`.

- Line-at-a-time parser with literal support. Extension to RFC 3501: accept `LITERAL+` non-synchronizing literals.
- Commands run concurrently when server permits (we do NOT in v1 — sequential per session is simpler, and IMAP's IDLE + CONDSTORE are the only places where interactivity matters). A future v2 may add command parallelism.
- Selected mailbox state: UID, MODSEQ, message flag vector — cached per-session, invalidated by store state-change feed.
- IDLE: session registers for state-change notifications; on mailbox change, emits `* N EXISTS`, `* M EXPUNGE`, `* X FETCH`. Heartbeat ensures clients don't time out.
- UTF-7 (mUTF-7) mailbox-name encoding: supported for old clients; new clients use `UTF8=ACCEPT`.
- COMPRESS=DEFLATE after authentication: zlib on both directions.

Per-session memory: current mailbox state vector is the largest item. Bounded by mailbox size; big mailboxes (>500k messages) have a budget and use range-streaming rather than full materialization.

### JMAP

Stateless HTTP requests (with optional push via SSE/WebSocket):

- `POST /jmap` — accepts a request batch, processes each method call, returns response batch. One DB transaction per batch (all-or-nothing or partial per JMAP semantics).
- `GET /jmap/download/…`, `/upload/…` — large blob endpoints.
- `GET /jmap/eventsource?types=…` — SSE push; long-lived HTTP stream. Session registers for state-change feed and emits `StateChange` events per RFC 8620 §7.
- `GET /.well-known/jmap` — session descriptor (capabilities, accounts, URLs).
- `GET /.well-known/webfinger?resource=…` — deferred; for client autodiscovery.

Auth: `Authorization: Bearer <token>` or basic with username+password (app password or primary). Rate-limited per token.

### ManageSieve

Small state machine: `NotAuthenticated → Authenticated`. Commands are text-line-based with quoted strings + literals. Script content uploaded as literal; we run the parser and return errors inline.

### Admin HTTP

Plain REST over HTTPS. Standard Axum/Actix/whatever-framework shape. Endpoints listed in `requirements/08-admin-and-management.md`.

### ACME HTTP-01 challenge listener

Bound on :80 only during challenge solve. Minimal HTTP: serves `/.well-known/acme-challenge/<token>` → `<keyAuth>`. Everything else 404. Can coexist with a redirect-to-HTTPS handler on :80 if the operator wants.

## TLS

One `CertStore` service.

- Holds per-hostname cert bundles + private keys.
- SNI selection for the TLS handshake.
- Rotation: cert updates applied immediately for new handshakes; existing connections unaffected.
- ALPN negotiation where relevant (HTTP/1.1 and h2 for JMAP; no ALPN for SMTP/IMAP — STARTTLS doesn't use it).
- Cert store fed by:
  1. File-based certs (reloaded on file-change notification or SIGHUP).
  2. ACME client (certs go into the store directly on issuance/renewal).
  3. Built-in self-signed (dev only).

STARTTLS mechanics: each protocol has its own cleartext→TLS upgrade. Shared code for the actual TLS handshake; protocol-specific glue for pre-STARTTLS command handling.

## PROXY protocol

For operators running behind a L4 load balancer (HAProxy, Envoy in TCP mode, AWS NLB):

- Per-listener `proxy_protocol = "v1" | "v2" | "off"`.
- When on, the first bytes of the TCP stream are the PROXY header (text v1 or binary v2); we parse it and override the peer address for rate limiting, logging, DNSBL, and SPF's "connecting IP" definition.
- Untrusted source spoofing the header: mitigated by only enabling on listeners that *only* accept connections from trusted front-ends (operator config).

## Rate limiting

Not one algorithm; different shapes per protocol:

- Connection-rate per source IP (token bucket).
- Concurrent connections per source IP (counter).
- Commands per session per minute (token bucket) — prevents CPU-abuse after AUTH.
- Auth attempts per source IP and per target account (see REQ-AUTH-23).
- Submission rate per authenticated principal.
- DATA throughput per session (prevents slowloris in DATA).

Implementation: per-IP state in an in-process LRU with a fixed max size (default 100k entries). IPs evicted on LRU pressure; the cost is "memorized" rate-limit state not surviving eviction; the benefit is bounded memory.

## Protocol testing

Each protocol has a conformance test suite (see `implementation/03-testing-strategy.md`). Handler code written to be driven by test inputs without needing a full server wiring — the protocol package exposes a "process this request, give me the response" function that tests drive directly.

## Backpressure

- Read side: bounded read buffers per connection.
- Work side: semaphore on concurrent expensive operations (message parse + spam scoring + Sieve run). If semaphore exhausted, SMTP returns 4xx (try again), JMAP returns 429, IMAP delays (rare — IMAP work is usually light).
- Write side: if client can't keep up with a server push (e.g. IMAP IDLE untagged responses), buffer up to a cap, then close the connection with `BYE`.

## Concurrency model summary

- One accept loop per listener (itself a goroutine).
- One goroutine per accepted connection.
- One goroutine per scheduled job (queue delivery, ACME renewal, cleanup, report rollup).
- Shared services are shared-memory with `sync.Mutex` / `sync.RWMutex` or channels where needed; most hot state is read-mostly (directory cache, cert store).
- No thread pool at the application layer; the Go runtime's scheduler is the pool.
- CPU-bound work (attachment text extraction, regex, crypto) runs inline on the goroutine that needs it. Go's scheduler preempts cooperatively, so a long CPU burst doesn't starve other goroutines.

## Protocol-specific notable decisions

- **IMAP command pipelining**: we accept pipelined commands but execute sequentially. Revisit if benchmarks show this is limiting.
- **JMAP batched requests**: we process them sequentially per batch. Per-batch atomicity is not guaranteed by spec; we achieve it anyway when the batch is pure-read or pure-set on one account.
- **SMTP 8BITMIME**: we accept 8-bit in, we advertise 8BITMIME out. No conversion to 7bit on delivery (that era is over).
- **CHUNKING/BDAT**: supported because big attachments stream better this way. Our message writer treats BDAT and DATA identically internally.

## What we don't build at the protocol layer

- Milter protocol (OpenMTPC / Sendmail milter) — complex and out of scope.
- LMTP as ingress — delivery is in-process.
- NOTIFY (RFC 5465) in IMAP — complex; IDLE is sufficient for v1.
- JMAP over WebSocket — SSE push is enough; revisit.
