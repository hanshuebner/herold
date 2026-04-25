# Open questions

Only live questions — everything resolved moved to the **Resolved log** at the bottom. Ordered by urgency.

## Active — pre-release admin

### Q2. Governance model

BDFL / maintainer group / foundation. Not urgent; revisit when external contributors appear.

### Q3. Development forum / chat

Matrix / Discord / GitHub Discussions / Zulip. Not urgent; revisit before first public release.

### Q4. Release signing keys

We sign release artifacts (REQ-NFR-131). Open: who holds the key, how rotated, hardware-backed (YubiKey), backup. Resolve before first signed release.

---

## Resolved log

Kept for traceability. Entries are the form in which they were resolved.

### R40. StateChange row shape → entity-kind-agnostic *(prior Q5)*

Reshaped `internal/store/types.go` StateChange and the `state_changes` SQL
schema on both backends to (EntityKind string, EntityID, ParentEntityID,
Op). Forward-compat constraint from architecture/05 is now load-bearing
in code; future JMAP datatypes are purely additive.

### R39. Project name → Herold *(prior Q1)*

German for "herald." 6 letters. GitHub-checked against German/Nordic candidates and English/German acronyms; only one 1⭐ personal repo shares the name. Candidate pool that was considered: Bote, Kranich, Pforte, Brunnen, Postreiter, Feldpost, Steg, Norn, Holm, Thegn, Havet, Sendir, Landbote, Bode, Frod. Rejected (real conflicts): Ferd, Rune, Heim, Brev, Horn, Fylgja, Bragi, Reise, Aegir, Ratatosk, Skald, Edda.

### R34. License → MIT *(prior Q1)*

Permissive, widely understood in the Go ecosystem. Copyleft dropped earlier; BSD-3-Clause was working decision; finalized to MIT.

### R35. Web UI framework → HTMX + Go templates + Alpine.js / vanilla JS *(prior Q2)*

No SPA framework. Client-side validators in vanilla JS or Alpine. Autocompletion via HTMX's `hx-get` + server-rendered dropdowns. No build pipeline. Total JS load < 30 KB expected.

### R36. Large-mailbox FTS rebuild budget → deferred to phase-2 perf tests *(prior Q4)*

Benchmark during phase 2 hardening on a synthetic 1 TB corpus. Document operational expectations.

### R37. Bleve at 300 GB index scale → deferred to phase-2 perf tests *(prior Q5)*

Validate on synthetic large corpus before phase-2.5 exit. Fallback: Postgres GIN FTS when Postgres backend chosen; for SQLite backend, revisit.

### R38. GDPR / right-to-erase workflow → policy-documented *(prior Q9)*

Admin-initiated delete is final. Self-service delete not implemented. Log retention defaults: logs 30 d, audit 365 d (audit stores principal ID not email). Policy-documented; not code-enforced at v1.

### R1. Language → Go *(prior Q2)*

Chosen for compile-time. Rust's runtime advantages not needed at 100 msg/s peak. Writing our own libraries for standard protocols neutralizes Rust's crate-ecosystem edge.

### R2. Plugin system → yes, in v1

Reversed the earlier "no plugins in v1" stance. Out-of-process JSON-RPC. First types: DNS, spam, events, directory, delivery hooks.

### R3. Multi-node → never

Non-goal NG2. No phase-4. Operators needing HA use hypervisor-level tricks.

### R4. Spam engine → LLM only

No rule engine, no Bayesian, no RBLs bundled. Classifier is a plugin; default is OpenAI-compat HTTP against local Ollama.

### R5. Encryption at rest → not implemented

Considered SQLCipher + envelope-encrypted blobs + custom Bleve Directory; rejected. Operators use volume-level encryption (LUKS / ZFS / FileVault).

### R6. Identity → local + per-user external OIDC federation; not an OIDC issuer

Relying party only. Users link external IdPs on a per-user basis; external email need not match local.

### R7. SES → portable, not verbatim

Our own HTTP send API + webhooks. Documented SES-porting guide. Not SigV4, not SES receipt-rule DSL, not SNS-verbatim.

