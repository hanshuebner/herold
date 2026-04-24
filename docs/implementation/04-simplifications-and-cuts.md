# 04 — Simplifications and cuts vs. Stalwart

*(Revised 2026-04-24 after multiple scope shifts: rescale, LLM-only spam, plugins in v1, Postgres first-class, HTTP APIs added, events added, Go language, no encryption at rest, no IdP issuer.)*

Explicit list of what Herold does differently from Stalwart. Each entry: what, why, and reversal cost. The purpose is to force honesty about scope.

## Confident cuts (Stalwart has it, Herold doesn't, we don't miss it)

### C1. Multi-tenancy
- **Stalwart**: tenants, per-tenant quotas, tenant-scoped domains, tenant-scoped permissions.
- **Herold**: none. Multiple instances for multiple brands.
- **Reversal cost**: very high (tenant_id on most tables). We won't.

### C2. Multi-node
- **Stalwart**: in progress with shared storage.
- **Herold**: non-goal. Single node forever.
- **Reversal cost**: architectural rewrite. Intentionally not kept open.

### C3. Traditional spam filtering
- **Stalwart**: Rspamd-class rule engine + Bayesian + RBLs + LLM (enterprise).
- **Herold**: LLM classification only, via spam plugin. No rules, no Bayes, no RBLs bundled.
- **Reversal cost**: moderate. Operators can add a rule-engine plugin.

### C4. Alternate storage backends beyond SQLite and Postgres
- **Stalwart**: SQLite, Postgres, MySQL, RocksDB, FoundationDB, composite.
- **Herold**: SQLite and Postgres. That's it.
- **Reversal cost**: low per backend via store interface, but we won't.

### C5. Alternate blob backends
- **Stalwart**: FS, S3, Azure, Postgres, MySQL, FoundationDB, sharded.
- **Herold**: filesystem only. Single node; no remote blob stores.
- **Reversal cost**: low-moderate but out of scope per NG8.

### C6. Redis / Memcached in-memory store
- **Stalwart**: yes.
- **Herold**: in-process only.
- **Reversal cost**: moderate.

### C7. SNMP, custom event streams, webhooks-for-everything
- **Stalwart**: yes, multi-channel observability.
- **Herold**: JSON logs + Prometheus + OTLP + event-publisher plugins (NATS default). No SNMP, no "events" stream separate from plugin-published events.
- **Reversal cost**: low.

### C8. Full Rspamd rule DSL and ecosystem
- **Stalwart**: near-parity with Rspamd.
- **Herold**: not shipped; plugin route if operators want.
- **Reversal cost**: moderate-high.

### C9. Enterprise license system
- **Stalwart**: Ed25519-signed key, account limits, phone-home renewal.
- **Herold**: no. Ever.
- **Reversal cost**: we won't.

### C10. Soft-delete with retention
- **Stalwart enterprise**: retention-window-restorable archived mail / accounts.
- **Herold**: Trash folder for user-deleted mail; operator-initiated account delete is final.
- **Reversal cost**: moderate.

### C11. Custom email / calendar / web templates (enterprise)
- **Stalwart enterprise**: override defaults per tenant.
- **Herold**: single operator-configured default. No templating DSL.
- **Reversal cost**: low.

### C12. Masked / auto-generated alias format with checksums
- **Stalwart enterprise**: algorithmic aliases with expiry + checksum.
- **Herold**: plain aliases with optional expiry column.
- **Reversal cost**: low.

### C13. Custom logos per tenant
- **Stalwart enterprise**: yes.
- **Herold**: one optional operator logo. No tenant dimension.
- **Reversal cost**: low.

### C14. Encryption at rest (any form)
- **Stalwart**: optional enterprise feature.
- **Herold**: **not implemented.** Operators use volume-level encryption (LUKS/ZFS native/FileVault) — standard OS answer.
- **Reversal cost**: moderate (SQLCipher for SQLite, envelope encryption for blobs, custom Bleve Directory — each non-trivial). Not planned.

