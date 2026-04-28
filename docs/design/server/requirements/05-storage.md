# 05 — Storage

*(Revised 2026-04-24: SQLite and Postgres both first-class; large-mailbox support (≥1 TB); FTS includes attachment text; download rate limits; no encryption at rest.)*

What data we persist, durability guarantees, backends supported. Architecture detail in `architecture/02-storage-architecture.md`; this doc is the *what*.

## Data categories

| Category | Volume | Write rate | Read pattern | Durability |
|---|---|---|---|---|
| Message bodies (blobs) | up to 10 TB | bursty | streamed, random | fsync, crash-safe |
| Mailbox metadata | up to ~50 GB | moderate | random, latency-sensitive | transactional |
| Principals, domains, aliases | MB | low | random | transactional |
| Sieve scripts, identities, app passwords | MB | low | random | transactional |
| Outbound queue | MB–GB | bursty | FIFO + targeted | transactional, crash-safe |
| FTS index | 10–30% of bodies | async | random | rebuildable |
| DKIM keys, ACME certs, API keys, OIDC provider configs | KB | very low | random | transactional |
| Audit log | MB/day | moderate | append, rare read | append-only, retention-bounded |
| DMARC / TLSRPT reports | MB | low | rare | retention-bounded |
| Webhook subscriptions, event-plugin configs | KB | very low | random | transactional |
| Download rate-limit state | KB (per principal) | hot | random | not crash-safe (in-memory OK) |

## Backends — both first-class

- **REQ-STORE-01** Default deployment MUST work with **zero external database**: SQLite for metadata, filesystem for blobs, Bleve for FTS, all within the server's data directory.
- **REQ-STORE-02** **PostgreSQL** MUST be an equal, supported option for metadata, chosen at install time. Operators pick based on their write concurrency needs.
- **REQ-STORE-03** Blob store: local filesystem only. No S3, no object-store backend.
- **REQ-STORE-04** FTS: Bleve (embedded), same regardless of metadata backend choice.
- **REQ-STORE-05** Switching backends post-install requires export + import (supported, documented, but not live).

Picking between SQLite and Postgres — rough guidance (not a requirement):

| Consideration | SQLite | Postgres |
|---|---|---|
| Zero-dep install | ✔ | ✗ |
| 1k mailboxes, modest load | ✔ | ✔ (overkill) |
| Heavy concurrent writers (SES-style ingest + many clients) | contention spikes | ✔ |
| 1 TB+ mailbox with many concurrent FETCHes and STOREs | usable | smoother |
| Operational surface | one file | one more service |
| Forensic inspection | `sqlite3` CLI | `psql` |

Both supported in CI; same test suite runs against both.

## Large mailboxes (≥1 TB per mailbox)

- **REQ-STORE-06** A single principal MAY have up to 1 TB+ of mailbox content without storage-layer degradation that affects other principals.
- **REQ-STORE-07** FETCH ranges / partial reads on large mailboxes MUST stream — no requirement for the server to hold a full mailbox's messages in memory.
- **REQ-STORE-08** Expanding a large mailbox index (e.g. full rebuild after an FTS schema change) MUST be **online** — the mailbox remains usable, new mail continues to index; the rebuild runs as a background worker.
- **REQ-STORE-09** Mailbox-level locks kept short: per-mutation transactions; no long-held table locks.

## Message bodies

- **REQ-STORE-10** Messages stored as **content-addressed** blobs: blob ID = hash of the canonicalized raw RFC 5322 message.
- **REQ-STORE-11** Dedup: one blob, multiple mailbox references on fanout.
- **REQ-STORE-12** Blob lifecycle: reference-counted. Deleted when refcount → 0 AND grace period elapsed (24h default).
- **REQ-STORE-13** Blob writes atomic: write temp, fsync, rename.
- **REQ-STORE-14** Blob reads streamable; no requirement to hold in RAM.
- **REQ-STORE-15** Blobs MAY be compressed at rest (zstd, off by default).
- **REQ-STORE-16** **No application-level encryption at rest.** Operators run on encrypted volumes (LUKS / ZFS native / FileVault) if they need disk-level protection.

Blob hash: **BLAKE3**, 32-byte output hex-encoded. 2-level (256×256) hex fan-out directory layout (`blobs/ab/cd/abcd...`).

## Download rate limiting

New in v1 scope. Prevents exfiltration and heavy-handed client behavior.