### R8. Storage metadata backend → SQLite + Postgres, both first-class

Chosen at install. Goose for migrations. Postgres 15+ floor. Migration tool SQLite↔Postgres supported.

### R9. License family → permissive, BSD-style *(prior Q1)*

Copyleft dropped. Exact variant pending (see active Q1).

### R10. Phase-0 spikes → run in parallel *(prior Q2)*

SQLite driver, MIME parser, IMAP library (fork vs write), FTS commit cadence — all spiked in parallel with a 2-week timebox.

### R11. MIME parser origin policy → loose *(prior Q3)*

Permissively-licensed generic libraries from any author acceptable. Moot under Go anyway.

### R12. Web UI → phase 2, minimal scope *(prior Q4/Q5)*

Dashboard + principal CRUD + password/2FA/app-passwords + domain/alias CRUD + queue monitor + email research (sent/bounced when). Not a full SPA.

### R13. Groupware → dropped entirely *(prior Q5/Q6)*

No CalDAV, no CardDAV, no WebDAV. Not a phase-3 candidate.

### R14. Shared mailboxes + IMAP ACL → phase 2 *(prior Q7)*

In v1.0. Not deferred.

### R15. POP3 → dropped entirely *(prior Q8)*

Not phase-3-candidate.

### R16. Binary size target → none *(prior Q10)*

Go binary naturally lands 30–60 MB. Acceptable.

### R17. Migration lib → goose; PG floor → 15 *(prior Q9)*

Bootstrap flag picks backend at install. Can't be changed live.

### R18. Bleve commit defaults *(prior Q10/Q11)*

Commit every 1 s or 100 docs, whichever first. Tunable. Benchmarks in phase 0 set final defaults.

### R19. IMAP idle memory *(prior Q21)*

Goroutine + session state ~12-16 KB per idle session. 1k sessions ~15 MB. Fine. Validate in phase 2 load test.

### R20. Fuzz findings policy *(prior Q22)*

Pre-v1.0: all fuzz-found crashes/panics fixed. Memory-usage-degradation tracked separately. Monthly review post-v1.

### R21. DNSSEC resolver → operator-provided *(prior Q13)*

DANE requires DNSSEC. We require operator to run a validating resolver (unbound / systemd-resolved). Absent → MTA-STS-only mode.

### R22. First-party DNS plugins *(prior Q14)*

v1: Cloudflare, Route53, Hetzner Cloud DNS, manual. acme-dns generic in phase 3. Others community.

### R23. First-party event-publisher plugins *(prior Q15)*

NATS only. Others community, no promises.

### R24. Alias expansion privacy *(prior Q16)*

Sender sees real address. Sieve redirect always rewrites envelope sender (DMARC preservation). Users wanting hidden group membership: custom delivery-hook plugin.

### R25. DMARC ruf / TLS-RPT defaults *(prior Q17)*

ruf off by default (privacy). TLS-RPT emit on by default (aggregated, not sensitive). DMARC rua: ingested but not sent from us (per protocol).

### R26. Default-on RBL list *(prior Q23)*

Moot under LLM-only. Operators adding rule-based filtering via plugin configure their own list.

### R27. HTTP send API auth *(prior Q26)*

API keys only (scoped per principal or service-account principal). No session-token reuse.

### R28. Webhook secret storage *(prior Q27)*

Stored in DB plaintext (HMAC keys not hashable without losing verification). Protected by OS-level filesystem permissions. Documented.

### R29. Postgres connection pool default *(prior Q28)*

Default 25; configurable. Documented interaction with PG `max_connections`.

### R30. Download rate-limit state persistence *(prior Q29)*

In-process only; resets on restart. Not a practical abuse surface (requires admin to trigger restart).

### R31. Bleve indexing worker count default *(prior Q30)*

`GOMAXPROCS - 1`, cap 8.

### R32. TOTP secret column naming *(prior Q31)*

Column named `totp_secret` (no misleading `_enc` suffix; not encrypted at rest).

### R33. Event delivery semantics *(prior Q32)*

At-most-once from server to plugin. NATS JetStream gives at-least-once from plugin onward if operator uses it.
