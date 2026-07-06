# 08 — Admin and management

How an operator creates accounts, inspects the queue, rotates keys, reads logs, etc. Three surfaces: REST API, CLI, Web UI. CLI and REST are v1; UI is phase 3.

## REST API

### Scope and shape

- **REQ-ADM-01** HTTP surface served from the same process, bound to a dedicated port (default 8080) *or* reusable on the JMAP vhost (path-prefixed). Default: dedicated port.
- **REQ-ADM-02** JSON in/out, `application/json`. No XML, no form encoding.
- **REQ-ADM-03** Authentication: bearer token (API key) or admin session cookie. API keys are scoped (`admin`, `readonly-admin`, per-domain).
- **REQ-ADM-04** Every mutating endpoint idempotent where possible (PUT over POST for set-state; POST for actions).
- **REQ-ADM-05** OpenAPI 3.1 spec published at `/api/openapi.json`. Spec is the source of truth, generated from code; no manual schemas.
- **REQ-ADM-06** Versioned: `/api/v1/…`. Backward-compatible changes within v1 allowed; breaking changes bump to v2.

### Minimum endpoints for v1

Grouped by resource. Every resource supports `GET list`, `GET /<id>`, `POST create`, `PUT /<id>`, `DELETE /<id>` unless noted.

- **REQ-ADM-10** `/api/v1/principals` — CRUD for principals (individuals, groups). Subresources: `/passwords`, `/app-passwords`, `/2fa`, `/aliases`, `/quota`.
- **REQ-ADM-11** `/api/v1/domains` — CRUD for hosted domains. Subresources: `/dkim`, `/mta-sts`, `/tls-rpt`, `/dmarc-records`.
- **REQ-ADM-12** `/api/v1/queue/messages` — queue inspection. Endpoints: list (with filters), get one, retry, hold, release, delete, bounce-now.
- **REQ-ADM-13** `/api/v1/mail/{principal}/messages/{id}` — inspect a specific mailbox message (admin read; rarely needed). Body not exposed by default.
- **REQ-ADM-14** `/api/v1/spam/train` — POST a message blob + label (ham/spam). `/api/v1/spam/rules` — read-only rule list with current weights.
- **REQ-ADM-15** No admin REST surface for Sieve scripts. Sieve is a per-user filter language; users edit their own scripts via ManageSieve (RFC 5804, port 4190) or the JMAP Sieve datatype (RFC 9007). Operators wanting site-wide policy use spam classification, the LLM categoriser, alias/transport rules, or DKIM/DMARC/Sieve-adjacent surfaces — not a global Sieve script. The originally-imagined `/api/v1/sieve/scripts` endpoint is intentionally not implemented.
- **REQ-ADM-16** `/api/v1/tls/certificates` — list, inspect, force-renew. `/api/v1/tls/acme/accounts`.
- **REQ-ADM-17** `/api/v1/reports/dmarc` — list received DMARC aggregate reports, per-domain + per-source.
- **REQ-ADM-18** `/api/v1/reports/tlsrpt` — TLS-RPT reports received.
- **REQ-ADM-19** `/api/v1/audit-log` — read audit log. Filters: since, actor, action, resource. Pagination by cursor.
- **REQ-ADM-20** `/api/v1/server/config` — effective config (redacted secrets). `/api/v1/server/reload` — POST triggers SIGHUP-equivalent reload.
- **REQ-ADM-21** `/api/v1/server/health` — liveness + readiness; unauthenticated.
- **REQ-ADM-22** `/api/v1/server/stats` — high-level stats. Prometheus metrics on separate `/metrics` endpoint.
- **REQ-ADM-22a** `/api/v1/import/jobs` — Gmail Takeout import jobs. `POST` creates a job (admin scope, can target any principal); `GET /{id}` returns state + counters; `GET /{id}/errors` paginates per-message errors; `POST /{id}/cancel` aborts. Full contract in `requirements/16-import.md` (REQ-IMPORT-60..65). The same endpoints are mirrored on the public listener under `user` scope, restricted to `principal == self`, for the Suite's self-service import flow (REQ-IMPORT-70..74).
- **REQ-ADM-23** Client-log surfaces (back the operator view of REQ-OPS-200..220):
  - **REQ-ADM-230** `GET /api/v1/admin/clientlog` — paginated read of the ring buffer. Filters: `slice` (`auth`|`public`, default `auth`), `app` (`suite`|`admin`), `kind`, `level`, `since`, `until`, `user`, `session_id`, `request_id`, `route`, `text` (substring match on `msg`/`stack`). Cursor pagination per REQ-ADM-40. Response carries the enriched record (client + server timestamps, computed `clock_skew_ms`, redacted fields visible as `***`).
  - **REQ-ADM-231** `GET /api/v1/admin/clientlog/timeline?request_id=<id>` — joined view of all server log records and client-log records carrying the same `X-Request-Id`, sorted by effective time. Implements the cross-source correlation surface (REQ-OPS-213).
  - **REQ-ADM-232** `POST /api/v1/admin/clientlog/livetail` — body `{session_id, duration?}`. Sets `clientlog.livetail_until` on the target session (REQ-OPS-211). `duration` defaults to and is clamped by `clientlog.livetail_default_duration` / `livetail_max_duration`. `DELETE /api/v1/admin/clientlog/livetail/{session_id}` cancels live-tail. Audit-logged (REQ-ADM-300).
  - **REQ-ADM-233** `GET /api/v1/admin/clientlog/stats` — high-level counters per endpoint and per app for the last hour / day, derived from the metrics in REQ-OPS-220. Used by the admin dashboard tile.
  - **REQ-ADM-234** The admin Client-logs view defaults to `kind=error` — the actionable crash-capture stream. `kind=log` (console output) and `kind=vital` (web-vitals performance telemetry: LCP, TTFB, FCP, CLS, INP) are reachable via the KIND filter (REQ-ADM-230) or a dedicated Performance sub-view, but do not populate the default incident-diagnosis view. Web vitals are trend telemetry, not per-incident signal.
  - All require admin scope (REQ-AUTH-SCOPE-*); none are exposed on the public listener.

