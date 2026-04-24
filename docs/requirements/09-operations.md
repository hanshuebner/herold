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

[logging]
format = "json"
level = "info"
destination = "stderr"

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

- **REQ-OPS-80** Logs are **JSON structured by default**. Text logfmt as alternate via config. Field names stable and documented.
- **REQ-OPS-81** Log destinations: stdout/stderr (for systemd, container runtimes), file with rotation (when not running under a manager). Syslog via `syslog(3)` on Unix optional.
- **REQ-OPS-82** Log levels: `trace`, `debug`, `info`, `warn`, `error`. Default `info`. Per-module level overrides (`logging.modules.smtp = "debug"`).
- **REQ-OPS-83** Every log line includes: timestamp (RFC 3339 with timezone), level, module, message, request/session correlation ID where applicable.
- **REQ-OPS-84** Sensitive values redacted at log time: passwords, API keys, bearer tokens, session cookies, LLM spam prompt bodies at `info` level. DKIM private keys never logged.

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

## What we don't build

- SNMP trap receiver or MIB.
- Alerting engine (delegate to Prometheus/Alertmanager).
- Webhook-as-events streams (alerting via metrics is cleaner).
- Custom bundled Grafana dashboards (provide examples in docs).
