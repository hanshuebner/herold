# Herold — agent partitioning and delegation guide

This repository uses specialist subagents (under `.claude/agents/`) to parallelise implementation of subsystems that each conform to distinct protocols, RFCs, or architectural concerns. The root agent reads this file to decide who to delegate to.

## Delegation rules

1. **A task crossing one subsystem's boundary is delegated to that subsystem's implementor.** Do not implement SMTP parsing from the root agent; delegate to `smtp-implementor`.
2. **A task spanning two subsystems is coordinated from the root agent**, which delegates parallel work to the two implementors and merges the result. Only the root agent writes integration glue.
3. **Every substantive PR gets a `reviewer` pass before merge.** Security-sensitive PRs also get `security-reviewer`. Wire-parser PRs also get `conformance-fuzz-engineer`.
4. **Scaffolding / CI / Docker / release tooling is `release-ci-engineer`'s surface.**
5. **Agents read `STANDARDS.md` as authoritative.** Agents do not override project-wide rules with local preferences.

## Implementor roster

Each owns an area of the code tree. Ownership is where design and first-pass review authority sit; it does not fence off contributions from other agents.

| Agent | Owns | Primary RFCs / specs |
|---|---|---|
| `smtp-implementor` | `internal/protosmtp` (inbound + outbound state machines, SASL) | 5321, 5322, 3207, 1870, 2920, 6152, 6531, 3030, 3461, 4954, 2034, 8689 |
| `imap-implementor` | `internal/protoimap` | 9051, 3501, 2177, 4315, 4731, 5256, 5258, 5819, 6154, 5161, 6855, 7888, 7162, 6851, 3691, 2342, 9208, 3516, 4469, 3502, 4978, 2971, 5464, 5465, 8474 |
| `jmap-implementor` | `internal/protojmap` | 8620, 8621, 8887 (optional) |
| `sieve-implementor` | `internal/sieve`, `internal/protomanagesieve` | 5228, 5804, 5232, 5173, 5230, 5231, 5233, 3894, 6609, 5229, 5260, 5490, 9042, 5293, 7352, 6131, 6134, 5703, 5235, 5429, 5435 |
| `mail-auth-implementor` | `internal/maildkim`, `internal/mailspf`, `internal/maildmarc`, `internal/mailarc`, TLS-RPT / MTA-STS / DANE wiring | 6376, 7208, 7489, 8617, 8460, 8461, 8462, 7672 |
| `storage-implementor` | `internal/store`, `storesqlite`, `storepg`, `storeblobfs`, `storefts` | n/a (own schema; Bleve; SQLite; Postgres) |
| `queue-delivery-implementor` | `internal/queue`, outbound SMTP client, `internal/acme`, `internal/autodns` | RFC 8555 (ACME), 5321 outbound, MX/TLSA resolution |
| `directory-auth-implementor` | `internal/directory`, `internal/directoryoidc`, password + TOTP + SASL mechanisms | 6238 (TOTP), 5802 (SCRAM), 7628 (OAUTHBEARER), 6749 + OIDC Core (RP only) |
| `plugin-platform-implementor` | `internal/plugin`, plugin SDK, first-party plugins under `plugins/` | JSON-RPC 2.0, own ABI |
| `http-api-implementor` | `internal/protoadmin`, `internal/protosend`, `internal/protowebhook`, `internal/protoevents` | own REST shapes; HMAC for webhooks; NATS for default event publisher |
| `ops-observability-implementor` | `internal/sysconfig`, `internal/appconfig`, `internal/observe`, `internal/tls` (loader), `internal/admin` CLI glue, boot / reload / shutdown | n/a |

## Cross-cutting roster

