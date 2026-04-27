# 00 — Scope and non-goals

**2026-04-26** (rev 9): co-deployment shape locked. Herold ships
the suite SPA as embedded static assets (REQ-DEPLOY-COLOC-01..05);
the deployment topology splits into a public listener (default
`0.0.0.0:443`, serves SPA + JMAP + chat WS + send/call APIs +
webhooks) and an admin listener (default `127.0.0.1:9443`, serves
admin REST + admin UI + /metrics) per REQ-OPS-ADMIN-LISTENER-01..03.
Auth scopes (REQ-AUTH-SCOPE-01..04) carry a closed-enum scope set
on session cookies and API keys, with admin step-up via TOTP and
cross-scope rejection at every handler boundary. The deployment
target remains "small family/association/group" -- 5-50 users on
a single VPS -- not enterprise scale; the design optimises for
operator simplicity (one binary, two ports, one config file) and
"end-user compromise cannot escalate to operator-level damage".

**2026-04-26** (rev 8): Web Push (RFC 8030 + RFC 8620 §7.2 PushSubscription
+ the suite's enriched payload) advanced from "phase 3, deferred" to **phase 1**
(REQ-PROTO-48 amended; REQ-PROTO-120..127 added; REQ-OPS-180..184 added).
Driven by the suite's `requirements/25-push-notifications.md` — push is essential
for the mail experience (a user who can't be told about new mail isn't really
using a mail client). Outbound push gateway: per-subscription rule evaluation
→ enriched-or-minimal payload → RFC 8291 encryption → RFC 8292 VAPID-authed
POST. Per-(subscription, thread/conversation) coalescing within 30 s windows.
Bodies never pushed beyond 80-char previews. Deployment-level VAPID key pair
managed under herold's secrets-handling rules.

**2026-04-26** (rev 7): shortcut coach datatype added (REQ-PROTO-110..114,
phase 2). New per-principal `ShortcutCoachStat` JMAP datatype backing
the suite's behaviour-driven keyboard-shortcut coach — small windowed
counters (14d / 90d) per (principal, action) recording mouse vs
keyboard invocation history. Private to the principal; not subject to
admin reads. Optional state-change-feed integration (clients don't
subscribe to their own writes). Storage trivial (~30k rows for 1k
principals). Driven by the suite's `requirements/23-shortcut-coach.md`.

**2026-04-26** (rev 6): email reactions extension added (REQ-PROTO-100..103,
REQ-FLOW-100..108, phase 2). Same-server reactions stored as the
`Email.reactions` extension property; cross-server reactions propagate as
outbound emails with `X-Tabard-Reaction-*` headers + readable body
fallback. Inbound pipeline detects the headers and applies as native
reactions when the referenced original is in the recipient's mailbox;
falls through to normal delivery otherwise. Driven by the suite's emoji-react
UI (`docs/design/web/requirements/02-mail-basics.md` § Reactions).

**2026-04-26** (rev 5): explicit substrate-side support for two
operator-deployment shapes that were ambiguous in earlier revisions:
SMTP smart host for outbound (REQ-FLOW-SMARTHOST-01..08) and AWS SES
inbound via S3+SNS (REQ-HOOK-SES-01..07). Smart host covers the
"port-25 blocked by ISP / cloud" reality and the "relay through a
deliverability provider" pattern (SES, SendGrid, Mailgun, corp
SMTP). SES inbound covers the inverse: the operator runs SES as the
public-facing receiver and herold processes the mail via the
existing inbound-webhook contract, specialised to the SES + S3 + SNS
shape. NOT bit-compat with the SES API (the rev-1 "dropped SES
bit-compat" non-goal still holds -- herold *uses* SES, herold doesn't
*expose an SES-shaped API*).

*(Revised 2026-04-25 — JMAP for Calendars/Contacts in scope (phase 2); chat + 1:1 video calls in scope (phase 2); coterminous with the suite plan.)*

## Vision

A self-hostable, single-node communications server. The substrate beneath the herold suite (mail, calendar, contacts, chat) plus full SMTP MTA / IMAP for traditional mail clients. SMTP MTA + IMAP / JMAP mailbox + JMAP for Calendars + JMAP for Contacts + chat (DMs, Spaces, 1:1 video calls) + HTTP send API + receive webhooks + Sieve + LLM-based spam classification + LLM-based message categorisation, with a clean operator experience and a first-class plugin system. Sized for small-to-medium self-hosters, including power users with 1 TB+ mailboxes — **not** hosting providers, **not** enterprise.

