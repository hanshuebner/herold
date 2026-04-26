# Operating Herold

Day-2 runbook for the operator who already has herold installed and
running. For first-run install paths see [./install.md](./install.md);
for application administration (domains, principals, mailboxes,
aliases) see [./administer.md](./administer.md); for the real-domain
walkthrough see [./quickstart-extended.md](./quickstart-extended.md).

This document is the operator's reference. Every config knob, every
operational lever, every common failure mode the operator hits in
production should be documented or linked from here.

## The two configuration surfaces

Herold splits configuration into two surfaces:

| Surface          | Location                  | Mutated by              | Reload              |
|------------------|---------------------------|-------------------------|---------------------|
| **System config** | `/etc/herold/system.toml` | Operator edits the file | SIGHUP / `herold server reload` |
| **Application config** | The herold DB        | Admin REST / CLI / UI   | Live (no SIGHUP)    |

System config is small (target <= 100 lines for a typical deployment,
REQ-OPS-08) and infra-owned: hostnames, listener bind addresses, TLS
sources, plugin declarations, log format. Application config is
everything operators tune day-to-day: domains, principals, aliases,
DKIM keys, Sieve scripts, spam policy, webhooks, API keys. The DB is
authoritative; there is no drift between file and DB because the file
holds none of it.

This document covers the operator's side: system.toml plus the
operational levers (TLS, DNS, observability, queue, plugins, OIDC
federation). Day-to-day application administration is covered in
[./administer.md](./administer.md).

## system.toml reference

The complete TOML schema is defined in
`internal/sysconfig/sysconfig.go`. This section is the operator's
high-level guide. Strict parsing applies: unknown keys are an error
(REQ-OPS-05). Validate without starting:

```bash
herold server config-check /etc/herold/system.toml
```

### `[server]` - process-wide settings

```toml
[server]
hostname = "mail.example.com"      # primary FQDN; required.
data_dir = "/var/lib/herold"       # required. Stores DB, blobs, FTS index, queue, ACME state.
run_as_user = "herold"             # drop privs to this user after binding listeners.
run_as_group = "herold"
shutdown_grace = "30s"             # graceful-shutdown deadline (REQ-OPS-141). Default 30s.
```

`hostname` is the primary FQDN herold identifies as on outbound SMTP
HELO and serves on the admin / JMAP virtual host. `data_dir` is the
single directory where the SQLite DB, blob tree, FTS index, queue
state, ACME account material, and (by default) the SQLite database
file live.

### `[server.admin_tls]` - TLS for the admin / JMAP / UI vhost

```toml
[server.admin_tls]
source = "file"                                  # phase 1: only "file" is accepted.
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"
```

Phase 1 supports only `source = "file"`. The `acme` source is parsed
(so configs with future ACME blocks do not hit unknown-key errors)
but rejected at validate with a clear message - ACME lands in
queue-delivery-implementor's surface in Wave 3.1+.

For the development / loopback quickstart, the admin listener can be
declared with `tls = "none"` and this block can point at any pair of
files; the listener does not consult them when TLS is off.

### `[server.storage]` - metadata-store backend

```toml
[server.storage]
backend = "sqlite"                 # "sqlite" (default) or "postgres".

[server.storage.sqlite]
path = "/var/lib/herold/herold.sqlite"   # defaults to <data_dir>/herold.sqlite when empty.

[server.storage.postgres]
dsn = "postgres://herold:secret@localhost:5432/herold?sslmode=verify-full"
blob_dir = "/var/lib/herold/blobs"       # required for postgres backend.
```

Both blocks are parsed unconditionally; only the block matching
`backend` is consulted. SQLite is the default and the right choice
for <= 200 mailboxes; Postgres for heavier deployments. To switch,
`herold diag migrate` (see "Upgrades and migration" below).

### `[[listener]]` - bound network endpoints

Every listener is a separate `[[listener]]` table. Required fields:
`name` (unique), `address` (`host:port`), `protocol` (one of `smtp`,
`smtp-submission`, `imap`, `imaps`, `admin`), `tls` (one of `none`,
`starttls`, `implicit`).

Optional fields: `auth_required` (forces SASL on submission listeners),
`proxy_protocol` (accepts the HAProxy PROXY protocol header),
`cert_file` and `key_file` (per-listener cert override; both or
neither), `kind` (`public` or `admin` for HTTP listeners; required in
production -- see below).

#### Listener kinds: public vs admin (REQ-OPS-ADMIN-LISTENER-01..03)

HTTP listeners (`protocol = "admin"`) carry a `kind` field that
partitions the suite's HTTP surface into two roles:

