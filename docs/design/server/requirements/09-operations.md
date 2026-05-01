# 09 — Operations

*(Revised 2026-04-24: config is now split into system config (file) and application config (DB-backed, runtime-mutable).)*

Configuration, TLS/ACME, observability, backup, upgrade. What day-2 operation looks like.

## Configuration model: two surfaces

We deliberately split configuration into two surfaces that an operator interacts with very differently.

| Surface | Location | Mutated by | Reloaded by | Change frequency |
|---|---|---|---|---|
| **System config** | `/etc/herold/system.toml` | Operator edits file | SIGHUP / `systemctl reload` | Once at install, rarely after |
| **Application config** | DB (SQLite or Postgres, depending on chosen backend) | Admin API / CLI / (Web UI phase 2) | Live (no SIGHUP needed) | Ongoing (add domains, users, change spam prompt, etc.) |

This mirrors a tension operators hit with Stalwart and similar projects: "production" edits (add a user, rotate DKIM, change Sieve) keep modifying the same config file that defines listeners and paths. That's wrong. Infra-level concerns and product-level concerns are different files edited by different tooling at different cadences.

### System config

- **REQ-OPS-01** System config is a single **TOML** file. YAML/JSON rejected.
- **REQ-OPS-02** Default location: `/etc/herold/system.toml`. Override via `--system-config <path>` or `HEROLD_SYSTEM_CONFIG`.
- **REQ-OPS-03** Contents: hostname, data_dir, listeners (bind addrs + protocol + TLS mode), admin-surface cert source (ACME account or file), run-as user/group, plugin declarations, log format + level, metrics bind, OTLP endpoint.
- **REQ-OPS-04** Secrets referenced via env var (`$VAR`), file (`file:/path`), or inline. Inline discouraged.
- **REQ-OPS-05** Strict parsing: unknown keys are errors.
- **REQ-OPS-06** `herold server config-check <path>` validates without starting.
- **REQ-OPS-07** SIGHUP or `herold server reload`: diff applied live where possible (bind changes, TLS source, plugin list, log level). Changes that require restart (data_dir move) are reported and rejected as reloads.
- **REQ-OPS-08** System config is **small** — target ≤ 100 lines for a typical single-domain deployment. If it grows beyond that, it's wrong: something belongs in application config.

### Application config (DB-backed)

- **REQ-OPS-20** Application state is stored in the main database, not in a file. Edits via admin API or CLI; persists across restarts.
- **REQ-OPS-21** Scope: hosted domains, principals, aliases, groups, per-user Sieve scripts, DKIM keys (per-domain, per-selector), spam policy (classifier plugin + prompt + thresholds), queue policy, retry schedule, ACME account binding per hostname, API keys, audit-log retention setting, attachment-extension blocklist.
- **REQ-OPS-22** No CLI / API operation on application config touches the system.toml file. No SIGHUP needed for adding a user.
- **REQ-OPS-23** Application config changes are audit-logged (REQ-ADM-300).
- **REQ-OPS-24** Application config supports **import/export** via CLI (`herold app-config dump > state.toml`, `herold app-config load state.toml`) for backup, GitOps-style management, and migration.
- **REQ-OPS-25** There is no "drift": the DB is authoritative; export is a view.

### Layout example (system.toml)

```toml
# /etc/herold/system.toml
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"
run_as_user = "herold"
run_as_group = "herold"

[server.admin_tls]
# The cert used for the admin HTTPS surface and JMAP.
# Mail-protocol certs (SMTP/IMAP per hostname) are managed per-domain in app config.
source = "acme"
acme_account = "default"

[[listener]]
name = "smtp-relay"
address = "0.0.0.0:25"
protocol = "smtp"
tls = "starttls"

[[listener]]
name = "smtp-submission"
address = "0.0.0.0:587"
protocol = "smtp-submission"
tls = "starttls"
auth_required = true

# ... imap, imaps, jmap, managesieve, admin ...

[acme]
email = "ops@example.com"
directory_url = "https://acme-v02.api.letsencrypt.org/directory"

[[plugin]]
name = "cloudflare"
path = "/var/lib/herold/plugins/herold-dns-cloudflare"
type = "dns"
lifecycle = "long-running"
options.api_token_env = "CF_API_TOKEN"

[[plugin]]
name = "spam-llm"
path = "/usr/lib/herold/plugins/herold-spam-llm"
type = "spam"
lifecycle = "long-running"
options.endpoint = "http://localhost:11434/v1"
options.model = "llama3.2:3b"

[directory.internal]
enabled = true
# No LDAP section — LDAP is out of scope.

# See "Logs" below for the full sink model. Minimal default:
[[log.sink]]
target = "stderr"
format = "auto"            # console on a tty, json otherwise
level  = "info"
activities = { deny = ["poll", "access"] }

[metrics]
bind = "127.0.0.1:9100"

[otlp]
enabled = false
# endpoint = "http://otelcol:4318"
```

### Reload

