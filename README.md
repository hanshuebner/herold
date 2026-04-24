# Herold — design baseline

A greenfield mail server project. Stalwart-adjacent scope (SMTP MTA + IMAP/JMAP + Sieve + DKIM/SPF/DMARC/ARC) with deliberate divergences: Go instead of Rust, SQLite/Postgres instead of six backends, LLM-only spam, first-class plugins (DNS, spam, events, directory, delivery hooks), HTTP send API + incoming webhooks, per-user external OIDC federation, single-node only. Source of truth while we iterate.

## How to read this

1. **[docs/00-scope.md](docs/00-scope.md)** — goals, non-goals, simplification themes. Read this first.
2. **docs/requirements/** — what the system must do, grouped by area. Numbered requirements (`REQ-XXX-nn`) so we can reference them in discussion.
3. **docs/architecture/** — how the system is shaped. Decisions, not code.
4. **docs/implementation/** — language/runtime choices, phasing, testing, deliberate cuts.
5. **docs/notes/** — reference material and unresolved questions.

## Latest scope revision

**2026-04-24** (rev 2): license → MIT (permissive, final); groupware dropped entirely; shared mailboxes + IMAP ACL in phase 2 (pre-1.0); minimal web UI in phase 2 (HTMX + Go templates + Alpine.js / vanilla JS for client-side validators and autocompletion); Hetzner Cloud DNS added to first-party DNS plugins; no binary-size target.

**2026-04-24** (rev 1): rescaled to 1k mailboxes / 100 domains / 10k+10k msg/day / 1 TB+ individual mailboxes / ~10 TB per node / 100 msg/s peak; switched language from Rust to Go; promoted Postgres to first-class alongside SQLite; LLM spam default via plugin (no rule engine, no Bayes, no RBLs); plugins first-class in v1 (DNS + spam + events + directory + delivery); added HTTP send API + incoming webhooks + typed event publication; added per-user external OIDC federation; split system config (file) from application config (DB); dropped multi-node; dropped encryption at rest; dropped SES bit-compat and OIDC-issuer role.

## Defaults in force

Working assumptions. Override by editing `docs/00-scope.md`; affected docs will be revised.

- Language: **Go** (goroutines, stdlib-first, small dependency tree). Compile-time was the decisive factor.
- Scope: **email-first**. Groupware (CalDAV/CardDAV/WebDAV) dropped entirely.
- Topology: **single node, period.** No multi-node path in v1 or beyond.
- Scale target: **1k mailboxes, 100 domains, 10k+10k msg/day, 100 msg/s peak, large mailboxes up to 1 TB+, ~10 TB per node, 1k concurrent IMAP/JMAP.**
- Storage: **SQLite or PostgreSQL** (both first-class, chosen at install). Filesystem blobs. Bleve FTS.
- Encryption at rest: **not implemented.** Operators run on encrypted volumes (LUKS/ZFS/FileVault).
- Spam: **LLM classification only** via the spam plugin (default: OpenAI-compatible HTTP, pointed at local Ollama).
- Identity: **local (password + TOTP) + per-user external OIDC federation.** We are a relying party only, not an OIDC issuer.
- HTTP mail APIs: **first-class.** Send API + incoming webhooks with inline or signed-fetch-URL body access. SES-portable, not SES-verbatim.
- Plugins: **first-class in v1.** Out-of-process, JSON-RPC. Types: DNS providers, spam classifier, event publishers, directory adapters, delivery hooks.
- Events: **first-class.** Server emits typed events; plugins publish to NATS (default, shipped) / Kafka / SQS / etc.
- Config: **split.** System config (file, SIGHUP) + application config (DB, live API/CLI).
- Admin surface: **CLI + REST + minimal Web UI** (user mgmt / domain+alias / queue monitor / email research). UI lands in phase 2.
- Shared mailboxes + IMAP ACL: **yes, phase 2.**
- License: **MIT**.

## Directory

```
herold/
├── README.md                           this file
├── CLAUDE.md                           working agreement for Claude Code agents
├── STANDARDS.md                        global coding and development standards
├── AGENTS.md                           specialist agent partitioning + delegation guide
├── LICENSE                             MIT
├── go.mod                              Go module: github.com/hanshuebner/herold
├── Makefile                            build, test, lint, fuzz-short, ci-local, docker
├── .github/workflows/                  CI: ci.yml, nightly.yml, release.yml
├── .claude/agents/                     specialist subagent definitions
├── .pre-commit-config.yaml             pre-commit hooks (gofmt, goimports, vet, staticcheck, gitleaks)
├── cmd/herold/                         single binary entrypoint (server + CLI merged)
├── internal/                           non-plugin code; not importable externally
│   ├── store, storesqlite, storepg     metadata store interface + backends
│   ├── storeblobfs, storefts           blob store (FS, content-addressed) + Bleve FTS
│   ├── protosmtp, protoimap, protojmap wire protocol servers
│   ├── protomanagesieve, protoadmin    ManageSieve + admin REST
│   ├── protosend, protowebhook         HTTP send API + incoming mail webhooks
│   ├── protoevents                     typed event dispatcher → event-publisher plugins
│   ├── directory, directoryoidc        internal directory + per-user external OIDC (RP)
│   ├── mailparse                       RFC 5322 / MIME parser
│   ├── maildkim, mailspf, maildmarc    DKIM / SPF / DMARC
│   ├── mailarc                         ARC
│   ├── sieve, spam                     Sieve interpreter + sandbox, spam classifier shim
│   ├── queue, tls, acme, autodns       outbound queue, TLS load, ACME, auto-DNS publish
│   ├── plugin                          plugin supervisor + JSON-RPC 2.0 stdio client
│   ├── observe, clock                  slog + Prometheus + OTLP; clock / rand injection
│   ├── sysconfig, appconfig            TOML system config + DB-backed app config
│   └── admin, testharness              cobra CLI + in-process test harness
├── plugins/                            first-party plugins, each its own main package
│   ├── sdk                             plugin Go SDK (JSON-RPC 2.0 on stdio)
│   ├── herold-dns-cloudflare/route53/  ACME DNS-01 + record publisher plugins
│   │   hetzner/manual
│   ├── herold-spam-llm                 OpenAI-compatible HTTP spam classifier
│   ├── herold-events-nats              default event publisher (NATS)
│   └── herold-echo                     SDK demo used in the plugin test suite
├── test/interop, test/e2e              cross-package scenarios + conformance wiring
├── deploy/docker, deploy/debian,       packaging
│   deploy/rpm, deploy/k8s
└── docs/
    ├── 00-scope.md                     vision / non-goals / simplification themes
    ├── requirements/
    │   ├── 01-protocols.md             SMTP, IMAP, JMAP, POP3, ManageSieve, Sieve
    │   ├── 02-identity-and-auth.md     directory, SASL, OAuth/OIDC, 2FA, permissions
    │   ├── 03-mail-flow.md             ingress → queue → delivery → DSN
    │   ├── 04-email-security.md        DKIM, SPF, DMARC, ARC, TLS-RPT, MTA-STS, DANE
    │   ├── 05-storage.md               mailbox data model, blob store, FTS
    │   ├── 06-filtering.md             spam (LLM plugin) + Sieve execution
    │   ├── 08-admin-and-management.md  REST API, CLI, web UI
    │   ├── 09-operations.md            TLS/ACME, observability, config split, backup
    │   ├── 10-nonfunctional.md         perf, scale, reliability, security
    │   ├── 11-plugins.md               plugin contract: DNS, spam, events, directory, delivery hooks
    │   ├── 12-http-mail-api.md         HTTP send API + incoming-mail webhooks
    │   └── 13-events.md                event publication (NATS default, Kafka/SQS/etc. via plugins)
    ├── architecture/
    │   ├── 01-system-overview.md
    │   ├── 02-storage-architecture.md
    │   ├── 03-protocol-architecture.md
    │   ├── 04-queue-and-delivery.md
    │   ├── 05-sync-and-state.md
    │   ├── 06-topology-and-clustering.md
    │   └── 07-plugin-architecture.md   how plugins run: process model, JSON-RPC, sandboxing
    ├── implementation/
    │   ├── 01-tech-stack.md
    │   ├── 02-phasing.md
    │   ├── 03-testing-strategy.md
    │   └── 04-simplifications-and-cuts.md
    └── notes/
        ├── stalwart-feature-map.md     reference inventory from the Stalwart codebase
        └── open-questions.md           to resolve before / while building
```

## Requirement ID convention

- `REQ-PROTO-nn` — protocols
- `REQ-AUTH-nn` — identity and auth
- `REQ-FLOW-nn` — mail flow
- `REQ-SEC-nn`  — email security
- `REQ-STORE-nn` — storage
- `REQ-FILT-nn` — filtering
- `REQ-ADM-nn`  — admin/management
- `REQ-OPS-nn`  — operations
- `REQ-NFR-nn`  — nonfunctional
- `REQ-PLUG-nn` — plugins
- `REQ-SEND-nn` — HTTP send API
- `REQ-HOOK-nn` — incoming webhooks
- `REQ-EVT-nn`  — events

When cutting or adding, reference by ID.

## Status

Baseline. Not reviewed. Not frozen. Expect edits.