- **REQ-STORE-20** Per-principal and per-session bandwidth limits on outbound data from the server to clients.
- **REQ-STORE-21** Applies to: IMAP `FETCH BODY[...]`, JMAP `Email/get` with body, JMAP download endpoint, webhook `fetch_url` (REQ-HOOK-30), admin API message-body fetch.
- **REQ-STORE-22** Defaults: 500 MB/hour per principal, 100 MB/session. Configurable per principal in application config (e.g. higher for power users with legitimate bulk-download needs).
- **REQ-STORE-23** On limit exceeded: IMAP returns `NO [LIMIT]` on the next command (custom code); JMAP returns 429 with `Retry-After`; webhook fetch returns 429.
- **REQ-STORE-24** Admin override: per-principal flag `ignore_download_limits`. Audit-logged when set.
- **REQ-STORE-25** Rate-limit state is in-process (not durable). After server restart, limits reset. Documented as intentional — a 429 avoidance via restart-abuse is impractical.

## Mailbox metadata

- **REQ-STORE-30** Per-principal mailboxes, with name, role, UIDVALIDITY, highest UID, highest MODSEQ, subscription state, flags.
- **REQ-STORE-31** Per-message metadata (mailbox-independent): blob ID, JMAP id, threadId, internal-date, received-date, size, parsed header cache. Per-(message, mailbox) metadata: UID, MODSEQ, flags. See REQ-STORE-36.
- **REQ-STORE-32** MODSEQ strictly increasing per mailbox. JMAP state tracks per-type + per-account.
- **REQ-STORE-33** Delete semantics: `\Deleted` + `EXPUNGE` or JMAP `Email/set destroy`. Blob refcount decremented; blob eventually GC'd. JMAP `destroy` removes the message from every mailbox it is in (REQ-STORE-36); IMAP `EXPUNGE` only removes the per-(message, mailbox) row for the selected mailbox — when other mailbox memberships remain, the message stays visible there. Final blob deref happens when the last per-(message, mailbox) row is removed.
- **REQ-STORE-34** Mailbox row carries an optional `color` column (string, hex like `#5B8DEE`, NULL when unset). Read/write through JMAP per REQ-PROTO-56. Not advertised to IMAP clients (no IMAP keyword for mailbox colour).
- **REQ-STORE-35** Identity row carries an optional `signature` column (plain-text body, NULL when unset; HTML signature deferred to phase 2 as a separate column). Read/write through JMAP per REQ-PROTO-57. Used by clients to populate compose; not embedded automatically by the server in outgoing messages — the client decides whether and how to insert it.

### Multi-mailbox membership

- **REQ-STORE-36** A single `Email` (per JMAP RFC 8621 §1.6.1) MAY be present in **multiple mailboxes simultaneously** within the same account. The model is M:N: one logical message (one blob, one threadId, one JMAP id) referenced from N mailboxes via N per-(message, mailbox) rows. JMAP `mailboxIds` is the set of mailbox IDs whose per-(message, mailbox) row points at this message. Required for spec-conformant `Email/get`, `Email/set update mailboxIds`, `Email/set destroy`, and `Mailbox/set onDestroyRemoveEmails` (RFC 8621 §4.6, §2.5).
- **REQ-STORE-37** IMAP UIDs remain **per-mailbox** (RFC 9051 §2.3.1.1). A message in N mailboxes carries N independent UIDs — one per per-(message, mailbox) row, allocated against that mailbox's `highest_uid`. JMAP `Email/copy` (within an account) and JMAP `Email/set update mailboxIds` allocate new per-(message, mailbox) rows with new UIDs in the target mailboxes; the source rows remain untouched until the client explicitly removes them. `MODSEQ` is also per-(message, mailbox); a flag change in one mailbox does not bump the row in another.
- **REQ-STORE-38** Migration plan from the current 1:1 model: introduce a `message_mailboxes(message_id, mailbox_id, uid, modseq, flags_bitmap, flag_keywords_json, internal_date)` join table; move `uid`, `modseq`, and the flag columns out of `messages`; backfill one row per existing `messages.mailbox_id`; drop `messages.mailbox_id`. Migration runs as a single offline upgrade (REQ-OPS-46) with an integrity check that asserts row counts match before drop. Both SQLite and Postgres carry the migration in lock-step.
- **REQ-STORE-39** **Deferred to next round (post 2026-04-28).** The four JMAP test-suite failures this unblocks are listed in `test/interop/README.md` "Known deferred failures" — `email/get-mailbox-ids`, `email/set-update-add-mailbox`, `email/set-update-remove-mailbox`, `email/set-destroy-removes-from-all-mailboxes`. Until REQ-STORE-36..38 land, herold reports a single mailboxId per Email and silently ignores attempts to add a second.

## Threading

- **REQ-STORE-40** Messages threaded per RFC 5256 (REFERENCES algorithm).
- **REQ-STORE-41** `threadId` computed at delivery time, stored.

## Quotas