- **`kind = "public"`** -- internet-facing. Serves the SPA mount
  point (Wave 3.7), JMAP, chat WebSocket, the HTTP send API, the call
  credential mint, the image proxy, public webhook ingress, and the
  public `/login` flow that issues end-user-scoped cookies. Default
  bind `0.0.0.0:443`.
- **`kind = "admin"`** -- operator-only. Serves the protoadmin REST
  surface, the admin UI, `/metrics`, and the admin `/login` flow that
  issues admin-scoped cookies after a TOTP step-up
  (REQ-AUTH-SCOPE-03). Default bind `127.0.0.1:9443` so the surface
  is invisible to internet scanners. Operators with a VPN flip the
  bind to a routable interface; operators without one tunnel via
  `ssh -L 9443:127.0.0.1:9443 admin@host`.

Cookies are mechanically distinct: the public listener issues
`herold_public_session`, the admin listener issues
`herold_admin_session`. Cross-listener cookie reuse is impossible at
the parser level; the in-handler `auth.RequireScope` check is
defence-in-depth.

A production config without an explicit `kind="admin"` listener is
**rejected at validate** with a migration message. Set
`[server.dev_mode] = true` to bypass the check during development;
`dev_mode` co-mounts both handlers on a single listener (the in-
handler scope check is the boundary in that shape).

```toml
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

[[listener]]
name = "imap"
address = "0.0.0.0:143"
protocol = "imap"
tls = "starttls"

[[listener]]
name = "imaps"
address = "0.0.0.0:993"
protocol = "imaps"
tls = "implicit"

# Public HTTP listener: SPA + JMAP + chat WS + send API + call creds +
# image proxy + public webhook ingress + public /login.
[[listener]]
name = "public"
address = "0.0.0.0:443"
protocol = "admin"
kind = "public"
tls = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

# Admin HTTP listener: protoadmin REST + admin UI + /metrics + admin
# /login (TOTP step-up).  Loopback by default; tunnel via ssh -L from
# operator workstations or flip the bind to a VPN interface.
[[listener]]
name = "admin"
address = "127.0.0.1:9443"
protocol = "admin"
kind = "admin"
tls = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"
```

Note: ManageSieve and JMAP do not have dedicated `protocol` values in
the current schema; ManageSieve runs as a separate process surface
TODO(operator-doc): managesieve-listener-shape, and JMAP is mounted
on the admin listener.

### `[[plugin]]` - out-of-process plugin declarations

Each plugin is a separate `[[plugin]]` table. Required: `name` (unique),
`path` (executable on disk), `type` (one of `dns`, `spam`, `events`,
`directory`, `delivery`), `lifecycle` (`long-running` or `on-demand`).
Optional: `options` (string-keyed string-valued map).

```toml
[[plugin]]
name = "spam-llm"
path = "/usr/lib/herold/plugins/herold-spam-llm"
type = "spam"
lifecycle = "long-running"
options.endpoint = "http://localhost:11434/v1"
options.model = "llama3.2:3b"
options.api_token_env = "$OLLAMA_TOKEN"        # secrets via env or file: only.

[[plugin]]
name = "cloudflare"
path = "/usr/lib/herold/plugins/herold-dns-cloudflare"
type = "dns"
lifecycle = "long-running"
options.api_token_env = "$CF_API_TOKEN"
```

Secret-bearing option keys (containing `secret`, `token`, `password`,
`api_key`, `apikey`, `credential`) must reference an env var
(`$VAR`) or a file (`file:/path`). Inline secret values are rejected
at validate (STANDARDS section 9). The substring match is
case-insensitive; rename a public field if it collides (e.g.
`api_key_url` -> `apikey_url_public`).

Plugin protocol is JSON-RPC 2.0 on stdio; lifecycle, restart policy,
and the type-specific RPC schemas live in
`docs/design/architecture/07-plugin-architecture.md` and
`docs/design/requirements/11-plugins.md`.

### `[server.snooze]` - JMAP snooze worker (REQ-PROTO-49)

```toml
[server.snooze]
poll_interval = "60s"          # default 60s; values below 5s rejected.
```

Snoozed messages wake when their `snoozedUntil` timestamp passes; the
poller drives the wake. Default 60 s; sub-5 s values are configuration
mistakes (sub-second snooze precision is not a JMAP guarantee).

### `[server.image_proxy]` - inbound HTML image proxy (REQ-SEND-70..78)