- **REQ-OPS-30** SIGHUP (or admin API `POST /server/reload`) reloads **system config only**. Listeners that changed bind addresses re-bind gracefully; protocol session settings apply to new connections only. Plugin list is reconciled (new started, removed stopped).
- **REQ-OPS-31** Application config changes never require SIGHUP; they're live.
- **REQ-OPS-32** Settings that require full restart: data_dir path, run_as user/group. `config-check` reports these.

## TLS and ACME

### Cert sources

- **REQ-OPS-40** Certs may be:
  1. **ACME-provisioned** (default for internet-facing deployment).
  2. **File-based** (`certificate_file`, `key_file` per hostname). For operators with existing PKI or using cert-manager.
  3. **Embedded self-signed** (dev mode only; explicit flag).
- **REQ-OPS-41** SNI-based cert selection per hostname across all listeners (SMTP 25/465/587, IMAP 143/993, JMAP 443, admin 8080, MTA-STS vhost).

### ACME behavior

- **REQ-OPS-50** Implement ACME RFC 8555 client. Challenge types: HTTP-01 (on 80/tcp), TLS-ALPN-01 (on 443), DNS-01 (via DNS provider plugin — REQ-PLUG).
- **REQ-OPS-51** DNS-01 provider support is **plugin-based** (REQ-PLUG-01). First-party plugins shipped: Cloudflare, Route53, Hetzner Cloud DNS, manual. Any others operator-installed.
- **REQ-OPS-52** ACME account key stored in `data_dir/acme/`, 0600.
- **REQ-OPS-53** Renewal: attempt at 1/3 remaining lifetime (for 90d certs: renew at ~60d old); retry with backoff on failure.
- **REQ-OPS-54** Provider choice: default Let's Encrypt production. Staging (`letsencrypt-staging`) supported for dev/test. ZeroSSL, Buypass, private ACME CAs via URL.
- **REQ-OPS-55** Rate-limit awareness: respect ACME directory limits; backoff on 429.

### Auto-DNS (first-class)

- **REQ-OPS-60** On `herold domain add <name> [--dns-plugin <name>]`: server generates DKIM keys, builds the set of records the domain needs (DKIM TXT, `_mta-sts` TXT, `_smtp._tls` TXT, `_dmarc` TXT, SPF TXT), and **publishes them via the associated DNS plugin**. No operator copy-paste.
- **REQ-OPS-61** On certificate issuance/renewal (and only if DANE is enabled for the domain): server updates the DANE TLSA record via the DNS plugin.
- **REQ-OPS-62** On DKIM key rotation: new selector TXT published, old selector kept during grace period, then removed.
- **REQ-OPS-63** If no DNS plugin is configured for a domain, the server falls back to the current "emit record text, operator publishes manually" mode. Documented.
- **REQ-OPS-64** DNS publication is idempotent (replace semantics per REQ-PLUG-30).
- **REQ-OPS-65** Periodic reconciliation: compare published records to expected, warn on drift. `herold diag dns-check <domain>` forces a reconciliation pass.

### Cert lifecycle visible to operator

- **REQ-OPS-70** `herold cert list` shows: hostname, issuer, NotBefore, NotAfter, SAN list, source (ACME/file/self-signed), last renewal attempt, next planned renewal.
- **REQ-OPS-71** Cert expiry warning metric + log event starting 14 days before expiry.
- **REQ-OPS-72** Certificates reloaded live on rotation — no connection draining required.

## Observability

Three pillars, one honest policy: no enterprise gates, no phone-home, no vendor lock.

### Logs

#### Sinks (multi-destination)

*(Revised 2026-04-29: a single log stream is not enough. Operators want JSON in a forensic file at `debug` and a calm human-readable view on the controlling terminal at `info`, with no choice between "useful for humans" and "useful for grep". The model below replaces the previous single-sink REQ-OPS-80/81. Per-module overrides (REQ-OPS-82) and field guarantees (REQ-OPS-83/84) are unchanged in spirit but now apply per sink.)*

- **REQ-OPS-80** Logging is **multi-sink**. The system config declares zero or more sinks under `[[log.sink]]`. Each sink has an independent target, format, level, per-module level overrides, and activity filter (REQ-OPS-86). A record is evaluated separately for each sink. Default configuration: a single stderr sink at level `info`, format `auto`. Field names are stable and documented and are identical across sinks.
- **REQ-OPS-81** Each sink's `target` is one of `stderr`, `stdout`, or an absolute filesystem path. File targets are append-only and rotated externally (logrotate, `journald` is reached via stderr under systemd). Syslog is not in v1.
- **REQ-OPS-81a** Each sink's `format` is one of:
  - `json` — `slog.JSONHandler`, one record per line (the canonical machine format).
  - `console` — human-readable: short timestamp, colorized level, message, then `key=value` attrs aligned for scanning. Colors are emitted only when the target is a TTY; redirected output is plain ASCII (STANDARDS.md §12 — no emojis). Multi-line attrs (stack traces) are indented under the parent line.
  - `auto` — `console` if and only if the target is a TTY at process start (or after SIGHUP re-detection), otherwise `json`. This is the default.