- **REQ-STORE-50** Per-principal quota: total bytes + overhead.
- **REQ-STORE-51** Enforced on: SMTP RCPT (`4.2.2` defer or `5.2.2` reject, per principal policy), IMAP APPEND, JMAP Email/import, HTTP send API.
- **REQ-STORE-52** Quota recomputed nightly to correct drift.
- **REQ-STORE-53** No per-folder or per-tenant quotas.
- **REQ-STORE-54** Quotas expressed in GiB (compile-time-readable defaults: `100 GiB` free tier, `1 TiB` for power users). Operators override.

## Full-text search

- **REQ-STORE-60** FTS index MUST cover:
  - Headers: `From`, `To`, `Cc`, `Bcc`, `Subject`, `Reply-To`.
  - Body text (from `text/plain` + html-to-text of `text/html`).
  - **Attachment filenames AND extracted text** for common formats:
    - PDF (text layer, not OCR).
    - Office: DOCX, XLSX, PPTX (OOXML XML extraction).
    - Plain text, CSV, Markdown, HTML (unpacked recursively if archives).
- **REQ-STORE-61** Extractable attachments with size > 25 MB skipped (log + metric). Configurable.
- **REQ-STORE-62** OCR of images: not in v1. (Phase 3+.)
- **REQ-STORE-63** Encrypted / password-protected attachments: indexed by filename only.
- **REQ-STORE-64** Language-aware tokenization for: English (default), operator-configurable primary, UTF-8 fallback for CJK.
- **REQ-STORE-65** Index MUST be rebuildable from primary storage.
- **REQ-STORE-66** Index updates asynchronous; bounded lag (default 5 s new-mail-to-searchable). JMAP `Email/query` may return `searchPending` hint when lag > 0.
- **REQ-STORE-67** Large mailbox FTS: first indexing of an imported 1 TB mailbox takes minutes–hours depending on hardware. Bounded by indexing-worker concurrency (default 2 workers; configurable up to CPU count − 1).

## Backup and restore

- **REQ-STORE-70** `herold diag backup <path>` produces a consistent point-in-time backup (tar.zst).
- **REQ-STORE-71** Contents: SQLite/Postgres snapshot (app config + mailbox metadata + queue), blob tree, ACME state, audit log, webhook and plugin config. System config referenced by path (not copied, avoids secret leakage); `--include-system-config` override.
- **REQ-STORE-72** Restore: `herold diag restore <path>` offline.
- **REQ-STORE-73** Incremental backups of blobs trivial (content-addressed, append-only). Metadata snapshot full each time.
- **REQ-STORE-74** For Postgres: backup uses `pg_dump` by default (stable, portable). Logical backup; restore across Postgres versions works.
- **REQ-STORE-75** Remote backup destinations (S3, rsync, rclone) out of v1; operator scripts the upload.

## Data migration

- **REQ-STORE-80** Maildir and mbox importers (REQ-STORE-70 was "OLD" — renumbered).
- **REQ-STORE-81** IMAP-source importer (phase 2+): read from another IMAP server, write to local.
- **REQ-STORE-82** Maildir exporter per-principal (phase 2+).
- **REQ-STORE-83** **SQLite ↔ Postgres migration tool**: export from one, import to the other. Offline. Supported and tested.

## Retention and deletion

- **REQ-STORE-90** Deleted messages moved to `Trash` by default. Trash auto-purges after 30 days configurable.
- **REQ-STORE-91** Admin-delete of a principal: final. Backup before.
- **REQ-STORE-92** Audit log retention default 365 days.
- **REQ-STORE-93** Event-emit audit entries (REQ-EVT-*) are in the audit log (not a separate log).

## Consistency and durability

- **REQ-STORE-100** Accepted messages survive `kill -9`. fsync before ack.
- **REQ-STORE-101** Metadata updates transactional: flag changes, deletions, UID/MODSEQ bumps atomic per mutation.
- **REQ-STORE-102** Crash recovery: replay WAL (SQLite) or recover from Postgres — both handled by underlying engines; our code tolerates either.
- **REQ-STORE-103** Orphaned-blob scan on startup; reschedule GC for discovered orphans.

## Integrity

- **REQ-STORE-110** `herold diag fsck` verifies: every blob referenced exists; every referenced blob hash matches content; quota accounting consistent; MODSEQ monotonic per mailbox; no dangling thread IDs. Runs online.
- **REQ-STORE-111** FTS integrity separate: `herold diag fts verify` cross-references FTS doc IDs against `messages` table.

## Out of scope

- S3 or any remote blob backend.
- Object-store blob sharding.
- Read replicas (Postgres or otherwise).
- Cold-storage tiering.
- Encryption at rest in any form.
- MySQL, MariaDB, etc. as metadata backend.
- Redis, Memcached.
