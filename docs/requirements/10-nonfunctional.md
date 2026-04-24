# 10 — Non-functional requirements

Performance, scale, reliability, and security *properties* the system must exhibit. Cross-cutting; other docs reference these.

## Performance targets (single node, modest hardware: 4 vCPU, 16 GB RAM, NVMe)

*(Rescaled 2026-04-24 to match new scope: 1k mailboxes, 10k msg/day, 2 TB, 1k concurrent sessions.)*

These are design targets, not promises; we verify during phase testing (see `implementation/03-testing-strategy.md`).

| Dimension | Target |
|---|---|
| Inbound SMTP acceptance latency excluding spam LLM call (p50) | ≤ 50 ms |
| Inbound end-to-end including LLM classification (p50 / p95) | ≤ 600 ms / ≤ 2 s |
| IMAP FETCH flags/headers (1000 messages, p50) | ≤ 100 ms |
| IMAP IDLE notification latency after delivery | ≤ 1 s |
| JMAP Email/query simple filter (p50) | ≤ 50 ms |
| JMAP Email/query full-text search (p50) | ≤ 200 ms |
| Outbound delivery attempt throughput | ≥ 30 msg/s aggregate (3× peak load) |
| Concurrent IMAP + JMAP sessions | ≥ 1,000 |
| Startup time | ≤ 5 s cold, ≤ 2 s warm |
| System config reload | ≤ 1 s |
| Application config change (e.g. add user) | ≤ 100 ms |

- **REQ-NFR-01** The server MUST meet these targets on the reference hardware with a realistic corpus (see phase 1 benchmark harness).
- **REQ-NFR-02** No single request/connection/session may consume more than a bounded share of CPU and memory; configurable caps per protocol.
- **REQ-NFR-03** Under sustained overload, the server MUST degrade gracefully: reject early with 4xx (SMTP) or 429/503 (HTTP), never accept work it cannot complete.

## Resource limits

- **REQ-NFR-10** Per-connection memory cap: ≤ 16 MiB (configurable). Streaming parsers preferred over in-memory.
- **REQ-NFR-11** Maximum in-flight messages in RAM: bounded by a global concurrency semaphore, not unbounded task spawning.
- **REQ-NFR-12** File descriptor usage bounded; no FD leak allowed (enforced by test).

## Scalability

- **REQ-NFR-20** Vertical scaling: performance scales reasonably with cores up to ~8. At the 10k msg/day scale target, a 4 vCPU box is more than sufficient; we're not targeting workloads that would need more.
- **REQ-NFR-21** Horizontal scaling: **non-goal**. No multi-node, no replication, no read replicas. Single node forever.
- **REQ-NFR-22** Storage growth: supports up to 2 TB per node (bounded by filesystem and backup throughput). At 10k msg/day with ~100 KB average message size, 2 TB holds ≈5.5 years of mail.

## Reliability

### Crash safety

- **REQ-NFR-30** `kill -9` at any moment MUST NOT result in:
  - Corrupted metadata store.
  - Accepted message lost.
  - Orphaned blob with no recovery path.
- **REQ-NFR-31** Recovery on restart: bounded time (≤ 30 s for a 1M-message store).
- **REQ-NFR-32** An observed crash from production MUST be reproducible in a test (expectation of our bug process).

### Data durability

- **REQ-NFR-40** `fsync` discipline: metadata commits fsync'd; blob writes fsync'd before mailbox reference inserted.
- **REQ-NFR-41** No "fire and forget" writes on user-visible paths.
- **REQ-NFR-42** Background GC (blob refcount, orphaned queue entries) is idempotent and safe to interrupt.

### Availability (single-node)

- **REQ-NFR-50** Server survives: network link flaps, DNS outage (retry logic with bounded cache), storage read transient errors, ACME provider downtime (uses existing cert until expiry), external RBL timeouts.
- **REQ-NFR-51** Server MUST NOT exit on any recoverable error. Hard exits only for: OOM, disk full past fatal threshold, config parse failure at startup, data store corruption with no recovery option.
- **REQ-NFR-52** Zero-scheduled-downtime on config reload (REQ-OPS-10).

### Fault tolerance scope

- **REQ-NFR-60** Single disk failure is not handled in-server; use RAID/ZFS/filesystem redundancy.
- **REQ-NFR-61** Geographic redundancy / multi-region HA: out of scope. Operators handle via backup replication + DR plan, or wait for multi-node story in phase 3+.

## Security

### Attack surface principles

