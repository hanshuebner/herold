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
- **REQ-ADM-15** `/api/v1/sieve/scripts` — read/write global script. Per-user scripts only via ManageSieve for the user.
- **REQ-ADM-16** `/api/v1/tls/certificates` — list, inspect, force-renew. `/api/v1/tls/acme/accounts`.
- **REQ-ADM-17** `/api/v1/reports/dmarc` — list received DMARC aggregate reports, per-domain + per-source.
- **REQ-ADM-18** `/api/v1/reports/tlsrpt` — TLS-RPT reports received.
- **REQ-ADM-19** `/api/v1/audit-log` — read audit log. Filters: since, actor, action, resource. Pagination by cursor.
- **REQ-ADM-20** `/api/v1/server/config` — effective config (redacted secrets). `/api/v1/server/reload` — POST triggers SIGHUP-equivalent reload.
- **REQ-ADM-21** `/api/v1/server/health` — liveness + readiness; unauthenticated.
- **REQ-ADM-22** `/api/v1/server/stats` — high-level stats. Prometheus metrics on separate `/metrics` endpoint.

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
- **REQ-ADM-203** Self-service panel for users (separate URL `/settings`): change password, set up 2FA, app passwords, forwarding, Sieve vacation, identity management.
- **REQ-ADM-204** UI framework TBD; prefer something small and long-lived (Svelte, SolidJS, or plain JS) over React-SPA default.

## Audit log

- **REQ-ADM-300** Every admin action (auth + non-trivial write) MUST produce an audit record: `{timestamp, actor, actor_ip, action, resource, outcome, before, after}` for state changes; `{timestamp, actor, actor_ip, action, resource, outcome}` for reads.
- **REQ-ADM-301** Audit log is append-only in the metadata store, retention per REQ-STORE-82.
- **REQ-ADM-302** Audit log readable via REST/CLI; exportable to JSON lines for ingestion into external SIEM.
- **REQ-ADM-303** Failed auth attempts MUST be logged separately in an "auth events" stream (for SIEM/fail2ban integration).

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
