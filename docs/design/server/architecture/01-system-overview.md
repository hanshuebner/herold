# 01 — System overview

How the process is shaped, how the major subsystems communicate, what runs in one binary vs. out of process.

## Top-down view

```
                               ┌─────────────────────────┐
                               │      herold(1)          │
                               │     single process      │
                               └──────────┬──────────────┘
                                          │
       ┌───────────────┬───────────────┬──┴──┬───────────────┬──────────────┐
       │               │               │     │               │              │
  ┌────┴────┐   ┌──────┴──────┐ ┌──────┴─┐  ┌┴────────┐ ┌────┴─────┐ ┌──────┴──────┐
  │Listeners│   │   Workers   │ │Sched/  │  │Director │ │ Storage  │ │Observability│
  │(wire)   │   │(per-protocol│ │Queue   │  │ (auth)  │ │ (meta+   │ │(log/metric/ │
  │         │   │ handlers)   │ │        │  │         │ │ blob+FTS)│ │ trace)      │
  └─────────┘   └─────────────┘ └────────┘  └─────────┘ └──────────┘ └─────────────┘
```

One OS process. One binary. All the boxes above are modules in that process, not services.

## Design values (shaping architecture)

1. **Single-process.** Mail servers are often deployed as Postfix + Dovecot + Rspamd + OpenDKIM + ACME client + cron jobs. We collapse all of that into one supervised unit. The cost is coordination (we build it); the benefit is operator simplicity.

2. **Goroutine-per-session, not OS-thread-per-session.** Every accepted connection is a goroutine. Goroutines start at 8 KB stacks and grow on demand; the Go runtime multiplexes them onto a small pool of OS threads (roughly `GOMAXPROCS`). 1k concurrent IMAP IDLE sessions cost ≈15 MB of stack memory and fractions of a core of scheduler overhead — not 1k OS threads.

   The "async vs threaded" distinction from other languages dissolves here. We write blocking-looking code (`conn.Read`, `db.Query`) and the runtime handles concurrency. What matters in practice:
   - Bound session state (REQ-NFR-10) so memory scales linearly, not quadratically.
   - Use `context.Context` everywhere to propagate deadlines and cancellation.
   - Never spawn an unbounded number of goroutines (use `errgroup` or a semaphore).

3. **Storage-centric.** Every state change goes through the store transaction layer. Protocol handlers compute intents; the store resolves and commits them. This is the invariant that keeps crash-safety tractable.

4. **No in-process pub/sub framework.** Notifications between subsystems are either (a) direct function calls in the same goroutine, or (b) changes to the store that other subsystems observe via a store-level change feed. We do *not* build a generic event bus.

   Why this is the right call:
   - **Traceability.** A direct function call leaves a stack trace. A bus hides the path from publisher to subscriber; debugging "who fired this?" gets painful as subscribers accumulate.
   - **Explicit ordering.** When delivery code calls `stateChanges.Append(...)`, the ordering and transactional scope is obvious. A bus adds delivery semantics (at-most-once? per-subscriber buffers? backpressure?) we'd need to design and test.
   - **Testing.** You can test a module by passing a mock of its direct dependencies. You can't easily test a module that publishes to "the bus" without standing up the bus.
   - **No dropped notifications.** Buses commonly drop events under backpressure; subscribers come and go. Our critical notifications (state-change feed for IDLE/JMAP push) are durable in the store and read by subscribers on their own schedule — no drops.

   Alternatives we considered and rejected:
   - Generic Go channel-based event bus (`chan interface{}` topics with multiple subscribers). Cheap to write; expensive to maintain — grows unbounded in subscribers, introduces ordering bugs.
   - Reflection-based dispatcher (à la Java EventBus). Clever, untraceable, slow.
   - Plugin-visible in-process events. If we want events exposed to external systems, that's what the **event-publisher plugin** system is for (`requirements/13-events.md`). Plugins see a curated, versioned event stream; the server internals don't.

   The exception that proves the rule: the **state-change feed** is a narrow, purpose-built notification channel (per-principal, monotonic seq, persisted in the store, 24h retention). It's not a generic pub/sub; it's a specific datatype that solves the IDLE/JMAP-push problem and nothing else.

5. **Clearly separated data and control planes.** Mail traffic (data plane) is hot, in async workers. Admin API, cert renewal, DMARC report parsing, backup (control plane) run on a separate executor where starvation of the data plane is not a risk.

## Major subsystems

Names here are design names, not Go package names; final package layout is in `implementation/01-tech-stack.md`.