### Errors

- **REQ-ADM-30** Errors return JSON with `{"error": "code", "message": "human readable", "details": {...}}`. Error codes stable.
- **REQ-ADM-31** HTTP status codes semantic: 400 (invalid input), 401 (auth), 403 (authz), 404 (missing), 409 (conflict), 422 (validation), 429 (rate-limited), 500 (bug), 503 (unavailable). No 200 wrappers around errors.

### Pagination

- **REQ-ADM-40** List endpoints paginate via cursor (`?cursor=…&limit=…`). Limit default 100, max 1000.
- **REQ-ADM-41** Cursors are opaque and stable across a paginated traversal (even under concurrent modification).

### Rate limiting

- **REQ-ADM-50** Admin API rate-limited per API key. Defaults generous (e.g. 100 req/s); configurable. Health endpoint exempt.

## CLI

The CLI is a thin wrapper around the REST API by default (via local UNIX socket when available, TCP + bearer token otherwise). Design goal: anything the UI can do, the CLI can do. Anything the CLI can do, one REST call can do.

### Invocation

- **REQ-ADM-100** Single binary `herold` with subcommands. (Or separate `heroldctl` — decide in tech stack doc.)
- **REQ-ADM-101** Subcommands grouped: `admin <noun> <verb>`, `queue <verb>`, `spam <verb>`, `cert <verb>`, `server <verb>`, `diag <verb>`.
- **REQ-ADM-102** Output: table by default, `--json` for scripting, `--raw` for pipeable.
- **REQ-ADM-103** Exit codes: 0 success, 1 usage, 2 not-found, 3 conflict, 4 auth, 5 network/server, 64-78 sysexits-style for system failures.

### Minimum commands for v1