| Agent | Mandate |
|---|---|
| `reviewer` | Style, structure, coverage, cross-subsystem consistency. Authority to block merge on any `STANDARDS.md` rule violation. |
| `security-reviewer` | Crypto, auth, session, input validation on wire surfaces, privilege drops, secret handling. Authority to block merge on security-sensitive paths. |
| `conformance-fuzz-engineer` | Wire-protocol conformance suites (imaptest, scripted SMTP, Pigeonhole, DKIM/DMARC/ARC vectors); fuzz target coverage; deterministic test harness. |
| `release-ci-engineer` | GitHub Actions workflows, pre-commit hooks, Dockerfiles, packaging (`.deb`, `.rpm`, K8s manifests), reproducible-build toolchain, SBOM. |
| `docs-writer` | Operator manual, admin reference, plugin SDK docs, migration guides. Active from Phase 2.5 onward. |

## Phase-to-agent map

Pulls directly from `docs/design/implementation/02-phasing.md`.

**Phase 0 — Foundations.** `release-ci-engineer` (scaffolding + CI), `storage-implementor` (store interface + SQLite + Postgres), `plugin-platform-implementor` (SDK + JSON-RPC + echo plugin), `ops-observability-implementor` (sysconfig/appconfig split + slog + Prometheus + OTLP wiring).

**Phase 1 — Inbound email works.** `smtp-implementor` (relay-in), `mail-auth-implementor` (verify-side), `sieve-implementor`, `imap-implementor` (baseline), `directory-auth-implementor` (internal + OIDC RP), `plugin-platform-implementor` (first-party LLM spam plugin), `storage-implementor` (FTS worker), `ops-observability-implementor` (`protoadmin` scaffolding + CLI).

**Phase 2 — Outbound + ACME + auto-DNS + JMAP + HTTP APIs + events + shared mailboxes + web UI.** `queue-delivery-implementor` (queue + outbound + ACME + autodns), `mail-auth-implementor` (signing + MTA-STS + DANE + DMARC reports + TLS-RPT), `plugin-platform-implementor` (Cloudflare + Route53 + Hetzner + manual DNS plugins + NATS event publisher), `jmap-implementor`, `imap-implementor` (CONDSTORE/QRESYNC + MOVE + ACL), `http-api-implementor` (send + webhooks + events dispatcher), `ops-observability-implementor` (web UI scaffolding, SQLite↔Postgres migration tool).

**Phase 2.5 — Hardening.** `conformance-fuzz-engineer` leads; `reviewer` + `security-reviewer` close the remaining items; `docs-writer` writes operator + plugin-SDK docs; `release-ci-engineer` ships packaging and SBOM pipeline.

## Cross-subsystem coordination points (where the root agent must merge)

These are the seams where two implementors must agree on a contract. The root agent is responsible for writing the interface and routing the two sides to the correct owners.

- **`store` interface ↔ every protocol handler.** `storage-implementor` owns the interface; each `proto*` implementor consumes it.
- **State-change feed ↔ IMAP IDLE / JMAP push.** Durable per-principal monotonic seq, defined by `storage-implementor`, consumed by `imap-implementor` and `jmap-implementor`.
- **Plugin SDK ↔ every plugin-calling subsystem.** `plugin-platform-implementor` owns the SDK and the JSON-RPC surface; `spam`, `autodns`, `acme` (DNS-01), `directory`, `protoevents`, `protowebhook` call into it.
- **Mail-auth results ↔ Sieve `spamtestplus` + DMARC/ARC actions.** `mail-auth-implementor` produces the typed `AuthResults`; `sieve-implementor` consumes them.
- **Outbound SMTP ↔ DKIM signing.** `queue-delivery-implementor` emits messages; `mail-auth-implementor` signs. The seam is a pure function (message in, message + signature headers out).
- **Admin REST ↔ every subsystem's mutable state.** `http-api-implementor` exposes; each implementor provides a typed service layer with audit logging integrated.

## How an agent claims a task

1. Read the open task list with `TaskList`.
2. Pick a pending, unowned task in your surface area. Prefer lowest ID.
3. Set `owner` to your agent name and status to `in_progress` via `TaskUpdate`.
4. When blocked, open a sub-task describing the blocker and link it via `addBlockedBy`.
5. When done, run the full local CI (`make ci-local`), mark the task `completed`, and hand off to `reviewer`.