```toml
[server.image_proxy]
enabled = true                          # default true.
max_bytes = 26214400                    # 25 MiB per fetch (REQ-SEND-74).
cache_max_bytes = 268435456             # 256 MiB cache footprint.
cache_max_entries = 8192
cache_max_age_seconds = 86400
per_user_per_minute = 200               # REQ-SEND-77.
per_user_origin_per_minute = 10
per_user_concurrent = 8
```

Mounts `/proxy/image` on the admin listener. Operators behind an
upstream proxy that owns image rewriting can disable.

### `[server.categorise]`

LLM-driven message categorisation (REQ-FILT-200..220) lives in the
spam-plugin family but is operationally distinct.
TODO(operator-doc): categorise-config-block - the config shape lands
with the categorise feature in Wave 3.x.

### `[server.chat]` - chat ephemeral channel (Phase 2)

```toml
[server.chat]
enabled = true
max_connections = 4096           # global cap on /chat/ws connections.
per_principal_cap = 8            # per-user cap (one tab = one connection).
ping_interval_seconds = 30
pong_timeout_seconds = 60        # must be >= ping_interval_seconds.
max_frame_bytes = 65536          # 64 KiB; oversize closes with code 1009.
write_timeout_seconds = 10
allowed_origins = ["https://mail.example.com"]
allow_empty_origin = false       # browser fetch policy default.
```

Mounts `/chat/ws` on the admin listener. Validation enforces sane
caps: `per_principal_cap <= max_connections`, `pong_timeout_seconds >=
ping_interval_seconds`. Chat is Phase 2; toggle `enabled = false` to
leave the upgrade handler unmounted on a server where the chat client
has not shipped.

### `[server.chat.retention]` - chat retention sweeper (REQ-CHAT-92)

```toml
[server.chat.retention]
sweep_interval_seconds = 60       # default 60. Floor 10 (avoid pinning a writer).
                                  # Ceiling 86400 (typo guard).
batch_size = 1000                 # default 1000. Range [1, 10000].
```

### `[server.call]` - 1:1 video calls (Phase 2)

```toml
[server.call]
enabled = true
ring_timeout_seconds = 30        # REQ-CALL-06; cap 300 (5 min).
```

### `[server.turn]` - TURN credential mint (Phase 2)

```toml
[server.turn]
uris = ["turn:turn.example.com:3478", "turns:turn.example.com:5349?transport=tcp"]
shared_secret_env = "$HEROLD_TURN_SECRET"     # required when uris is set.
credential_ttl_seconds = 300                  # REQ-CALL-22; cap 12h.
```

When `uris` is non-empty, `shared_secret_env` must be a `$VAR` or
`file:/path` reference (STANDARDS section 9; no inline secrets). With
`uris` empty the credential mint endpoint returns 503 and chat falls
back to STUN-only ICE - that works for ~85-90% of network shapes; the
remaining 10-15% need TURN.

### `[server.tabard]` - tabard SPA mount on the public listener

```toml
[server.tabard]
enabled = true                    # default true; set false for admin-only deployments.
# asset_dir = "/abs/path/to/tabard/dist"   # dev-mode override; default unset.
```

Herold embeds the tabard SPA build artefacts into the binary and
serves them at `/` on the public listener (REQ-DEPLOY-COLOC-01..05).

- `enabled` toggles the SPA mount. `enabled = false` leaves the
  catch-all on the public mux empty so `/` returns the default 404;
  use this for admin-only deployments where the public listener
  exists only to terminate JMAP / send / chat / image-proxy traffic.
- `asset_dir`, when set, makes the server read SPA assets from disk
  on every request rather than from the embedded FS. Use this in
  development to avoid rebuilding the binary on every tabard change.
  The path MUST be absolute AND contain `index.html` at startup; the
  validator refuses the config otherwise.

The handler emits a strict `Content-Security-Policy` (no operator
override in v1; see REQ-DEPLOY-COLOC-04 for the directive set),
content-addressed asset caching with `Cache-Control: public,
max-age=31536000, immutable` for hashed assets, `max-age=3600` for
stable non-hashed assets, and `no-cache` for `index.html`. Unknown
non-API paths fall through to `index.html` so the SPA's client-side
router takes over.

The pinned tabard release the current herold binary embeds is
recorded in `deploy/tabard.version`. Operators who want a different
tabard version use the `asset_dir` override; see
`docs/user/install.md` for the embed-tabard workflow.

### `[server.ui]` - operator-facing web UI

```toml
[server.ui]
enabled = true
path_prefix = "/ui"
cookie_name = "herold_ui_session"
csrf_cookie_name = "herold_ui_csrf"
session_ttl = "24h"
secure_cookies = true            # production must keep this true.
signing_key_env = "$HEROLD_UI_KEY"   # empty = random per-process key.
```