- `herold admin bootstrap` — first-run initialization.
- `herold admin principal {create,delete,list,show,rename,quota,disable,enable,set-password,add-alias,remove-alias}`
- `herold admin domain {create,delete,list,show,dkim rotate,dkim show,mta-sts show,tls-rpt show}`
- `herold admin group {create,delete,member add/remove,list}`
- `herold queue {list,show,retry,hold,release,delete,bounce}`
- `herold spam {train,status,rules,score <file>}` (score = dry-run scoring)
- `herold cert {list,show,renew,add-manual}`
- `herold server {reload,status,config-check,version}`
- `herold mail {import,export,inspect <msgid>}`
- `herold import gmail --principal <email> [--archive <path>|--directory <path>] [--locale <tag>] [--dry-run] [--resume <job-id>] [--no-settings] [--no-mail]` — Google Takeout import. Full requirements in `requirements/16-import.md` (REQ-IMPORT-60..65). The Gmail-specific subcommand sits under a generic `herold import <vendor>` namespace so future importers (Outlook PST, Apple Mail mbox, generic Maildir) can extend the same surface without name churn.
- `herold diag {backup,restore,fsck,collect}` (collect = support bundle)

### Ergonomics

- **REQ-ADM-110** CLI commands with side effects take `--yes` or prompt. `--dry-run` wherever meaningful.
- **REQ-ADM-111** `herold diag collect` produces a redacted support bundle (config with secrets masked, last N log lines, metrics snapshot, version info, queue stats). One command, zip output.
- **REQ-ADM-112** Shell completions for bash, zsh, fish generated from the command tree.

## Web UI

Phase 3. Design placeholders here so later work doesn't demand redesign.

- **REQ-ADM-200** Web UI served from the same process (embedded static assets) at `/admin`. Auth-gated.
- **REQ-ADM-201** UI is a SPA that consumes the REST API. No additional backend logic in the UI layer.
- **REQ-ADM-202** Features covered:
  - Principal list / edit / quota.
  - Domain list / add / DKIM rotate / DNS record help (copyable TXT record bodies).
  - Queue inspector (list, filter, retry, hold).
  - DMARC report viewer (aggregates per source; trend graphs).
  - Certificate status.
  - Spam rule list, global Sieve edit, spam training corpus size.
  - Server config (read-only with "edit in file" hint and reload button).
  - Audit log viewer.
  - Stats dashboard (queued, accepted, rejected, rate of delivery).
  - Recent client errors / logs (ring-buffer view per REQ-ADM-230..233; per-request timeline; live-tail toggle).
- **REQ-ADM-203** Self-service panel for users (separate URL `/settings`): change password, set up 2FA, app passwords, forwarding, Sieve vacation, identity management.
- **REQ-ADM-204** UI framework: Svelte 5 + Vite + pnpm, sharing the design system imported from the former tabard repo (Bits UI + Carbon-inspired tokens + IBM Plex). See `docs/design/web/notes/adr-0001-merge-tabard-and-rewrite-admin-ui.md`. Built via the workspace under `web/`; embedded into the herold binary via `internal/webspa/` with a `-tags nofrontend` opt-out for backend-only builds.

## Audit log

The audit log is the security/compliance record of *actor-initiated* actions. It answers "who did what". Routine *system-initiated* mail-flow telemetry (SMTP acceptance, recipient resolution, inbound receipt, webhook dispatch outcomes) is not an audit concern and lives in the System events stream (REQ-ADM-304). The distinguishing axis is the nature of the event — a deliberate or security-relevant action by an identifiable actor — not the `ActorKind` alone.

- **REQ-ADM-300** Every admin action (auth + non-trivial write) MUST produce an audit record: `{timestamp, actor, actor_ip, action, resource, outcome, before, after}` for state changes; `{timestamp, actor, actor_ip, action, resource, outcome}` for reads. The audit stream carries actor-initiated actions — `ActorPrincipal` and `ActorAPIKey` writes and admin operations — plus a bounded allowlist of security-relevant `ActorSystem` events (configuration import, send-policy denial such as `mail.send.forbidden_from`, and recipient-leak detection such as `smtp.phase1_rcpt_leak`). Routine `ActorSystem` mail-flow events MUST NOT be written to the audit log; they belong to REQ-ADM-304.
- **REQ-ADM-301** Audit log is append-only in the metadata store, retention per REQ-STORE-82.
- **REQ-ADM-302** Audit log readable via REST/CLI; exportable to JSON lines for ingestion into external SIEM.
- **REQ-ADM-303** Failed auth attempts MUST be logged separately in an "auth events" stream (for SIEM/fail2ban integration). Auth events are security events: they belong with the audit/security trail, distinct from the operational System events stream (REQ-ADM-304).