- **REQ-NFR-70** **Plugin system uses process isolation** as its security boundary (REQ-PLUG-40..44). No in-process dynamic code loading, no cdylib plugins, no scripting engine other than sandboxed Sieve.
- **REQ-NFR-71** Plugins run as a less-privileged UID/GID than the server and cannot access the server's DB or data dir directly (only via JSON-RPC).
- **REQ-NFR-72** No shell-out from server code paths. (Plugins are launched via `execve` with controlled args; that's the only process-creation path in the server.)
- **REQ-NFR-73** No unsafe parsers in message-handling hot paths; fuzz testing (REQ-TEST) of message/MIME/DKIM parsers required.
- **REQ-NFR-74** Dependencies minimized and audited. Licensing compatible with chosen OSS license. Dependency count tracked; adding new deps requires explicit review (phase decision).
- **REQ-NFR-75** LLM spam classifier receives only curated content (REQ-FILT-30/31); the server enforces the content boundary, not the plugin.

### Hardening

- **REQ-NFR-80** Binary MUST NOT require root to run. Installer binds privileged ports via `CAP_NET_BIND_SERVICE` or `systemd` socket activation.
- **REQ-NFR-81** Runs as an unprivileged user (`herold:herold`) by default.
- **REQ-NFR-82** No SUID/SGID on any binary.
- **REQ-NFR-83** Recommended systemd unit uses `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `NoNewPrivileges=yes`, `ReadWritePaths=/var/lib/herold /var/log/herold`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`, `SystemCallFilter=@system-service`.
- **REQ-NFR-84** Memory-safety: Go (GC-managed). No `unsafe.Pointer` in message parsing / crypto paths; any use of `unsafe` or `cgo` requires justification in review.

### Input validation

- **REQ-NFR-90** All network-facing parsers (SMTP, IMAP, JMAP, DAV, DKIM/DMARC/ARC, Sieve) MUST treat input as hostile. No untrusted input into format strings, no allocation on unbounded lengths, no recursion on untrusted depth.
- **REQ-NFR-91** MIME parser depth limit (default 20), total parts limit (default 1000), per-part size bounded by message size.
- **REQ-NFR-92** Header parser: max header size (default 64 KiB), max number of headers (default 1000), max line length per RFC (998 octets).

### Secrets and crypto

- **REQ-NFR-100** Passwords hashed with Argon2id (REQ-AUTH-20).
- **REQ-NFR-101** DKIM and ACME private keys stored 0600, never logged, rotatable.
- **REQ-NFR-102** Session tokens: JWT signed with Ed25519 or HS256 with 256-bit secret. Default rotation every 30 days for signing keys.
- **REQ-NFR-103** CSRF protection on admin UI (double-submit cookie or origin check).
- **REQ-NFR-104** TLS 1.2+ only (REQ-PROTO-70).

### Denial-of-service

- **REQ-NFR-110** Rate limits per-IP and per-account on all auth paths (REQ-PROTO-13, REQ-AUTH-23).
- **REQ-NFR-111** Zip-bomb protection on message attachments: decompress with size limits.
- **REQ-NFR-112** Regex engine MUST be linear-time (no catastrophic backtracking). Use a regex library with RE2-style guarantees. User-supplied regexes (Sieve) and author-supplied rule regexes both protected.
- **REQ-NFR-113** No recursive alias/forward cycles (REQ-FLOW-100).

### Privacy

- **REQ-NFR-120** No telemetry phone-home. Ever. Not even anonymous "server count". This is a design value, not just a default.
- **REQ-NFR-121** DMARC ruf (failure reports) not sent by default — they leak user data.
- **REQ-NFR-122** Admin can read any message (inherent in a mail server); this MUST be audit-logged (REQ-ADM-300) and visible to users (via audit log exposure in self-service panel — phase 3).

### Supply chain

- **REQ-NFR-130** Reproducible builds SHOULD be achievable.
- **REQ-NFR-131** Release artifacts signed (Sigstore/cosign + PGP).
- **REQ-NFR-132** SBOM published per release.

## Portability

- **REQ-NFR-140** Linux amd64 and arm64 are first-class **production** targets; tested in CI per PR.
- **REQ-NFR-141** macOS arm64 (Apple Silicon) is a first-class **development** target and is tested in CI per PR. macOS is the primary developer platform; catching "works on Linux, broken on mac" drift at PR time is a hard requirement. Not a production target (we don't ship or support mail servers on macOS in production). macOS amd64 is best-effort.
- **REQ-NFR-142** Windows: development-only, no CI coverage, no production support.
- **REQ-NFR-143** FreeBSD, OpenBSD, illumos: community-supported if contributed; not in CI.

## Observability (non-functional view)

- **REQ-NFR-150** Every user-visible behavior MUST be introspectable via: a log line, a metric, or an admin API response. No "black box" operations.
- **REQ-NFR-151** Any production issue a user reports SHOULD be diagnosable from: logs + metrics + `diag collect` bundle. If not, open a gap ticket.

## Documentation (as NFR)

- **REQ-NFR-160** Operator documentation: installation, bootstrap, DNS setup, backup, restore, upgrade, troubleshooting — published alongside every release.
- **REQ-NFR-161** Architecture documentation maintained in-tree (these files evolve with the code).
- **REQ-NFR-162** Every CLI command and every REST endpoint has a one-line description and, where non-trivial, an example.

## Non-functional that we are NOT promising in v1

- 99.99% availability (single-node design).
- Sub-100ms cross-protocol p99 under all loads.
- Byzantine fault tolerance.
- Compliance certifications (SOC2, HIPAA, PCI-DSS) — operator's responsibility.