`secure_cookies = true` is the production policy; only override
during local development behind a trusted localhost reverse proxy.

### `[acme]` - ACME account (Phase 2)

```toml
[acme]
email = "ops@example.com"
directory_url = "https://acme-v02.api.letsencrypt.org/directory"
```

Parsed but rejected at validate in Phase 1. Wave 3.1+ lights this up.

### `[observability]` - log + metrics + traces

```toml
[observability]
log_format = "json"                # "json" (default) or "text".
log_level = "info"                 # "debug" | "info" | "warn" | "error".
metrics_bind = "127.0.0.1:9090"    # default loopback. See note below.
otlp_endpoint = ""                 # empty = OTLP off.
```

The metrics endpoint is **unauthenticated** and binds to loopback by
default (REQ-OPS-90, STANDARDS section 7). If you publish a
non-loopback `metrics_bind`, herold logs a `slog` warning at startup
and on every SIGHUP - informational, does not block startup, but the
operator obligation is real: front it with TLS + auth at a reverse
proxy.

OTLP is off by default. When enabled, traces export over OTLP/HTTP to
the configured endpoint.

## TLS and ACME

### Cert sources

Two production cert sources (REQ-OPS-40):

1. **ACME-provisioned** (default for internet-facing). Phase 2.
2. **File-based.** `cert_file` + `key_file` per listener (or via
   `[server.admin_tls]` for the admin / JMAP vhost). Reloaded live
   on rotation; SIGHUP not required for cert refresh.