Stalwart is the closest functional reference for the mail half. The chat / video-call half has no direct reference in the same product family — those are net-new herold scope, sized for the same single-node target.

Herold narrows the target in some dimensions (no multi-tenancy, no multi-node, no bundled rule-based spam) and widens it in others (HTTP send/receive APIs, large-mailbox support, plugin-first extensibility, JMAP-native calendar/contacts, integrated chat).

## Goals

- **G1. One binary, one system config file, one data directory.** System configuration static; everything else is runtime state in the DB, mutated via API/CLI.
- **G2. Protocol correctness.** SMTP, IMAP, JMAP, Sieve, DKIM/SPF/DMARC/ARC. Interop with Gmail, iCloud, Outlook, Thunderbird, Apple Mail, Fastmail JMAP clients is a release blocker.
- **G3. Storage choice.** SQLite (default, zero-dep) and PostgreSQL (for heavier deployments) both first-class. Filesystem blobs, embedded FTS. No S3, no Redis.
- **G4. Large mailbox support.** Individual mailboxes up to ≥1 TB. Design doesn't bottleneck at this size.
- **G5. LLM-first spam.** No rule engine, no Bayesian, no RBL/URIBL scoring. One classifier call per message via the spam plugin. Operator picks the endpoint.
- **G6. Plugin system on day one.** Out-of-process, JSON-RPC. Primary use cases: DNS providers (ACME DNS-01 + auto-publish DKIM/MTA-STS/TLSRPT/DMARC/DANE), spam classifier, directory adapter, delivery hooks.
- **G7. HTTP mail APIs.** A clean HTTP sending API (not SES-verbatim, SES-portable) and an incoming-mail webhook subsystem. Apps coded to AWS SES or similar should port with modest work.
- **G8. Identity you can federate.** Local identity with password + TOTP 2FA. Per-user association to external OIDC providers (Google, Microsoft, GitHub, corporate Okta, etc.). External identities may use different email addresses than local.
- **G9. Config split.** System config (hostname, listeners, data paths, admin-surface cert, run-as user, plugins, storage backend) is a file. Application config (domains, principals, aliases, Sieve, spam policy, DKIM keys, rate limits, webhooks) is runtime DB state.
- **G10. Honest defaults.** TLS on, ACME auto-managed, DKIM auto-signing on first domain add, LLM spam classification on by default (default endpoint: local Ollama), quotas on, download rate limits on, 2FA available.
- **G11. Full-text search that's actually useful.** Body + common attachment formats (PDF, Office, plain text). Large mailboxes searchable.
- **G12. Observable.** JSON logs + Prometheus metrics on every build. OTLP traces optional.
- **G13. No phone-home. No license gates. Ever.**

## Non-goals

