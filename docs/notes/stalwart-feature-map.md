# Stalwart feature map (reference)

A reference snapshot of what Stalwart Mail Server does, organized to cross-reference with Herold's scope decisions. This is descriptive of Stalwart, not prescriptive for us.

Sourced from research against the Stalwart source tree (as of April 2026) during initial scoping. May drift; re-verify against Stalwart's repo if relitigating a cut.

## Cargo feature flags in Stalwart

Stalwart ships ~40 cargo features. The significant ones:

| Feature | What it enables |
|---|---|
| `sqlite`, `postgres`, `mysql`, `rocks`, `foundationdb` | meta store backends |
| `s3`, `azure` | blob store backends |
| `redis`, `nats` | in-memory / pub-sub backends |
| `elasticsearch` | alternate FTS |
| `enterprise` | the enterprise-only feature set (see below) |
| `test_mode` | development-only license shortcut |

Herold's Go build tags: `!postgres` only. Default build includes Postgres. See `implementation/01-tech-stack.md`.

## Protocols supported by Stalwart

- SMTP in (25) and submission (587, 465)
- IMAP4rev1 and rev2, with major extensions (CONDSTORE, QRESYNC, UTF8, COMPRESS, etc.)
- JMAP Core + Mail (8620, 8621)
- JMAP for Calendars / Contacts (draft) — Stalwart has implemented partial
- POP3
- ManageSieve
- CalDAV, CardDAV, WebDAV
- HTTP admin API and built-in Web UI

Herold matches: SMTP, IMAP, JMAP Mail, ManageSieve. Drops entirely: POP3, CalDAV, CardDAV, WebDAV, JMAP Calendars/Contacts, generic WebDAV file storage.

## Sieve extensions in Stalwart

Full list (per `stalwart/crates/common/src/scripts/...`):
- core + fileinto + reject + envelope + imap4flags + body + vacation + relational + subaddress + regex + copy + include + variables + date + mailbox + mailboxid + encoded-character + editheader + duplicate + enotify + spamtestplus + foreverypart + mime + extlists
- extras: `llm` (enterprise), `eval`, possibly `execute` variants

Herold: all of the above except the last three (`llm`, `eval`, `execute`). See REQ-PROTO-60..68.

## Directory backends in Stalwart

- Internal
- SQL (MySQL/Postgres/SQLite tables)
- LDAP
- OIDC (auth only, with internal provisioning)
- Memory (testing)

Herold: **Internal only** (password + TOTP) + per-user external OIDC federation. Drops LDAP, SQL-table directory, memory backend.

## Storage backends in Stalwart

Meta:
- SQLite
- RocksDB
- Postgres
- MySQL
- FoundationDB
- Composite (read replicas, failover)

Blob:
- Filesystem
- S3
- Azure
- Postgres (LOB)
- MySQL (BLOB column)
- FoundationDB
- Sharded (composite)

In-memory:
- Internal (in-process)
- Redis (single / cluster / sharded)
- NATS KV

FTS:
- Internal
- ElasticSearch

Herold: SQLite meta, FS blobs, internal in-memory, internal FTS (Tantivy). S3 blobs phase 2. Postgres meta phase 3+.

## Email authentication in Stalwart

- DKIM sign + verify (RSA + Ed25519)
- SPF verify
- DMARC evaluate + aggregate report generation + ingest
- ARC verify + re-seal
- MTA-STS fetch + enforce + publish
- DANE verify
- TLS-RPT ingest + emit

Herold: same surface. Different implementation (REQ-SEC-*).

## Observability in Stalwart

- Structured logs
- Events (separate stream)
- Metrics (enterprise: Prometheus, OTEL)
- Traces (enterprise: OTEL)
- Webhooks for events
- SNMP

Herold: JSON logs + Prometheus (free, always on) + OTLP traces (optional). No events stream, no webhooks, no SNMP. See REQ-OPS-50..73.

## Spam filtering in Stalwart

- Rspamd-class rule engine with extensive rule library
- Bayesian classifier
- RBLs / URIBLs
- Reputation (internal + external)
- LLM classification (enterprise)
- Sieve plugin `llm` (enterprise)

Herold: built-in rules + Bayesian + RBL/URIBL (configurable). No LLM, no Sieve `llm`. No per-user Bayesian in v1. See REQ-FILT-*.

## Admin in Stalwart

- REST API
- CLI (`stalwart-cli`)
- Web UI (React SPA, bundled)
- Built-in DNS record suggestions
- Tenant-aware (enterprise)

Herold: REST + CLI for v1. Web UI phase 3. No tenants. See REQ-ADM-*.

## Enterprise features in Stalwart

Already inventoried in detail in prior conversation. Summary:

- License key with Ed25519 signature, domain binding, account limit, API renewal.
- Multi-tenancy.
- Tenant quotas.
- Live metrics + live traces APIs.
- Prometheus exposition.
- OTLP trace export.
- Metric alerts.
- Trace/metrics retention.
- LLM spam + Sieve LLM.
- Sharded storage (blob, in-memory, SQL replicas).
- Soft-delete mail and accounts.
- Custom calendar templates.
- Custom logos.
- Masked email addresses.

Herold implements: Prometheus, OTLP traces (at parity, free). Drops everything else (see `implementation/04-simplifications-and-cuts.md`).

## Deployment shapes Stalwart supports

- Single binary (common)
- Docker image (official, multi-arch)
- Kubernetes (no official chart but K8s-friendly)
- Multi-node (partial; they've been working toward it with shared storage)

Herold targets: same first three. Multi-node is phase 4+, if pursued.

## License positioning

- Stalwart core: AGPL-3.0 (most files)
- Stalwart enterprise module: SEL (proprietary license file, tampering prohibited)
- Commercial terms: license keys purchased for enterprise features

Herold:
- Open source license TBD (AGPL-3.0 or MPL-2.0 — open-questions)
- No enterprise split
- No proprietary code paths

## Takeaway for reimplementation

Stalwart is the most ambitious free-software mail server project in years, and it's excellent engineering. The places we diverge:

1. **Scope.** They aim for feature-complete across small-ops and enterprise. We aim for excellent single-node self-host with an honest "this is what you get" pitch.
2. **Observability model.** They have many channels with some paywalled. We ship one cohesive, always-free observability surface.
3. **Storage menu.** They support every storage backend; we pick one and do it well.
4. **No enterprise gates.** Whatever we ship, we ship everywhere.

Use this map when weighing cuts: if a cut moves us substantially away from parity *and* the feature is broadly useful (not Stalwart-enterprise-only), reconsider.