- **REQ-OPS-82** Log levels: `trace`, `debug`, `info`, `warn`, `error`. Each sink has its own `level`; default `info`. Per-module level overrides apply per sink (`[log.sink.modules]` table; keys match the `subsystem` or `module` slog attribute). The lowest level any sink wants determines the minimum severity the logger materialises; sinks whose level is higher silently drop the record.
- **REQ-OPS-83** Every log line includes: timestamp (RFC 3339 with timezone for `json`; short local time `HH:MM:SS.mmm` for `console`), level, module/subsystem, message, request/session correlation ID where applicable, and the activity tag from REQ-OPS-86. Field names are identical across formats; only rendering differs.
- **REQ-OPS-84** Sensitive values redacted at log time before any sink sees them: passwords, API keys, bearer tokens, session cookies, LLM spam prompt bodies at `info` level. DKIM private keys never logged. Redaction is a single handler in the chain — no sink can opt out.
- **REQ-OPS-85** SIGHUP reloads the sink list: sinks added, removed, or with changed parameters are reconciled without dropping records. File handles for unchanged file sinks are kept open across reload (so external `logrotate copytruncate` works without coordination).

#### Activity taxonomy (REQ-OPS-86) — closed enum, mandatory

*(Added 2026-04-29: severity alone cannot distinguish "user X moved 3 messages" from "POST /jmap 200" — both are routine operational signals. A small closed enum on every record lets operators filter noise from signal without losing forensic detail.)*

- **REQ-OPS-86** Every log record emitted from a wire-protocol layer (`protosmtp`, `protoimap`, `protojmap`, `protomanagesieve`, `protoadmin`, `protosend`, `protowebhook`), the queue/delivery path, the plugin supervisor, and the auth/directory layer MUST carry an `activity` attribute drawn from this closed enum:
  - `user` — caller-initiated state changes or information retrieval that an operator would want to see in a normal day's log: JMAP `*/set` calls, `*/query` and `*/get` at info, IMAP `APPEND` / `STORE` / `MOVE` / `EXPUNGE`, SMTP submission of a message, ManageSieve PUTSCRIPT, admin API mutations, login success.
  - `audit` — security-relevant events: login attempts (success and failure), permission denials, ACL decisions, scope-boundary rejections, plugin process kills, key rotations. Always retained even when other activities are filtered out.
  - `system` — server-initiated work the operator should see: outbound delivery attempts, queue retries, ACME issue/renew, DNS publication, schema migrations, plugin start/stop/restart.
  - `poll` — recurring no-op heartbeats: JMAP EventSource pings, IMAP `IDLE` keep-alives, push reconnects, periodic reconciliation reads. Almost always filtered out of console sinks.
  - `access` — per-request / per-command echo lines: HTTP request log, IMAP command trace, SMTP command echo. Forensic-only; emitted at `debug` by default.
  - `internal` — diagnostic, framework-level, or library events with no caller-facing semantics. Used sparingly for background goroutine state, lock contention warnings, etc.
- **REQ-OPS-86a** Records lacking an `activity` attribute on a layer that is required to tag fail CI: a lint check (`make lint-log-activity` / equivalent) inspects emitted records via the test harness and rejects any record from a covered package without an `activity` attribute. The test harness installs a debugging handler that records an attribute set on each record; tests that exercise wire layers assert tagging. Reviewer blocks merge on missing tags.
- **REQ-OPS-86b** Each sink has an optional `activities` filter: `allow = [...]` (only these activities pass), `deny = [...]` (these activities are dropped), or omitted (all pass). The default console sink ships with `deny = ["poll", "access"]`. The default file sink (when configured) ships with no filter. `activities.allow` and `activities.deny` are mutually exclusive in any one sink.
- **REQ-OPS-86c** A `--log-verbose` CLI flag and `HEROLD_LOG_VERBOSE=1` env var, when set, override every sink's `activities` filter to allow-all and lower every sink's `level` floor to `debug` for the lifetime of the process. This is the "give me everything for the next ten seconds" knob; operators reach for it during incident response without touching `system.toml`.
- **REQ-OPS-86d** The HTTP/JMAP per-request access log is `activity=access` and emitted at `debug`. The corresponding per-JMAP-method log line (`Email/set`, `Mailbox/query`, …) is `activity=user` (or `audit` for `Identity/*` and auth-adjacent calls) and emitted at `info`. The IMAP per-command trace is `activity=access` at `debug`; per-state-change events (`APPEND`, `STORE`, `MOVE`, `EXPUNGE`) are `activity=user` at `info`. Equivalent splits apply to SMTP, ManageSieve, and the admin API.

### Layout example (system.toml)

```toml
# /etc/herold/system.toml — observability section

# Console sink: human-friendly, default-quiet, suppresses polling and access noise.
[[log.sink]]
target  = "stderr"
format  = "auto"           # console on a tty, json otherwise
level   = "info"
modules = { protojmap = "info", queue = "info" }
activities = { deny = ["poll", "access"] }

# Forensic JSON sink: everything, including poll/access, for grep / shipping.
[[log.sink]]
target = "/var/log/herold/herold.jsonl"
format = "json"
level  = "debug"
# no `activities` filter -> all activities are kept
```