A third "embedded self-signed" mode exists for dev (REQ-OPS-40 #3) -
explicit flag only, never enable in production.

### ACME (Phase 2)

When ACME lands:

- Account key persisted at `<data_dir>/acme/`, mode 0600.
- Renewal at 1/3 remaining lifetime (60 days for a 90-day cert);
  exponential backoff on failure (REQ-OPS-53).
- Default directory: Let's Encrypt production. Staging is selectable
  via `letsencrypt-staging`. ZeroSSL, Buypass, private ACME CAs via
  URL.
- Challenge types: HTTP-01 on tcp/80, TLS-ALPN-01 on tcp/443, DNS-01
  via DNS-provider plugin.
- Rate-limit aware: respects ACME directory limits; backs off on 429.

### When ACME fails

- **`429 too many requests`:** you hit a Let's Encrypt rate limit.
  Switch the directory to `letsencrypt-staging` while iterating, then
  back to production.
- **HTTP-01 fails:** port 80 not reachable. Either firewall, a local
  process holding the port, or NAT not forwarding. Use TLS-ALPN-01
  on 443 if the operator owns 443 but not 80.
- **DNS-01 fails:** the DNS plugin's API token is wrong, expired, or
  scoped to the wrong zone. `herold diag dns-check <domain>` shows
  what herold believes the zone state is.

### File-based certs

```toml
[[listener]]
name = "imaps"
address = "0.0.0.0:993"
protocol = "imaps"
tls = "implicit"
cert_file = "/etc/letsencrypt/live/mail.example.com/fullchain.pem"
key_file  = "/etc/letsencrypt/live/mail.example.com/privkey.pem"
```

If the per-listener cert override is present, it wins for that
listener regardless of `[server.admin_tls]`. Both `cert_file` and
`key_file` must be set or both empty.

### Cert lifecycle visibility

Once protoadmin lands:

```bash
herold cert status                  # list each cert: hostname, issuer, NotBefore/NotAfter, SAN list, source.
herold cert show mail.example.com   # chain PEM, issuer, validity window.
herold cert renew mail.example.com  # force an immediate ACME renewal.
```

A `herold_tls_cert_expiry_seconds{hostname}` Prometheus gauge plus a
14-days-before-expiry log warn give the operator early notice
(REQ-OPS-71).

## DNS records you need to publish

For a hosted domain to be deliverable to and from, publish the
following at a minimum:

1. **A / AAAA** for `mail.example.com` -> the herold node's public IP.
2. **MX** for `example.com` -> `mail.example.com.` (priority 10).
3. **SPF (TXT)** at `example.com`:

   ```
   "v=spf1 mx -all"
   ```

4. **DKIM (TXT)** at `<selector>._domainkey.example.com`:

   The DKIM TXT body is generated when the operator runs `herold
   domain add example.com` (or `herold domain dkim show example.com`
   - planned, Wave X.Y, see REQ-ADM-310). Copy-paste the printed TXT
   record into the zone, or configure a DNS-provider plugin and
   herold will publish it itself (REQ-OPS-60).

Recommended on top:

5. **DMARC (TXT)** at `_dmarc.example.com`:

   ```
   "v=DMARC1; p=quarantine; rua=mailto:dmarc-reports@example.com"
   ```

6. **MTA-STS (TXT)** at `_mta-sts.example.com`:

   ```
   "v=STSv1; id=20260101000000Z;"
   ```

   Plus the policy file served at
   `https://mta-sts.example.com/.well-known/mta-sts.txt`. Herold
   serves the policy out of its admin vhost when MTA-STS is enabled
   for the domain.

7. **TLS-RPT (TXT)** at `_smtp._tls.example.com`:

   ```
   "v=TLSRPTv1; rua=mailto:tlsrpt-reports@example.com"
   ```

8. **DANE TLSA** (optional; requires DNSSEC) at
   `_25._tcp.mail.example.com` - published automatically by herold on
   cert issuance / renewal if the domain has DANE enabled and a DNS
   plugin is bound (REQ-OPS-61).

When a DNS-provider plugin is configured, `herold domain add`
publishes (1)-(7) for you (REQ-OPS-60). Without a plugin, herold
prints the records and the operator pastes them into the zone
manually (REQ-OPS-63).

To verify what is actually published vs. what herold expects:

```bash
herold diag dns-check example.com
```

(REQ-OPS-65, REQ-ADM-311.)

## Smart host

To deliver outbound mail through a relay (Gmail SMTP, AWS SES,
SendGrid, a corporate MTA) instead of opening direct SMTP connections
to the public internet, configure a smart host. The example
[./examples/system.toml.smarthost](./examples/system.toml.smarthost)
shows the target shape per the REQ-FLOW-SMARTHOST spec landing in
Wave 3.1.

Why an operator chooses a smart host:

- ISP / cloud provider blocks tcp/25 outbound (very common - AWS,
  GCP, Azure, residential ISPs).
- The operator already pays for SES / SendGrid deliverability and
  reputation.
- Outbound traffic should funnel through a corporate egress for audit.

The shape (planned):

```toml
[smart_host]
host = "smtp.gmail.com"
port = 587
auth_method = "plain"             # "plain" | "login" | "xoauth2"
username = "you@gmail.com"
password_env = "$GMAIL_APP_PASSWORD"   # env or file: only.
tls_mode = "starttls"             # "starttls" | "implicit"
fallback_policy = "queue"         # "queue" | "fail" - what to do if the relay is down.
```

The implementation lands in Wave 3.1; until then the example file
documents the target config shape and the queue-delivery code path
talks plain SMTP to MX records directly.

## Backup and restore

Herold's backup format is a **bundle**: a directory containing a
`manifest.json`, one JSONL file per metadata table, and a
content-addressed `blobs/` tree (REQ-OPS-120, REQ-STORE-60..63).
Concurrent application writes are allowed during the backup; the
snapshot is consistent at the start of the run.

### Take a backup

```bash
herold diag backup --to /var/backups/herold/2026-04-25
```

The bundle directory contains:

- `manifest.json` - schema_version, backup_version, created_at,
  backend, per-table row counts, blob count and bytes, total bytes.
- `<table>.jsonl` - one JSONL file per metadata table.
- `blobs/<sha256-prefix>/<sha256>` - content-addressed message
  blobs.

The full manifest schema lives in
`internal/diag/backup/manifest.go`. Current `BackupVersion` is 1;
restore tooling refuses bundles with a higher version so a future
incompatible bump is caught at the earliest point.

### Verify a bundle

```bash
herold diag verify --bundle /var/backups/herold/2026-04-25
```

Re-counts each JSONL and verifies blob hashes against the manifest.
Read-only; no store access.

### Restore a bundle

Restore is offline. Stop the server, point `system.toml` at the
target store, then:

```bash
herold diag restore --from /var/backups/herold/2026-04-25 --mode fresh
```

`--mode` controls conflict handling:

- `fresh` (default) - requires the target store be empty.
- `merge` - skips rows that already exist (idempotent re-apply).
- `replace` - truncates each target table before re-inserting.

Restart the server. Verify with `herold server status` and
`/healthz/ready`.

### Off-site backup

V1 does not implement remote backup destinations (REQ-OPS-123). The
operator uses standard tooling: `rsync`, `restic`, `borg`, or a cloud
snapshot of the data volume.

## Upgrades and migration

### Routine upgrade (binary swap)

1. Take a backup (`herold diag backup`).
2. Stop the server (`systemctl stop herold` or SIGTERM with the
   30 s graceful drain - REQ-OPS-141).
3. Replace the binary.
4. Start the server. Schema migrations are applied incrementally on
   startup (REQ-OPS-130). Migrations are forward-only; downgrade is
   explicitly rejected.

If the new binary has more migrations than the running schema
expects, the start-up applies them automatically. If a migration
fails, the server refuses to start and logs the failing step; the
backup from step 1 is the recovery path.

### Backend migration (SQLite -> Postgres or vice versa)

```bash
herold diag migrate \
  --to-backend postgres \
  --to-dsn "postgres://herold:secret@localhost:5432/herold?sslmode=verify-full" \
  --to-blob-dir /var/lib/herold/blobs
```

Migrations are offline: stop the server, run migrate, point
`system.toml` at the new backend, start the server. Both stores must
be at the same schema version; the target must be empty. Blob hashes
are verified during copy; FK-respecting insert order is preserved.

## Observability

### Logs

JSON-structured by default (REQ-OPS-80). Every log line carries a
timestamp (RFC 3339 with timezone), level, module, message, and
correlation IDs (request, session, principal) where applicable.
Operator changes via `system.toml`:

```toml
[observability]
log_format = "json"      # "json" (default) | "text"
log_level = "info"       # "debug" | "info" | "warn" | "error"
```

Default destination is stderr (suits systemd, container runtimes,
Docker). For non-supervised installs, redirect via shell or use
`logrotate`. Sensitive values are redacted at log time: passwords,
API keys, bearer tokens, session cookies (REQ-OPS-84). DKIM private
keys are never logged.

Per-module level overrides
(`logging.modules.smtp = "debug"` shape) are planned per REQ-OPS-82
but TODO(operator-doc): module-log-level-config-shape - not yet
exposed in the system.toml schema.

### Metrics

Prometheus-format on `/metrics`. Default bind is `127.0.0.1:9090`.
The handler does not perform authentication. The minimum metric
families (REQ-OPS-91):

- `herold_smtp_connections_total{listener,status}`
- `herold_smtp_messages_total{direction,status}`
- `herold_imap_sessions_active`
- `herold_jmap_requests_total{method,status}`
- `herold_queue_size{stage}`, `herold_queue_oldest_seconds`
- `herold_delivery_attempts_total{status}`,
  `herold_delivery_duration_seconds` (histogram)
- `herold_spam_verdict_total{verdict}`,
  `herold_spam_confidence` (histogram),
  `herold_spam_classifier_latency_seconds`,
  `herold_spam_classifier_failures_total`
- `herold_plugin_invocations_total{plugin,method,status}`,
  `herold_plugin_latency_seconds{plugin,method}`,
  `herold_plugin_state{plugin}`,
  `herold_plugin_restarts_total{plugin}`
- `herold_storage_bytes{type}`, `herold_storage_messages_total{type}`
- `herold_tls_cert_expiry_seconds{hostname}`
- `herold_auth_attempts_total{protocol,result}`
- Go runtime (`go_goroutines`, `go_memstats_*`, `go_gc_*`).

Operator obligation per STANDARDS section 7: if `metrics_bind` is
non-loopback, front `/metrics` with TLS + auth at a reverse proxy.
Herold logs a `slog` warning at startup and on every SIGHUP when the
bind resolves to a non-loopback address - informational, not blocking.

### Traces (OTLP)

Off by default. Enable with `otlp_endpoint`:

```toml
[observability]
otlp_endpoint = "http://otelcol:4318"
```

Spans cover full SMTP sessions, IMAP commands, JMAP requests, mail
delivery attempts, spam classification, Sieve execution, ACME
renewal, plugin calls (REQ-OPS-101). Trace context propagates across
internal async boundaries.

No built-in trace storage - ship to Jaeger / Tempo / Datadog / etc.

### Health endpoints

- `GET /healthz/live` - liveness. 200 if the process is running.
- `GET /healthz/ready` - readiness. 200 only if the store is open,
  listeners are bound, the ACME account loaded (when applicable),
  every critical plugin is up, and no critical errors fired in the
  last N seconds. 503 otherwise.

Both are unauthenticated (REQ-OPS-112) and exposed on the admin
listener.

## Queue triage

The outbound queue carries every message en route to delivery.
Inspecting and acting on stuck messages is a routine operator task.

### Inspect

```bash
herold queue list                       # list all queued items.
herold queue list --state deferred      # filter by lifecycle state.
herold queue list --principal 7         # filter by principal id.
herold queue list --limit 100 --after 12345    # paginate via keyset.
herold queue stats                      # per-state counts.
herold queue show <id>                  # full record for one item.
```

Lifecycle states: `queued`, `deferred`, `inflight`, `done`, `failed`,
`held`.

### Act

```bash
herold queue retry <id>          # bump the row to retry now.
herold queue hold <id>           # move to held; will not retry until released.
herold queue release <id>        # held -> queued.
herold queue delete <id>         # operator force-delete (interactive confirm).
herold queue flush --state deferred    # bump every deferred row to retry now.
```

`herold queue delete` and `herold queue flush` prompt for a literal
`yes`; pass `--force` to skip the prompt for scripted runs.

### Common shapes

- **Massive `deferred` backlog.** A downstream MX is offline. Look at
  the `last_status` field on a sample `queue show <id>`. If the issue
  is transient and you want to retry now, `queue flush --state
  deferred --force`.
- **Items stuck in `inflight`.** A delivery worker hung. Worker
  restart releases stuck items back to `queued` after the
  in-flight-deadline expires.
- **Repeated `failed` items for one destination.** Check whether the
  destination's SPF / DMARC / TLS posture changed; some receivers
  refuse on DMARC alignment failure (publish your DMARC TXT) or on
  STARTTLS missing a hostname match.

## Plugin lifecycle

Plugins run as **separate processes** speaking JSON-RPC 2.0 over
stdio (STANDARDS section 1.2). The supervisor in
`internal/plugin/` starts every plugin declared with
`lifecycle = "long-running"` at server startup and respawns on
crash (with backoff). `lifecycle = "on-demand"` plugins start when
first invoked and idle out.

### Inspect plugin state

`herold_plugin_state{plugin}` and `herold_plugin_restarts_total{plugin}`
on `/metrics` give the live state.

### Debug a hung plugin

A plugin that wedges (deadlocked, GC pause loop, awaiting a remote
that never replies) shows up as elevated
`herold_plugin_latency_seconds{plugin,method}` and rising
`herold_plugin_invocations_total{plugin,method,status="error"}`. The
operator's options:

- **Restart the plugin only:** the supervisor watches the subprocess
  exit; sending the subprocess `SIGTERM` (or `SIGKILL` if it
  ignores SIGTERM) makes the supervisor respawn. Find the PID with
  `pgrep -f herold-spam-llm` (substituting your plugin name); send
  `kill -TERM <pid>` then `kill -KILL <pid>` if needed.
- **Restart herold:** `systemctl restart herold` reloads every
  plugin from scratch.

### Reload after editing `[[plugin]]` block

Plugin list reconciliation is part of SIGHUP reload (REQ-OPS-30):
new plugins start, removed plugins stop. Edit `system.toml`, run
`herold server reload` (or send `SIGHUP` directly).

## OIDC RP (federated login)

Herold authenticates principals against:

1. Local password + TOTP (REQ-AUTH-*).
2. **External OIDC providers** the operator registers - herold is a
   relying party only, never an issuer (NG11).

Per-user federation: a principal can link to one or more external
OIDC providers (Google, Microsoft, GitHub, corporate Okta, etc.), and
the external email need not match the local canonical email.

### Register a provider

```bash
herold oidc provider add google \
  --issuer https://accounts.google.com \
  --client-id <id> \
  --client-secret <secret>
```

(The CLI accepts `--client-secret` for now; once protoadmin's
secret-env shape is in place, prefer
`herold oidc provider update <name> --client-secret-env=$VAR` so the
secret never lands in shell history.)

### List / show / update / remove

```bash
herold oidc provider list
herold oidc provider show google
herold oidc provider update google --client-secret-env=$GOOGLE_OIDC_SECRET
herold oidc provider remove google
```

### Link / unlink a principal

The operator-side commands are:

```bash
herold oidc link-list user@example.com
herold oidc link-delete user@example.com google
```

The link-*creation* path (a user signs in via Google, herold matches
or claims the account) is a Phase-2 user-flow, not an operator CLI
command. See `docs/design/requirements/02-identity-and-auth.md`
for the registered-user OIDC sign-in flow.

## Common operational issues

### "Outbound mail just sits in the queue"

Likely your ISP / cloud provider blocks tcp/25 outbound. Symptoms:
`queue list --state deferred` accumulates; `last_status` shows
`connection refused` or `timeout`. Workaround: configure a smart host
(see "Smart host" above) - funnel outbound through SES / SendGrid /
Gmail relay / a corporate MTA. Smart-host implementation lands in
Wave 3.1.

### "ACME keeps hitting rate limits"

Let's Encrypt enforces aggressive rate limits per registered domain
and per IP. Symptoms: ACME order fails with `429 too many requests`
or `urn:ietf:params:acme:error:rateLimited`. Mitigations:

- Switch to staging while iterating:
  `directory_url = "https://acme-staging-v02.api.letsencrypt.org/directory"`.
- Wait. Limits reset hourly / weekly depending on the limit.
- Use ZeroSSL or Buypass as a fallback ACME directory.

### "FTS results look stale or missing"

Bleve indexes new mail asynchronously after the store commits the
message. Sub-second on a healthy node; if you see persistent staleness
it usually means the FTS index file is corrupt.

Recovery: stop the server, delete the FTS index directory under the
data dir, restart. Herold rebuilds the index from the change feed
(REQ-STORE-* / REQ-FTS-*). For a 1 TB mailbox the rebuild is
minutes-to-hours of indexing throughput; incremental indexing on
new mail is sub-second.

### "Storage full"

The data directory is on a volume that filled up. In rough priority:

1. Free space immediately by rotating logs (`logrotate -f
   /etc/logrotate.d/herold`) or moving the log file.
2. Check the queue for stuck mail with attachments
   (`herold queue list` and look for large `size` values). Delete
   spam / stuck rows that should not retry.
3. Run `herold diag backup` to a different volume, then prune older
   mail per retention policy. (Retention CLI: TODO(operator-doc):
   retention-prune-cli.)
4. Consider migrating to a larger volume; extend on the fly with
   LVM / ZFS / cloud-volume resize, then restart herold.

### "Bleve index corruption"

A crashing host or an external `kill -9` to a long-running plugin can
rarely leave the Bleve index inconsistent. Symptom: search returns
errors or never converges on new mail. Recovery: stop the server,
remove the FTS index directory, restart. Herold rebuilds from the
change feed.

### "DMARC alignment fails on inbound"

Receiving servers refuse mail when the domain's DMARC TXT is missing
or its policy is `reject` and SPF / DKIM do not align. Publish (or
fix) the DMARC TXT and re-run `herold diag dns-check example.com`.

## Signals

- **`SIGTERM`** - graceful shutdown. Stop accepting new connections,
  drain in-flight requests up to `[server] shutdown_grace` (default
  30 s), stop plugins, then force close (REQ-OPS-141). The PID file
  is removed.
- **`SIGINT`** - same as SIGTERM for interactive runs (Ctrl-C from a
  foreground `herold server start`).
- **`SIGHUP`** - system-config reload. Diff applied live where
  possible: bind changes, TLS source, plugin list, log level.
  Settings that require restart (`data_dir`, `run_as_user`,
  `run_as_group`) are reported and rejected as reloads (REQ-OPS-32).
  Application config never needs SIGHUP - DB-backed mutations are
  live. Use `herold server reload` to send SIGHUP to the running
  process from another shell.

## Performance tuning

The bottlenecks at the v1 scale target (1k mailboxes, 10k+10k
msg/day) are:

1. **Bleve FTS indexing throughput on bulk import.** First-time index
   of a 1 TB mailbox is minutes-to-hours. Incremental indexing on new
   mail is sub-second.
2. **SQLite write contention under sustained concurrent large-mailbox
   writes.** WAL mode is on; mmap is on. If you see contention, the
   right answer is usually Postgres. Knobs (ordered by leverage):

   - `PRAGMA wal_autocheckpoint` (default 1000 pages). Lower for
     less peak latency, higher for less I/O. TODO(operator-doc):
     sqlite-pragma-config-block.
   - `PRAGMA cache_size`. Default is small; raising helps repeated
     queries.

3. **Queue concurrency.** Outbound delivery is bounded by a worker
   semaphore. Defaults are sensible for the v1 target; tune if your
   message rate exceeds 100 msg/s peak. Knob:
   TODO(operator-doc): queue-concurrency-config-shape.

4. **Plugin RPC latency.** Each spam classification is a JSON-RPC
   round-trip plus the LLM call. At 15 msg/min peak this is trivial;
   at higher rates either run a faster local model (Ollama on GPU) or
   pool calls via a remote OpenAI-compatible endpoint.

The general principle: **measure first**. Pull
`herold_*_latency_seconds` histograms from `/metrics` before tuning.
Premature pragma-tuning is the wrong answer.

## Where to next

- Application administration (domains, principals, mailboxes,
  aliases, API keys, Sieve, audit log): [./administer.md](./administer.md).
- Real-domain walkthrough (DNS records, ACME, DKIM, DMARC, MTA-STS):
  [./quickstart-extended.md](./quickstart-extended.md).
- The historical record (requirements, architecture, implementation):
  the `docs/design/` tree.