## System events

- **REQ-ADM-304** System-initiated operational events — SMTP acceptance (`*.accept`), recipient resolution (`smtp.rcpt.resolve`), inbound receipt (`ses_inbound_received`), webhook dispatch outcomes (`hook.dispatch.*`), and IMAP-import worker lifecycle including the per-account debug stream (REQ-IMAP-IMP-65) — are recorded in a dedicated bounded ring-buffer table, separate from the audit log. Bounded by row-count and age, both configurable; oldest rows evicted as new ones arrive, mirroring the client-log ring (REQ-OPS-206). Read-only via REST/CLI. Excluded from the audit SIEM export (REQ-ADM-302) and from `herold diag backup` by default. This is operational telemetry for diagnosing mail flow, not a security trail.
- **REQ-ADM-305** The admin SPA presents each observability concern as one nav surface with one stated purpose: **Audit** (actor-initiated actions, REQ-ADM-300), **Events** (system-initiated operational telemetry, REQ-ADM-304), **Queue** (live delivery state with management actions — retry/hold/release/delete/flush), and **Message research** (retrospective per-message tracing, REQ-ADM-306 — the surface previously labelled "Research"/"Email research"). Live per-account state (e.g. IMAP-import worker status, REQ-IMAP-IMP-65) is presented as an account-scoped diagnostics panel, not a log surface.
- **REQ-ADM-306** Message research is the retrospective, operator-facing tool that answers "what happened to a given message" end-to-end, for non-technical mail operators. It offers flexible, combinable search filters — sender address, recipient address, date range, and Message-ID, with subject as an optional additional filter — across three sources and presents a per-message timeline joining them: (1) received mail and its disposition from the messages store — the delivering mailbox, Junk placement, and spam verdict (`SpamVerdict`); (2) the inbound accept / reject / defer trail from the System events stream (REQ-ADM-304), which is the only record of mail rejected or deferred at SMTP time that never became a stored message; (3) outbound send outcomes from queue history. This is distinct from **Queue**, which is the live operational management view of the outbound pipeline. Message research is admin-scoped and its access is audit-logged (REQ-ADM-300); the surface exposes envelope metadata and disposition only — never message bodies or attachment content. It is subject to the operator domain scope of REQ-ADM-307.
- **REQ-ADM-307** An operator sees only data for the domains they manage. Every observability surface that exposes per-domain mail-flow data — **System events** (REQ-ADM-304), **Queue**, **Message research** (REQ-ADM-306), and the per-domain slice of the **Audit log** (REQ-ADM-300) — filters its results to the operator's managed-domain set, enforced server-side. This introduces a delegated-operator authorization model: an operator principal is associated with a set of managed domains, and a full-access operator (all domains) is the special case of that set being unrestricted. The association model and its enforcement are a prerequisite for the domain-scoped surfaces; a surface MUST fail closed (show nothing) rather than leak cross-domain data if the managed-domain set cannot be resolved.

## Bootstrap and DNS assistance

Setting up a mail server correctly has many DNS touch-points (MX, SPF, DKIM TXT, DMARC TXT, MTA-STS record and HTTPS vhost, TLS-RPT, DANE TLSA). The admin tooling reduces the pain:

- **REQ-ADM-310** On domain creation, emit the *exact* DNS records the operator must publish. Copy-paste format for common providers.
- **REQ-ADM-311** `herold diag dns-check <domain>` verifies published DNS against expected values and reports mismatches.
- **REQ-ADM-312** `herold cert status` shows live cert + expiry + ACME account status per hostname.

## Configuration surface

See `09-operations.md` for config file structure; admin API exposes read + live reload but not arbitrary mutation of the config file. Operators edit the file, then trigger reload.

## Out of scope

- Multi-admin concurrency controls (optimistic concurrency via ETag on REST mutate is enough).
- Custom role definitions (the 3 roles from REQ-AUTH-60 are the fixed set).
- Web-based config file editor in v1.
- Delegated admin with scoped permissions (phase 3).