The legacy single-sink form (`[logging] format=... level=... destination=...` from earlier revisions of this document) is accepted by the parser as a one-shot translation into a single `[[log.sink]]` entry, with a deprecation warning at startup. New deployments use the explicit list.

### Metrics

- **REQ-OPS-90** **Prometheus-format metrics on `/metrics`** (unauthenticated by default on a separate bind address). No license gate.
- **REQ-OPS-91** Metric families at minimum:
  - `herold_smtp_connections_total{listener, status}`
  - `herold_smtp_messages_total{direction, status}`
  - `herold_imap_sessions_active`
  - `herold_jmap_requests_total{method, status}`
  - `herold_queue_size{stage}`, `herold_queue_oldest_seconds`
  - `herold_delivery_attempts_total{status}`, `herold_delivery_duration_seconds` histogram
  - `herold_spam_verdict_total{verdict}`, `herold_spam_confidence` histogram, `herold_spam_classifier_latency_seconds`, `herold_spam_classifier_failures_total`
  - `herold_plugin_invocations_total{plugin,method,status}`, `herold_plugin_latency_seconds{plugin,method}`, `herold_plugin_state{plugin}`, `herold_plugin_restarts_total{plugin}`
  - `herold_storage_bytes{type}`, `herold_storage_messages_total{type}`
  - `herold_tls_cert_expiry_seconds{hostname}`
  - `herold_auth_attempts_total{protocol, result}`
  - Go runtime metrics (`go_goroutines`, `go_memstats_*`, `go_gc_*` via the prometheus client's default collector).
- **REQ-OPS-92** OpenMetrics (text exposition) format. No pushgateway integration required.

### Traces

- **REQ-OPS-100** OpenTelemetry **OTLP/HTTP** export, optional (off by default). When enabled, sample rate configurable.
- **REQ-OPS-101** Trace spans cover: full SMTP session, IMAP command, JMAP request, mail delivery attempt, spam classification, Sieve execution, ACME renewal, plugin calls.
- **REQ-OPS-102** Trace context propagated across internal async boundaries (queue enqueue/dequeue, worker handoff, plugin JSON-RPC).
- **REQ-OPS-103** No built-in trace storage. Operators ship to Jaeger/Tempo/Datadog/etc.

### Client log ingest (UI errors, logs, telemetry)

*(Added 2026-05-01: the herold-served SPAs (Suite on the public listener, Admin on the admin listener) need to forward runtime errors, console output, and Web Vitals back to the server. The server is the sole upstream — no direct browser-to-third-party telemetry. Captured events are re-emitted into the existing `internal/observe` pipeline (slog + OTLP) so they fan out to whatever observability backend the operator already runs, plus a bounded ring buffer in the metadata DB so a small operator with no collector still has the last N hours visible from `/admin`.)*

#### Endpoints and auth boundaries

- **REQ-OPS-200** Two HTTP ingest paths exist, with different auth and different limits:
  - **Authenticated**: `POST /api/v1/clientlog` on whichever listener serves the SPA emitting the event (public listener for Suite, admin listener for Admin UI). Requires a valid session cookie. Carries the full event schema.
  - **Anonymous**: `POST /api/v1/clientlog/public` on the same listeners. No auth. Carries the *narrow* schema (REQ-OPS-207). Used for login-page crashes and any other pre-auth failure.
  Each listener serves its own pair; the cookie-scope split (REQ-OPS-ADMIN-LISTENER-03) prevents cross-surface auth confusion.
- **REQ-OPS-201** Both endpoints accept a JSON body of shape `{events: [Event, ...]}`. The `Content-Type` is `application/json` for `fetch` and `Blob`-wrapped `application/json` for `navigator.sendBeacon`; servers MUST accept both. Bodies over the per-endpoint cap (REQ-OPS-216) are rejected with 413; rate-limited requests on the anonymous endpoint are dropped silently (200 with empty body) to avoid signalling abuse.

#### Event schema

- **REQ-OPS-202** The full event schema (authenticated endpoint) is:
  ```
  {
    "v":          1,                                    // schema version, integer
    "kind":       "error" | "log" | "vital",
    "level":      "trace"|"debug"|"info"|"warn"|"error",// "error" implied for kind=error
    "msg":        "string",                             // human-readable summary, capped 4 KiB
    "stack":      "string",                             // raw, unsymbolicated; capped 16 KiB; kind=error only
    "client_ts":  "RFC3339 with ms",                    // browser wall clock at capture
    "seq":        12345,                                // monotonic per page load
    "page_id":    "uuid",                               // per page load, opaque
    "session_id": "uuid",                               // per browser session (storage), opaque
    "app":        "suite" | "admin",
    "build_sha":  "string",                             // SPA build identifier
    "route":      "string",                             // current route, e.g. "/mail/inbox"
    "request_id": "string",                             // correlated server X-Request-Id when known; optional
    "ua":         "string",                             // User-Agent, capped 256 chars
    "breadcrumbs": [Breadcrumb, ...],                   // last N (≤32) navigation/network/log events; allow-listed fields only
    "vital":      { "name": "LCP"|"INP"|"CLS"|..., "value": number, "id": "string" }, // kind=vital only
    "synchronous": false                                // optional; see REQ-OPS-211
  }
  ```
  A `Breadcrumb` carries `{ts, kind, route?, status?, method?, url_path?, msg?}` only. URL query strings, request bodies, response bodies, header values, DOM snapshots, and input field values MUST NOT appear in breadcrumbs.
- **REQ-OPS-207** The narrow schema (anonymous endpoint) is the strict subset `{v, kind, level, msg, stack, client_ts, seq, page_id, app, build_sha, route, ua}`. No breadcrumbs, no `request_id`, no `session_id`, no `vital` payload. Server rejects (400) any unknown field on this endpoint — strict parsing only.

#### Sanitisation, enrichment, fan-out

- **REQ-OPS-203** On receive, the server:
  1. Validates schema strictly (unknown fields fail; oversize fields truncate with a `_truncated` marker).
  2. Strips/redacts the redaction set from REQ-OPS-84 from `msg`, `stack`, and breadcrumb `msg` (passwords, bearer tokens, cookies, API keys). The same redaction handler that protects server logs also runs on client payloads — no separate code path.
  3. Enriches: stamps `server_recv_ts` (server wall clock), computes `clock_skew_ms = server_recv_ts - max(events[].client_ts)` for the batch, attaches `user_id` (auth'd endpoint only), `client_ip` (truncated per privacy policy), `listener` (`public`|`admin`), and `endpoint` (`auth`|`public`).
  4. Fans out: writes one slog record per event (REQ-OPS-204), one OTLP log record per event when egress is enabled (REQ-OPS-205), and one row in the ring buffer (REQ-OPS-206).
- **REQ-OPS-204** The slog emission carries the standard log fields plus `source=client`, `app`, `kind`, `route`, `build`, `client_ts`, `client_session=session_id`, `request_id` (when present), and the activity tag from REQ-OPS-86: `audit` for `kind=error` (security-relevant view: errors are seen by operators), `user` for `kind=log` at level `info` and below from authenticated sessions, `internal` for `kind=vital`, `access` for breadcrumb echo lines. The console formatter SHOULD render `source=client` records visually distinct from server records (e.g. a leading marker or distinct colour) on TTY targets.
- **REQ-OPS-205** When OTLP export is enabled (REQ-OPS-100), client events are emitted as OTLP log records with resource attributes `service.name=herold-suite` or `herold-admin`, `service.version=<build_sha>`, `deployment.environment=<operator-config>`. Each record carries `attributes`: `client.session_id`, `client.page_id`, `client.route`, `client.ua`, `user.id` (auth'd only), `request_id` (when present). Anonymous events are NOT sent to OTLP unless the operator sets `[clientlog.public].otlp_egress = true` (default `false`); this prevents random internet abuse from inflating the operator's observability bill.
- **REQ-OPS-213** Cross-source ordering between client and server events is via `request_id` correlation, not clock alignment: every JMAP/admin request response carries `X-Request-Id` (existing), the SPA captures it on each fetch and attaches it to every event emitted while that request is in flight. The admin viewer (REQ-ADM-230) joins client and server records by `request_id` when displaying a request timeline.

#### Ring buffer (in-DB recent storage)

- **REQ-OPS-206** The server stores recent client events in a bounded ring-buffer table in the metadata DB. Two separate slices: one for `endpoint=auth` events, one for `endpoint=public` events. Each slice is bounded by row count and age, both configurable; oldest rows are evicted as new ones arrive. Defaults: auth slice 100 000 rows / 7 days, public slice 10 000 rows / 24 h. Ring-buffer storage is independent of OTLP egress: operators with no collector still get a recent view.
- **REQ-OPS-206a** Ring-buffer rows are read-only via the admin REST API (REQ-ADM-230..232). They are excluded from `herold diag backup` by default; `--include-clientlog` opts in.

#### PII, opt-out, and field allowlist

- **REQ-OPS-208** Per-user opt-out: each principal has a boolean `clientlog_telemetry_enabled`. When `false`, the SPA emits only `kind=error` events from that user's authenticated session; `kind=log` and `kind=vital` are dropped client-side. The default is taken from the system config (`[clientlog.defaults].telemetry_enabled`); the quickstart example ships this `true`. Errors are always sent regardless of the opt-out: a user cannot opt out of having their crashes reported, only out of behavioural telemetry.
- **REQ-OPS-215** Server-side field allowlist is enforced on both endpoints: any `events[].breadcrumbs[]` entry MUST conform to the allow-listed shape (REQ-OPS-202); unknown fields are dropped silently with a warning metric (`herold_clientlog_dropped_fields_total`). Message bodies, attachment names, contact data, and chat content MUST NOT appear in any client-log payload — the SPA enforces this in the wrapper (REQ-CLOG-10) and the server enforces it again as defence-in-depth via the field allowlist.

#### Console rendering and temporal ordering

- **REQ-OPS-210** Client events emitted to console sinks pass through a per-session reorder buffer keyed by `client_ts + clock_skew_ms`. The buffer holds events for `reorder_window_ms` (default 1000 ms) before flushing in order. Events arriving after the window close for their session are emitted immediately with an attribute `late=true` and the original `client_ts`, so an operator can see they belong earlier. The reorder buffer applies only to console sinks; JSON file sinks and OTLP export receive events in arrival order with all timestamps preserved (downstream tools order by `client_ts`).
- **REQ-OPS-211** Synchronous emission is supported for two cases:
  1. **Per-event**: an event with `synchronous: true` is sent by the SPA as a non-batched `fetch` with `keepalive: true`. The SPA's caller awaits the request. Used for fatal-during-boot errors that must not be lost to a later batch flush. The server's handling is otherwise identical.
  2. **Per-session live-tail**: an operator with admin scope can flip a session into "live tail" mode via `POST /api/v1/admin/clientlog/livetail` (REQ-ADM-232). The SPA polls the session's `clientlog.livetail_until` field on every JMAP response; while non-zero and in the future, the SPA flushes its queue every 100 ms and emits each event synchronously. Live-tail auto-expires (default 15 min, max 60 min); the server clamps the requested duration to the configured maximum.
  Synchronous mode is never the default. The SPA wrapper documents the cost (one HTTP RTT per event) so contributors do not enable it casually.

#### Source maps

- **REQ-OPS-212** The server stores raw, unsymbolicated stacks. It does NOT perform server-side source-map resolution. Source maps are published as part of the SPA build artefacts and are publicly fetchable from the SPA origin (`/assets/*.map`), since herold is open-source and the maps reveal nothing not already in the source tree. The admin viewer (REQ-ADM-230) renders the raw stack and offers a "Symbolicate" button that fetches the relevant `.map` lazily client-side and rewrites the trace in place; symbolication never happens server-side.

#### Quotas and limits

- **REQ-OPS-216** Per-endpoint limits, configurable, with the following defaults:
  | Limit | Auth endpoint | Public endpoint |
  |---|---|---|
  | Max body bytes per request | 256 KiB | 8 KiB |
  | Max events per batch | 100 | 5 |
  | Per-session token bucket | 1000 events / 5 min | n/a |
  | Per-IP token bucket | n/a | 10 events / min, burst 20 |
  | Max `stack` length | 16 KiB | 4 KiB |
  | Max `msg` length | 4 KiB | 1 KiB |
  | Breadcrumb count | 32 | 0 (none allowed) |
  Over-quota events are dropped, counted in `herold_clientlog_dropped_total{endpoint, reason}`, and the SPA may include a synthetic `kind=log level=warn msg="N events dropped"` event in the next batch so the gap is visible.

#### Anonymous-endpoint security

- **REQ-OPS-217** The anonymous endpoint:
  1. Enforces strict schema (REQ-OPS-207) — extra fields fail-closed.
  2. Rate-limits per remote IP (REQ-OPS-216) with a separate bucket pool from the authenticated endpoint.
  3. Checks `Origin` when present: requests from foreign origins are dropped silently. Absent `Origin` is allowed (CLI tools, sendBeacon may omit).
  4. CORS preflight (`OPTIONS`) responds only with `Access-Control-Allow-Origin: <own origin>`; foreign origins receive 204 with no allow-headers, blocking browser-side cross-origin abuse.
  5. Uses a separate ring-buffer slice (REQ-OPS-206) so anonymous traffic cannot evict authenticated records.
  6. Defaults OTLP egress off (REQ-OPS-205).
  7. Strips no source-map metadata (none stored), and admin-viewer rendering of public-slice rows is HTML-encoded with no clickable links — log-injection / stored-XSS defence (REQ-OPS-218).
- **REQ-OPS-218** The admin viewer renders all client-log fields as text, never as HTML. URLs in `breadcrumbs[].url_path` and `route` are displayed as monospace text with no `href`. This applies to both endpoint slices but is mandatory for the public slice because content is attacker-controlled.

#### Configuration

- **REQ-OPS-219** System-config layout (extends `system.toml`):
  ```toml
  [clientlog]
  enabled = true                        # master switch; default true
  reorder_window_ms = 1000              # console reorder buffer (REQ-OPS-210)
  livetail_default_duration = "15m"
  livetail_max_duration    = "60m"

  [clientlog.defaults]
  telemetry_enabled = true              # default value of per-user opt-in (REQ-OPS-208)

  [clientlog.auth]
  ring_buffer_rows = 100000
  ring_buffer_age  = "168h"              # 7 days
  rate_per_session = "1000/5m"
  body_max_bytes   = 262144

  [clientlog.public]
  enabled          = true                # the anonymous endpoint
  otlp_egress      = false               # REQ-OPS-205 default
  ring_buffer_rows = 10000
  ring_buffer_age  = "24h"
  rate_per_ip      = "10/m"
  body_max_bytes   = 8192
  ```
  All values reloadable via SIGHUP (REQ-OPS-30). Setting `clientlog.enabled = false` disables both endpoints (404 to clients) and stops SPA emission via the bootstrap descriptor (REQ-CLOG-12).
- **REQ-OPS-220** Metrics (extend REQ-OPS-91):
  - `herold_clientlog_received_total{endpoint, app, kind}`
  - `herold_clientlog_dropped_total{endpoint, reason}` (reason: `rate_limit`, `body_too_large`, `schema`, `field_allowlist`, `disabled`)
  - `herold_clientlog_ring_buffer_rows{slice}`
  - `herold_clientlog_livetail_active_sessions`

### What we explicitly do NOT ship

- SNMP.
- Webhooks.
- Email alerts for metrics (use Alertmanager with Prometheus).
- Built-in metric storage beyond short-term in-process for `/admin/stats`.
- Custom "events" streams separate from logs.

## Health endpoints

- **REQ-OPS-110** `/healthz/live` — liveness. HTTP 200 if the process is running.
- **REQ-OPS-111** `/healthz/ready` — readiness. HTTP 200 only if: store open, listeners bound, ACME account loaded, all critical plugins up, no critical errors in last N seconds. 503 otherwise.
- **REQ-OPS-112** Health endpoints don't require auth. Exposed on the admin listener.

## Backup and restore

See REQ-STORE-60..63 for data model. Operationally:

- **REQ-OPS-120** `herold diag backup <path>` produces a consistent backup file (tar.zst). Concurrent writes allowed; snapshot isolation via store.
- **REQ-OPS-121** Backup contents: application DB snapshot (contains all application config — domains, principals, aliases, Sieve, spam policy, DKIM keys), blob directory, queue state, ACME account state, audit log. System config referenced by path, not copied (avoids leaking secrets). `--include-system-config` override available.
- **REQ-OPS-122** Restore is offline: server stopped, `herold diag restore <path>`, server started. System config on the target host must be compatible (same listeners / paths) or explicitly merged.
- **REQ-OPS-123** Remote backup destination (operator-configured): out of v1 scope (single-node with local backups + external snapshots is enough).

## Upgrade and migration

- **REQ-OPS-130** Store has a version number; on startup, server checks and runs incremental migrations if needed. Migrations MUST be forward-only (no downgrade path).
- **REQ-OPS-131** Major version upgrades: document data layout changes; encourage backup-before-upgrade.
- **REQ-OPS-132** Restart of a single-node deployment: planned brief unavailability is acceptable. Long-running connections (IMAP IDLE) dropped cleanly with `BYE`.

## Process supervision

- **REQ-OPS-140** Server MUST run cleanly under systemd (Type=notify for readiness). `sd_notify` integration to signal startup complete.
- **REQ-OPS-141** MUST handle SIGTERM with a graceful shutdown: stop accepting new connections, drain in-flight requests up to configurable deadline (default 30s), stop plugins, then force close.
- **REQ-OPS-142** SIGHUP → system-config reload (REQ-OPS-30).
- **REQ-OPS-143** No daemonization in-process. If operator wants background, use `systemd` or the supervisor of their choice.

## Packaging

- **REQ-OPS-150** Official Linux packages: Debian `.deb`, Red Hat `.rpm`, a single Docker image (Debian-based), a static musl binary tarball. First-party plugins bundled in the packages.
- **REQ-OPS-151** Docker image: non-root user, read-only root FS supported, data mounted at `/var/lib/herold`, system config mounted at `/etc/herold`, plugins bundled at `/usr/lib/herold/plugins`. No embedded secrets.
- **REQ-OPS-152** Kubernetes manifests (StatefulSet + ConfigMap/Secret) in `deploy/k8s/`. Not a Helm chart in v1; document with plain manifests.
- **REQ-OPS-153** macOS and Windows binaries provided for development, not as supported production targets.

## Secrets handling

- **REQ-OPS-160** No secrets in logs (REQ-OPS-84).
- **REQ-OPS-161** Secrets in config: prefer `file:/path/to/secret` references over inline. Admin CLI never prints decrypted secret values to stdout.
- **REQ-OPS-162** systemd `LoadCredential=` and Docker/K8s secret files supported by `file:` references.
- **REQ-OPS-163** Plugin secrets delivered via env var, stdin at configure, or FIFO (REQ-PLUG-22). Never via command-line arguments.

## VAPID keys (Web Push)

Per `requirements/01-protocols.md` REQ-PROTO-122. Phase 1.

- **REQ-OPS-180** Herold maintains a single deployment-level VAPID key pair (P-256 ECDSA per RFC 8292). Generated at first start (or via `herold push generate-vapid-keys`) and persisted to the data dir under `secrets/vapid/`.
- **REQ-OPS-181** Configuration:
  ```
  # /etc/herold/system.toml
  [push.vapid]
  public_key  = "file:/var/lib/herold/secrets/vapid/public.key"
  private_key = "file:/var/lib/herold/secrets/vapid/private.key"
  contact     = "mailto:operator@example.com"   # used as the VAPID JWT 'sub' claim
  ```
- **REQ-OPS-182** Public key surfaced in the JMAP session descriptor for clients to pass to `pushManager.subscribe`. Private key never leaves the herold process.
- **REQ-OPS-183** Rotation: manual operator process. New keys generated; subscriptions registered against the old key fail on next push attempt with 410-equivalent (the subscription's `vapidKeyAtRegistration` doesn't match); herold destroys those subscriptions and clients re-subscribe on next launch. Rotation cadence is operator policy; not automated in v1.
- **REQ-OPS-184** Without VAPID configured, herold does NOT advertise the `https://netzhansa.com/jmap/push` capability and the suite's push features degrade per `docs/design/web/requirements/25-push-notifications.md` (no push delivery; in-app indicators only).

## coturn (TURN relay for chat video calls)

Phase 2 — see `requirements/15-video-calls.md` § Operations.

For chat's 1:1 video calls (REQ-CALL-*), herold mints short-lived TURN credentials against a coturn deployment configured by the operator. coturn is NOT bundled with herold; it's a separate process the operator runs alongside (typical pattern for self-hosted WebRTC deployments).

### Deployment shape

- **REQ-OPS-170** Operator deploys coturn (or equivalent — Pion TURN, eturnal) at the same origin or a closely-coordinated origin (e.g. `turn.example.com` if `mail.example.com` hosts herold).
- **REQ-OPS-171** Default ports: 3478/UDP and 5349/TCP (TLS). IPv4 and IPv6 both reachable.
- **REQ-OPS-172** TLS certificate: same ACME flow as herold's other listeners (REQ-OPS-50..55) or operator-supplied. The cert covers the TURN host's CN.

### Configuration

coturn is configured with the long-term-credential mechanism:

```
# /etc/coturn/turnserver.conf (operator-side, illustrative)
listening-port=3478
tls-listening-port=5349
fingerprint
use-auth-secret
static-auth-secret=<shared-secret>
realm=mail.example.com
total-quota=1000
stale-nonce=600
no-loopback-peers
no-multicast-peers
cert=/etc/letsencrypt/live/turn.example.com/fullchain.pem
pkey=/etc/letsencrypt/live/turn.example.com/privkey.pem
```

The shared secret is also configured in herold:

```
# /etc/herold/system.toml
[chat.turn]
host = "turn.example.com"
port = 3478
tls_port = 5349
shared_secret = "file:/etc/herold/secrets/coturn"
```

- **REQ-OPS-173** Shared secret is rotated by the operator: update both `/etc/coturn/turnserver.conf` and the herold `chat.turn.shared_secret` reference, SIGHUP both processes (or reload coturn per its own conventions). Rotation is rare; a credentialised call survives the rotation if the credential was minted before the change (the credential is HMAC of the username and the at-mint-time secret, validated by coturn against the at-validate-time secret — operators avoid mid-call rotation).
- **REQ-OPS-174** `chat.turn.realm` defaults to the herold listener's primary hostname; operator can override.

### Operating posture

- coturn is the operator's responsibility to monitor and update. herold does not bundle coturn binaries, configurations, or systemd units in v1; the deploy/ docs include reference configurations.
- Without coturn, video calls still work for ~85–90% of network configurations (STUN-only). The remaining ~10–15% (strict NAT, symmetric NAT, restrictive firewalls) require relay; absent TURN, those calls fail at ICE establishment.

## Admin listener (operator-config)

*(Added 2026-04-26 rev 9: distinct admin listener separates internet-facing end-user surfaces from operator-only admin surfaces; pairs with the auth-scope boundary in REQ-AUTH-SCOPE-01..04.)*

- **REQ-OPS-ADMIN-LISTENER-01** The HTTP admin surface (admin REST at `/api/v1/admin/*` per REQ-ADM-01..06, admin UI under `/admin/*`, Prometheus `/metrics` per REQ-OPS-90, and all of protoadmin) MUST be served from a distinct listener configured via `[server.admin_listener]` in system.toml: `bind` (default `127.0.0.1:9443`), `tls` (cert/key file refs; ACME also acceptable per REQ-OPS-40). The public listener (`[server.public_listener]`, default `0.0.0.0:443`) MUST NOT serve any admin path; an admin path arriving at the public listener returns 404 (NOT 403 -- the path doesn't exist on this origin).
- **REQ-OPS-ADMIN-LISTENER-02** The default `127.0.0.1:9443` bind makes the admin surface invisible to internet scanners and unreachable except via local-machine access or operator-tunnelled (`ssh -L 9443:127.0.0.1:9443 admin@host`). Operators with a VPN / wireguard / corporate intranet flip the bind to `0.0.0.0:9443` (or whatever interface); operators without any of those tunnel via SSH, documented in `docs/user/operate.md`.
- **REQ-OPS-ADMIN-LISTENER-03** Cross-listener cookie scope: a cookie issued by the public listener has `Domain=` set to the public origin (e.g. `mail.example.com`) and `SameSite=Lax`, so presenting it on the admin listener (different host or different port) is a no-op because Domain doesn't match. A cookie issued by the admin listener has `Domain=` set to the admin origin (e.g. the loopback or a separate `admin.mail.example.com`); presenting it on the public listener is similarly a no-op, and the listener boundary therefore mechanically enforces the auth-scope boundary while the handler-side scope check (REQ-AUTH-SCOPE-02) is defence-in-depth.

## What we don't build

- SNMP trap receiver or MIB.
- Alerting engine (delegate to Prometheus/Alertmanager).
- Webhook-as-events streams (alerting via metrics is cleaner).
- Custom bundled Grafana dashboards (provide examples in docs).