### Listeners

- TCP/TLS accept loops, one per configured listener.
- Per-listener protocol dispatcher hands accepted streams to a per-protocol session handler.
- Per-listener config: bind addr, TLS mode (none/implicit/starttls), PROXY protocol, accept filters.
- Fast rejection path (rate limits, IP deny) runs before TLS accept.

### Protocol sessions (workers)

Per-protocol async task that runs the session state machine:

- `smtp-relay` (25)
- `smtp-submission` (587, 465)
- `imap` (143, 993)
- `jmap` (443 HTTP, sharing with admin if configured)
- `managesieve` (4190)
- `admin-http` (8080)
- `acme-http-challenge` (80) — ephemeral, only during ACME dance

Sessions hold a reference to shared services (store, directory, queue, spam) but do not own them. They're short-lived from the service's perspective.

### Scheduler and queue

- Outbound mail queue: persistent, scanned by delivery workers.
- Delivery worker pool: concurrency caps per-destination, per-total.
- Retry scheduler: picks due items, hands to delivery workers.
- Periodic tasks: DMARC report rollup, TLS-RPT report send, DKIM key rotation reminders, blob GC, FTS index compaction, stats snapshot.

One scheduler coordinates all periodic work. No separate cron mechanism.

**What "in-process scheduler" means vs. systemd timers / cron / Kubernetes CronJobs:**

| | In-process scheduler (ours) | External cron |
|---|---|---|
| Where it runs | Same process as server | Separate process |
| DB access | Direct (shared connection pool) | Needs its own DB credentials |
| Structured logs | Already integrated | Must be plumbed separately |
| Shared cache warmth | Yes | No |
| Correlation with mail events | Trivial (same log stream, same request IDs) | Cross-process join |
| Deployment unit | One binary | Two+ units to monitor |
| Failure mode | Visible in server health | Silent unless you check cron logs |

The tradeoff: we take on responsibility for getting this right (start/stop, observability, catching up after downtime). In exchange, operators monitor one thing.

**How periodic tasks are observed and monitored:**

Every registered periodic task emits:

- Metric `herold_task_runs_total{name, status}` — counter, status ∈ `success` | `error` | `skipped`.
- Metric `herold_task_duration_seconds{name}` — histogram per run.
- Metric `herold_task_last_run_timestamp{name}` — gauge (seconds since epoch).
- Metric `herold_task_next_run_timestamp{name}` — gauge.
- Log line on every run (level = info on success, error on failure), with task name + duration + outcome + request ID.
- Event `task.ran` / `task.failed` through the event-publisher plugin system (REQ-EVT).
- Admin API: `GET /api/v1/server/tasks` lists all registered tasks with last-run, next-run, last-status, recent errors. Surfaced in the web UI.

**Missed runs:** if the server was down when a scheduled run should have occurred, the scheduler catches up on startup (runs it once, flagged as `catch-up` in the metric + log). It does NOT try to run every missed occurrence — one catch-up is enough for our tasks (idempotent summaries, not transactional events).

**Operator-visible failure signals:** task failures 3× in a row trigger a Prometheus alertable condition (`herold_task_runs_total{status="error"}` increment with no success between). Alertmanager integration is the operator's, not ours.

### Directory

- Source of truth for principals: **internal store**.
- External OIDC providers federate in on a **per-user** basis (not a directory backend — see REQ-AUTH-50..58). A user signs in via Google/GitHub/etc. and that auth resolves to their local principal via a stored `{principal_id, provider, sub}` association.
- Answers: "does this principal exist?", "authenticate this credential", "list aliases", "is this email local?", "resolve group → members".
- Caches: hot results (default 30s) to keep token verification off the hot path.

### Storage

Three logical stores; separate because access patterns are different:

- **Metadata store** — structured relational data: principals, mailboxes, messages-in-mailboxes, queue, state-change feed, audit log, app config, webhooks, ACME state. **Backed by SQLite (default) or PostgreSQL**, operator's choice at install. The internal Go interface is a typed repository (`GetPrincipalByEmail`, `InsertMessage`, `UpdateMailboxModseq`, …) not a raw KV — though we colloquially called it "KV" in earlier drafts. The term "KV" is retired; it's the **metadata store**.
- **Blob store** — message bodies on the local filesystem, content-addressed (BLAKE3 hex, 2-level hex fan-out). Dedup across fanout.
- **FTS** — full-text search index via Bleve. Derived from metadata + blob content (including extracted attachment text). Rebuildable.