- **NG1.** Hosting-provider / multi-tenancy features. No tenants, no per-tenant quotas, no per-tenant branding.
- **NG2.** Multi-node deployment. Single node only. Operators needing HA use hypervisor-level tricks (ZFS snapshot + failover, shared block storage). v1 does not grow into multi-node.
- **NG3.** CalDAV / CardDAV / WebDAV — out, ever. The DAV protocol family is not the substrate; operators wanting DAV run a separate service. **Updated 2026-04-25:** JMAP for Calendars (RFC 8984 + JMAP-Calendars binding) and JMAP for Contacts (RFC 9553 + JMAP-Contacts binding) are **in scope as phase-2 additions** of the herold + the suite, replacing the prior "out, but addable" framing. Both fit additively on the existing JMAP capability registry (`server/architecture/03-protocol-architecture.md` §Capability and account registration) and the entity-kind-agnostic state-change feed (`server/architecture/05-sync-and-state.md` §Forward-compatibility constraint) — no schema migrations of existing tables, no dispatch-core edits.
- **NG4.** Traditional spam filtering. No bundled rule engine. No Bayesian. No RBLs by default. (Operators who want these can write a plugin or run an external filter; we don't ship them.)
- **NG5.** Webmail. (The suite is a *separate* project — a JMAP web client that herold serves; herold itself hosts only the static bundle plus its API.)
- **NG6.** POP3 at launch.
- **NG7.** Exchange-compatible protocols (MAPI/EWS/ActiveSync).
- **NG8.** S3 blobs. Local filesystem only.
- **NG9.** Sharded / read-replica / clustered anything.
- **NG10.** Encryption at rest. Operators concerned about disk-level snooping run herold on encrypted volumes (LUKS/ZFS native/FileVault) — standard OS-level answer. We do not implement application-level encryption.
- **NG11.** Being a full OpenID Connect *issuer*. We authenticate users against local identity (password + TOTP) and federate to external OIDC providers; we do not issue OIDC tokens for third-party applications to consume.
- **NG12.** Bit-exact AWS SES API compatibility. We provide an HTTP send API and mail-arrival webhooks such that porting an SES-based app is tractable; we don't reproduce SigV4, SNS, or receipt rules verbatim.

## Simplification themes vs. Stalwart

| Theme | Stalwart | Herold |
|---|---|---|
| Scale | 10k+ mailboxes, enterprise at upper end | 1k mailboxes, 100 domains, 10k+10k msg/day, large mailboxes (≥1 TB each), up to ~10 TB per node, 1k concurrent sessions |
| Storage | 6 meta × 6 blob + composite | SQLite OR PostgreSQL (both first-class); filesystem blobs; Tantivy FTS |
| Spam | Rspamd-class engine + Bayesian + RBLs + LLM (enterprise) | LLM only via plugin (OpenAI-compat, local Ollama default). No rules, no Bayes, no RBLs. |
| Directory | Internal / SQL / LDAP / OIDC / memory | **Internal only** (password + TOTP) + **per-user external OIDC federation** (Google/MS/GitHub/corp — may use different email from local). No LDAP, no SQL-table directory. |
| Observability | Logs + events + metrics + traces + webhooks + SNMP + OTEL | JSON logs + Prometheus + OTLP (always available, no gate) |
| Admin UI | React SPA bundled | CLI + REST for v1; minimal web UI phase 2 |
| Config | Registry + TOML + hot reload + web edit | System config file (SIGHUP) + app state in DB (live via API/CLI/UI) |
| HTTP send API | Basic JMAP submission | **First-class HTTP send API** + SES-portable shape |
| Mail-arrival webhooks | via Sieve/delivery hook | **First-class webhooks** with easy body access (inline or signed fetch URL) |
| Calendar / Contacts | CalDAV / CardDAV (legacy DAV) + draft JMAP | **JMAP for Calendars + JMAP for Contacts (phase 2)**; no DAV |
| Chat / messaging | None | **Built-in chat (phase 2)**: DMs, Spaces, typing / presence / reactions, 1:1 video calls (WebRTC + self-hosted coturn) |
| LLM categorisation | Spam-only | **Spam + automatic message categorisation** (Gmail-style Primary/Social/Promotions/Updates/Forums by default; user-configurable prompt) |
| Multi-tenancy | Yes | No |
| Multi-node | In progress | Never |
| Plugin system | None | **Yes, v1.** Out-of-process JSON-RPC. DNS providers, spam, directory adapters, delivery hooks. |
| DNS automation | Record text to operator | Auto-published through DNS provider plugin (ACME DNS-01 + DKIM + MTA-STS + TLSRPT + DMARC + DANE) |
| Encryption at rest | Optional enterprise | **Not implemented.** Operators use volume-level (LUKS/ZFS/FileVault). |
| Download rate limiting | Partial | **Built-in** per-user / per-session on IMAP FETCH and JMAP/HTTP blob download. |
| FTS coverage | Headers + body | Headers + body + attachment text (PDF / Office / plain) |
| License | AGPL core + SEL enterprise | Single OSS license; no tier |

## Target scale (v1)

Per single node, provisioned hardware (8 vCPU, 32 GB RAM, NVMe):

- **Accounts:** up to ~1,000 active mailboxes.
- **Domains:** up to ~100 hosted domains.
- **Mail volume:** ~10,000 inbound + ~10,000 outbound messages / day (~15 msg/min peak).
- **Concurrent sessions:** up to ~1,000 combined IMAP/JMAP (predominantly IMAP IDLE).
- **Per-mailbox size:** up to ~1 TB individual mailboxes (power users, shared archive mailboxes).
- **Total storage:** up to ~10 TB per node (a handful of large mailboxes + typical average-sized).

At this scale SQLite handles a mixed workload but hits occasional contention when multiple large-mailbox clients do concurrent heavy writes. Operators with sustained high-concurrency writes pick PostgreSQL at install; both backends are first-class. See `server/architecture/02-storage-architecture.md`.

LLM classification at ~15 msg/min is trivially affordable (cloud 2 s call or local ~300 ms). Per-mailbox full-text indexing on a 1 TB mailbox initially is minutes-to-hours of indexing throughput; incremental indexing on new mail is sub-second.

## What success looks like (v1 ship gate)

1. Receive, store, and serve mail for ≥1 domain. Deliverability verified against Gmail, Outlook, iCloud.
2. Thunderbird, Apple Mail, Fastmail JMAP client all work. 1 TB mailbox searchable and browsable without hanging.
3. DKIM signs outbound; SPF/DKIM/DMARC/ARC verified on inbound.
4. **Adding a domain is one command.** `herold domain add example.com` — DKIM keys generated, DNS records auto-published via configured DNS plugin, MTA-STS + TLSRPT records up, certificates provisioned via ACME. No copy-paste, no operator-side DNS edit.
5. LLM spam classification against local Ollama out of the box; reconfigurable to any OpenAI-compat endpoint.
6. **HTTP send API**: app calls `POST /api/v1/mail/send` with JSON envelope + body → queued for delivery → observable via same queue inspection as SMTP submissions. One command to get an API key.
7. **Incoming webhooks**: operator registers a webhook for a domain or principal → new mail triggers POST with message metadata + body (inline for small, signed fetch URL for large). Webhook retries on 5xx.
8. **External OIDC federation** works: user links their local principal to Google/GitHub/etc. and can sign in either way. External email need not match local.
9. `systemctl start herold` on fresh VM with `/etc/herold/system.toml` → `herold bootstrap` → working server within 10 minutes (with DNS plugin).
10. `herold` CLI covers: bootstrap, principal/domain/alias CRUD, queue inspect/flush, log tail, cert status, plugin list/reload, spam policy, webhook CRUD, API key CRUD, OIDC provider CRUD, FTS rebuild.
11. Survives `kill -9`: no data loss for accepted mail, no corruption, no orphaned blobs.
12. At least one community DNS plugin (Cloudflare) shipped alongside v1.

Anything beyond this is phase 2+.

## Plugin scope (v1)

First-party plugins shipped alongside v1:

- **DNS providers** — at minimum: Cloudflare, Route53, manual/webhook generic. All other providers come from the community.
- **Spam classifier** — the default LLM adapter (OpenAI-compatible HTTP).

Plugin **contract** (process lifecycle, JSON-RPC schema, versioning) is a stable interface at v1. Breaking changes bump a major plugin-ABI version.

Plugin **catalogue** (installable plugins) is an ecosystem concern; we don't run a registry. Operators install plugins by dropping an executable into `plugins/` in the data dir and declaring it in system config.

Detail: see `server/requirements/11-plugins.md` and `server/architecture/07-plugin-architecture.md`.

## Config split summary

| Scope | Location | Mutation | Reload | Examples |
|---|---|---|---|---|
| **System** | `/etc/herold/system.toml` | Operator edits file | SIGHUP (process-level) | hostname, listeners, bind addrs, TLS for admin + JMAP, ACME account, data dir, run-as user, plugin declarations |
| **Application** | Inside the DB; editable via API/CLI/(later UI) | API calls | live (no SIGHUP) | domains, principals, aliases, Sieve scripts, DKIM keys, spam endpoint + prompts, queue policy, per-domain overrides |

Detail: see `server/requirements/09-operations.md` §Config.

This split gives us:
- A tiny, stable system file. Ansible/Nix/NixOS module owns it.
- Everything operators tune day-to-day stays out of config files. No SIGHUP for adding a user. No config drift between file and DB.

## Out of scope (so we don't relitigate)

- Tenants, tenancy, tenant quotas.
- Multi-node, clustering, replication.
- Rspamd-compatible rule engine.
- Bayesian token training.
- RBL/URIBL bundled.
- DAV groupware (CalDAV/CardDAV/WebDAV) as a herold protocol surface, ever. JMAP for Calendars / Contacts is in scope (phase 2); DAV is not.
- Web UI in phase 1.
- Webmail.
- SMIME/PGP at the server.
- POP3 at launch.
- LMTP ingress (delivery is in-process).
- SIEM / alerting engine (emit to Prometheus + logs).
- **Encryption at rest (any form).** Operators use volume-level encryption if needed.
- **OIDC issuer / full IdP.** We authenticate against local + external; we don't hand out tokens for third-party apps.
- **Bit-exact SES compatibility.** Our send API is portable from SES with modest rework, not drop-in.
- **S3 or other remote blob storage.**
- **LDAP.** No LDAP directory backend, no LDAP read-only bind-auth. Operators with LDAP environments provision principals via admin API / CLI or OIDC federation (many LDAP setups have an OIDC front anyway).