### C15. SES bit-compat
- **Stalwart**: none (they have their own admin HTTP API).
- **Herold**: portable-from-SES HTTP send API + webhooks. Not SigV4, not `ReceiptRule` DSL, not SNS-verbatim. Apps port in a day, not a minute.
- **Reversal cost**: high if we were to go SES-verbatim; low if we incrementally extend ours.

### C16. Acting as an OIDC issuer
- **Stalwart**: full OIDC + OAuth 2 AS.
- **Herold**: **relying party only.** We federate *in* from external OIDC providers on a per-user basis; we do not hand out tokens for external apps to consume. Non-goal NG11.
- **Reversal cost**: high.

### C17. Groupware (CalDAV/CardDAV/WebDAV)
- **Stalwart**: yes, first-class.
- **Herold**: **dropped entirely.** Not a phase-3 candidate. Operators who want calendar/contacts run Radicale / Baikal.
- **Reversal cost**: significant — effectively a new project.

### C18. Language: Rust → Go
- **Stalwart**: Rust.
- **Herold**: Go. Chosen for compile-time; we don't need Rust's runtime advantages at our scale.
- **Reversal cost**: rewrite.

## Additions — things Stalwart doesn't do (or does worse) that we prioritize

### A1. Deterministic one-command DNS automation
- Server auto-publishes DKIM/MTA-STS/TLSRPT/DMARC/DANE via configured DNS plugin. `herold domain add` is one command; no copy-paste.
- Stalwart emits record text; operator publishes. We close the loop.

### A2. First-class HTTP mail API + SES-portable shape
- `POST /api/v1/mail/send` with tags/configuration_set/idempotency for app integrations. Porting from SES is documented and tractable.

### A3. First-class incoming-mail webhooks
- Register a webhook per address/domain/principal; new mail → HTTP POST with body access (inline or signed fetch URL). Automation counterpart to JMAP push.

### A4. Events + pluggable publication
- Typed event stream → `event-publisher` plugins. NATS ships default. Kafka/SQS/Redis Streams/custom via operator-added plugins.

### A5. Per-user external OIDC federation (not directory-level)
- Any user can link any number of external IdPs. External email need not match local. Clean model: local principal is canonical; external tokens auth against `sub` claim mapping.

### A6. Single storage split decision
- Operator picks SQLite or Postgres at install; both first-class, both CI-tested, clean migration tool between them. No "six backends" to pick from.

### A7. Download rate limits
- Per-principal and per-session bandwidth ceilings on IMAP FETCH / JMAP download / webhook fetch. Baked in, not plugin.

### A8. FTS with attachment content in v1
- PDF / DOCX / XLSX / PPTX / plain text indexed. Stalwart indexes less without the right config; ours works out of the box.

### A9. Backup/restore that actually specifies what's included
- `diag backup` produces a consistent bundle: metadata dump + blobs + ACME state + audit log + webhook configs + event-plugin configs. System config referenced by path (doesn't leak secrets into backups).

### A10. `diag dns-check <domain>` + `diag collect` support bundle
- Operator ergonomics Stalwart only partially has.

### A11. Plugin system from day one
- Out-of-process JSON-RPC. DNS providers, spam, events, directory, delivery hooks. Operators extend rather than wait for core to grow.

## Cuts with tension (could reconsider)

### T1. POP3
Dropped entirely. No phase-3 candidacy. If a real user shows up with a hard POP3 requirement, it's ~2 weeks to add.

### T2. Per-user Bayesian (if we ever bring rules back)
Not relevant under LLM-only approach. Would become relevant only if we reintroduce traditional filtering.

### T3. External AV (ClamAV, Sophos)
Not bundled. Operators who need it write a `delivery-pre` plugin that calls ClamAV daemon and returns reject/allow.

## Items Stalwart also doesn't do (for completeness)

- Webmail client.
- Exchange-compatible protocols (MAPI/EWS/ActiveSync).
- MAPI over HTTP.

## How to use this doc

When iterating:
- Cutting more → add entry to Confident Cuts or Cuts With Tension.
- Adding back something cut → move entry and add matching REQs.
- Adding new stuff → add to Additions first, then promote to REQs when agreed.

Every reversal costs implementation time. Every add costs scope. This doc is the budget.