One logical `Store` handle that every caller goes through; SQLite and Postgres implementations behind it. See `architecture/02-storage-architecture.md`.

### Observability

- Structured logging sink, pluggable format.
- Metrics registry → `/metrics` endpoint.
- Optional OTLP tracer (and optional OTLP log exporter for client events; see `10-client-log-pipeline.md`).
- Health endpoints.
- Browser-side errors and logs from the SPAs are ingested via authenticated and anonymous HTTP endpoints, sanitised, and re-emitted into the same slog + OTLP fan-out as server logs, with a bounded ring buffer for "last N hours" operator visibility. Detail in `10-client-log-pipeline.md`.

All three share a common `span/event` abstraction so that a log line and a trace span for the same event agree on fields and IDs.

### Spam and filtering

- **Spam classifier**: a thin server-side shim that builds a classification prompt (headers + curated body excerpt + auth results) and invokes the spam-classifier plugin (REQ-PLUG, REQ-FILT). The plugin does the actual LLM call or whatever it internally chooses to do. The server doesn't know about models or endpoints directly.
- **Sieve interpreter**: parse + execute + sandbox. Runs post-classification.

The classifier shim is a pure function over inputs; the Sieve interpreter too. The plugin call is the only I/O.

## Cross-cutting: principals, domains, and routing

The system's domain model is simple enough to state completely:

- A **domain** belongs to this server if it's in the configured domains table.
- An **address** `local@domain` resolves via:
  1. Exact match on a principal's canonical address or alias.
  2. Group address (fans out).
  3. Catch-all for that domain.
  4. Unknown → reject or defer.
- A **principal** has mailboxes (folders), credentials, Sieve script, quota, and forwarding rules.

No hidden joins or indirection. The directory implements these lookups; the rest of the system calls the directory.

## What runs outside the process

Optional external dependencies. None required for v1.

- **DNS resolver** — the system resolver (`getaddrinfo`) plus a specialized DNS client for SPF/MX/TLSA/SRV/TXT lookups. DNS is I/O; we do it async inside the process but *the DNS server itself* is outside.
- **ACME CA** — Let's Encrypt or similar. HTTP client inside the process.
- **S3-compatible blob store** — only if operator opts in (phase 2).
- **SIEM / Grafana / Prometheus / Tempo** — consume our outputs.

## What does NOT run outside the process

- Database — SQLite is embedded; Postgres (when chosen) runs as a separate server but is still a data store we manage, not a coordination service.
- Cache (in-process only; no Redis).
- Spam engine (integrated; no external Rspamd).
- DKIM signer (integrated; no OpenDKIM).
- ACME client (integrated).
- Queue broker (no Kafka, no RabbitMQ; our queue is a table in the store).

This is an explicit choice: operator runs one service. Stalwart makes the same choice; we're keeping it.

## Lifecycle

**Startup:**
1. Parse config (fail fast on errors).
2. Open store (run migrations if needed).
3. Verify integrity on opening (lightweight — full fsck is separate).
4. Start observability (so startup errors are logged).
5. Load certs; start ACME if configured; bind admin listener last.
6. Bind listeners; emit `ready` (systemd sd_notify, `/healthz/ready` flips to 200).
7. Start scheduler.

**Running:** steady state. No scheduled restarts.

**Reload (SIGHUP):**
1. Reparse config; if invalid, log error and keep current.
2. Diff: new/removed listeners, changed TLS, changed directory config, changed spam thresholds, changed queue schedule.
3. Apply diff. Listeners re-bind (new connections use new settings); existing sessions complete under old settings.
4. Reload certs from disk.
5. Emit `reload-complete` event.

**Shutdown (SIGTERM):**
1. Stop accepting new connections.
2. Drain in-flight requests up to `shutdown_grace` (default 30s).
3. Flush queue checkpoints; close store cleanly.
4. Exit 0.

**Hard kill (SIGKILL / panic):**
Recovery handled on next start. Data invariants preserved by fsync discipline + transactional writes. No post-crash inconsistencies visible to users.

## What is NOT in the architecture

We want to be explicit about shapes we rejected:

- **Microservices.** Not needed for the problem size. Stalwart doesn't do it; we don't do it.
- **Actor framework (erlang-style).** Goroutines + channels are enough; a formal actor abstraction adds conceptual weight without benefit at our scope.
- **In-process plugin loader (dlopen / Wasm).** We have a plugin system, but plugins are **out-of-process** (see `architecture/07-plugin-architecture.md`). Process boundary = security boundary. No cdylib / Wasm embedded runtime.
- **Multi-node coordination (Raft, etcd, gossip, replication).** Non-goal. Single node, no HA beyond external (hypervisor/ZFS) tricks. No "preserving the option" for multi-node — we're committed.
- **Custom RPC internally.** Module boundaries are Go interfaces; cross-module calls are plain function calls. The only serialization boundary inside the deployment is between the server and its plugin child processes (JSON-RPC on stdio).
- **Traditional spam engine.** No bundled Rspamd-equivalent, no Bayesian, no RBL lookups. Classification is one LLM call per message via the spam-classifier plugin.

## Module boundaries (rough)

Names are logical; final package boundaries in `implementation/01-tech-stack.md`.

- `store` — metadata + blob + FTS abstraction, backend implementations (SQLite, Postgres, FS, Bleve).
- `proto-smtp`, `proto-imap`, `proto-jmap`, `proto-managesieve`, `proto-admin`, `proto-send` (HTTP send API), `proto-webhook` (incoming webhooks), `proto-events` (event dispatcher).
- `directory` — auth backends + plugin chain.
- `queue` — outbound scheduling + delivery.
- `mail` — message parsing, DKIM/SPF/DMARC/ARC, MIME.
- `sieve` — parser + interpreter.
- `spam` — classifier prompt builder + plugin invocation.
- `tls` — cert management, ACME client.
- `acme-dns` — DNS-01 challenge via DNS plugin.
- `auto-dns` — DKIM/MTA-STS/TLSRPT/DMARC/DANE publication via DNS plugin.
- `plugin` — plugin supervisor, JSON-RPC client, manifest validator.
- `observe` — logs, metrics, traces, health.
- `sysconfig` — system config parser + validator.
- `appconfig` — application config access layer (DB-backed).
- `admin` — REST handlers.
- `cli` — `herold` binary entrypoints.

The binary is `herold`; all of the above are libraries (single workspace). Plugins are separate binaries — see `architecture/07-plugin-architecture.md`.

## Suite SPA co-deployment

*(Added 2026-04-26 rev 9: the single-binary single-node target (REQ-NFR-21) extends to bundling the suite SPA as embedded static assets; pairs with the public/admin listener split in REQ-OPS-ADMIN-LISTENER-01..03.)*

- **REQ-DEPLOY-COLOC-01** Herold MUST be capable of serving the suite SPA static assets (HTML, JS, CSS, fonts, images) as a first-class operator-deployment shape. The default packaging embeds the suite build artefacts into the herold binary at release time via `go embed`; an alternative shape `[server.suite].asset_dir = "/path/to/suite/dist"` lets operators point at an external directory for hot-reload during development.
- **REQ-DEPLOY-COLOC-02** The suite SPA is served at the root (`/`) of the public listener (REQ-OPS-ADMIN-LISTENER-01); API endpoints (`/.well-known/jmap`, `/jmap`, `/api/v1/mail/*` per REQ-SEND-01..05, `/api/v1/call/*`, `/chat/ws` per REQ-CHAT-40, `/proxy/image`, `/hooks/*` per REQ-HOOK-01) are served alongside, the SPA's client-side router handles client-side paths, and an unknown `/foo/bar` at the public listener returns the SPA's `index.html` (200, NOT 404) so the SPA's router decides. Server-side path matching uses longest-prefix.
- **REQ-DEPLOY-COLOC-03** Build coupling: a herold release bundles a pinned suite build, and operators who want a different version use the `asset_dir` override (REQ-DEPLOY-COLOC-01); updates to herold automatically pull in the matching suite revision. The release-build script reads a pinned suite revision from a manifest at `deploy/suite.version`, and mismatched manifests fail the release build.
- **REQ-DEPLOY-COLOC-04** Content-Security-Policy: the suite is served with a strict CSP -- `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' https:; connect-src 'self' wss://<public-host>;`. The image-proxy path (`/proxy/image`) is the explicit relaxation for inbound HTML rendering; CSP is hard-coded in the handler with no operator override in v1 (review when a real-world tenant complains).
- **REQ-DEPLOY-COLOC-05** Versioning + cache: SPA assets are served with content-addressed filenames (e.g. `app.<hash>.js`) plus `Cache-Control: public, max-age=31536000, immutable`; `index.html` itself is `Cache-Control: no-cache` so a refresh always pulls the latest entry point. The asset hash is computed at release-build time and the embedded FS is read-only at runtime.
