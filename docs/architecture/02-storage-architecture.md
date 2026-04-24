# 02 — Storage architecture

*(Revised 2026-04-24: SQLite and Postgres both first-class; Bleve FTS in Go; large-mailbox design notes; no encryption.)*

Three logical stores, two metadata backends. Backend decisions live here; requirements in `requirements/05-storage.md`.

## Three stores

| Store | Purpose | Access pattern | Backends |
|---|---|---|---|
| **Metadata** | Principals, mailboxes, messages-in-mailbox, queue, aliases, audit log, Sieve scripts, DKIM keys, ACME state, rate-limit state, webhooks, event-plugin configs, OIDC provider configs | Random, transactional, hot | SQLite (default) or PostgreSQL |
| **Blob** (bodies) | Message bodies (content-addressed) | Append-heavy, streamed reads | Local filesystem (only) |
| **FTS** (full-text) | Inverted index on headers/bodies/attachment text | Random read, background write | Bleve (embedded, Go-native) |

The three are separately addressable but logically coherent: a message row in the metadata store points to a blob ID (hash) and an FTS document ID. No single database holds all three; the coordination layer does.

(Earlier drafts called the metadata store "KV." That term is retired — it's a SQL-backed structured repository, not a key-value store.)

## Why separate them

Different access patterns, different durability needs:

- The **metadata store** is the hot database. Transactions, low latency, random access. Fits in RAM for most workloads.
- Blobs are large-but-immutable. Streaming I/O. Dedup by content hash. fsync'd on write, then never modified.
- FTS is derived and rebuildable. Can lag. Can be rebuilt from scratch on disaster.

Stalwart puts all three behind one store abstraction with pluggable backends (even storing blobs inside Postgres). We don't — the abstractions are cleaner when each store is optimized for its access pattern.

## Metadata store — dual backend

Both backends implement the same internal `store.Metadata` interface (a typed repository: `GetPrincipal`, `InsertMessage`, `UpdateMailboxModseq`, …). CI runs the full test suite against both. Operators pick at install; not runtime-switchable.

### SQLite backend (default)

- Driver: **`modernc.org/sqlite`** (CGO-free pure Go).
- Mode: WAL, `synchronous=NORMAL`, `busy_timeout=30000`, `foreign_keys=ON`.
- Single writer; many readers. At 100 msg/s peak, fine; at sustained concurrent large-mailbox writes from multiple clients, occasional contention spikes (~100 ms) — documented.
- Backup: `VACUUM INTO` for snapshot; `.backup` works too.
- Operators can `sqlite3 store.db` for forensics.

### PostgreSQL backend

- Driver: **`github.com/jackc/pgx/v5`** with a bounded connection pool.
- Minimum version: Postgres 15 (for some JSON features, performance improvements).
- Schema identical to SQLite (same DDL modulo type differences: `BLOB` → `BYTEA`, `INTEGER PRIMARY KEY` → `BIGINT GENERATED ALWAYS AS IDENTITY`, etc.). Migrations carry both variants side-by-side.
- Transactions short (milliseconds). Prepared statements for hot paths.
- Advisory locks for singleton tasks (if/when we have any — queue scheduler doesn't need it at single-node).
- Backup: `pg_dump`. No incremental; full each time.

### When each is the right pick

| Scenario | SQLite | Postgres |
|---|---|---|
| Homelab, ≤100 mailboxes | ✔ | overkill |
| 1k mailboxes, mixed load | ✔ | ✔ |
| Heavy concurrent writes (1k HTTP-send API clients simultaneously) | contention visible | ✔ |
| 1 TB mailbox with many FETCH+STORE clients | usable | smoother |
| "Just works" install | ✔ | needs Postgres instance |
| Compliance / DR tooling built around Postgres | ✗ | ✔ |

### Schema shape

- `principals(id, name, kind, canonical_email, password_hash, totp_secret_enc, quota_bytes, disabled, created_at, updated_at, ...)`
- `aliases(email_lower, principal_id, created_at)`
- `groups_members(group_id, member_principal_id)`
- `oidc_providers(name, issuer_url, client_id, client_secret_ref, scopes_json, auto_provision, created_at)`
- `principal_oidc_links(principal_id, provider_name, subject, email_at_provider, linked_at)`
- `domains(domain, owner_principal_id, dkim_current_selector, dns_plugin_name, ...)`
- `dkim_keys(domain, selector, algorithm, private_key, public_key, created_at, expires_at, active)`
- `mailboxes(id, principal_id, parent_id, name, role, subscribed, uidvalidity, highest_uid, highest_modseq, created_at, updated_at)`
- `messages(id, mailbox_id, blob_id, uid, modseq, internal_date, received_date, size_bytes, flags_bitmap, flag_keywords_json, thread_id, header_cache_json)`
- `threads(id, root_message_id, subject_hash, participant_hash, updated_modseq)`
- `blob_refs(blob_id, ref_count, last_ref_change)`
- `queue(id, direction, state, next_attempt_at, attempts, sender, rcpt, blob_id, tags_json, config_set, ...)`
- `audit_log(id, ts, actor, action, resource, outcome, details_json)`
- `api_keys(id, principal_id, hashed_token, scopes, allowed_from_addresses_json, allowed_from_domains_json, name, created_at, last_used_at)`
- `sieve_scripts(principal_id, name, content, active, created_at)`
- `state_changes(id, principal_id, change_type, entity_id, seq, created_at)`
- `acme_orders(hostname, status, url, expires_at, ...)`, `acme_accounts(provider, account_key, uri)`
- `dmarc_reports_aggregate(…)`, `tls_rpt_reports(…)`
- `webhooks(id, name, principal_id, target_kind, target_value, url, secret, body_mode, filter_json, active)`
- `webhook_deliveries(id, webhook_id, message_id, status, attempts, last_attempt_at, last_status_code, last_response_snippet)`
- `event_subscriptions(plugin_name, event_types_json, tag_filter_json, active)`
- `config_sets(name, signing_domain, headers_json, retry_schedule_json, event_filter_json)`
- `fetch_signed_urls(token, message_id, expires_at, used_count)` — for webhook body-fetch signed URLs

Indices created explicitly. Every query has a matching index. Schema kept small and stable; evolution through numbered migrations in both SQLite and Postgres variants.

### Transactions

- Every state change that affects multiple tables runs in one transaction.
- Common transaction: "deliver message to mailbox" — insert into `messages`, bump `mailboxes.highest_uid`/`highest_modseq`, insert into `state_changes`, update `blob_refs`.
- Transactions short (milliseconds). No long-held locks.

### State-change feed

JMAP and IMAP IDLE need to notify clients of changes. Design: every mutation that produces a JMAP state change or affects an IMAP selected mailbox appends to `state_changes`. A single-process broadcaster reads the feed tail and fans out to subscribed sessions. `state_changes` is retained for a bounded window (e.g. 24h) so reconnecting clients can catch up with `CONDSTORE` / JMAP `since` state.

See `architecture/05-sync-and-state.md` for full detail.

## Blob store

### Filesystem default layout

```
data/blobs/
  ab/cd/abcd1234...ef      # file named by lowercase hex hash of canonical message
  ab/cd/abcd1234...ef.meta # optional metadata sidecar (size, content-type hint)
```

Two levels of hex fan-out (256 × 256) — keeps directory listings small on ext4/XFS.

### Content addressing

- Hash algorithm: **BLAKE3** (fast, collision-resistant, 32-byte output hex-encoded).
- Canonicalization before hashing: normalize CRLF line endings (RFC 5322 says CRLF; we canonicalize). Trim no data. Any bit that survives delivery is in the hash.
- Dedup: identical inbound message to N recipients = one blob, N `messages` rows.

### Writes

1. Accept into temp file `data/blobs/tmp/<uuid>`.
2. Stream-hash while writing.
3. `fsync` the temp file.
4. `rename` to content-addressed path (atomic on POSIX).
5. Insert `messages` row + increment `blob_refs.ref_count`.
6. Commit transaction.

If steps 1–4 succeed but the transaction fails, the blob is orphaned. GC cleans up orphans older than grace period (REQ-STORE-12).

### Reads

- Direct open-read by hash. No in-memory copy required.
- Range reads for IMAP partial fetch / JMAP download.

### Refcount and GC

- `blob_refs` table tracks ref count per blob.
- Ref count incremented on insert into `messages`, decremented on delete.
- Blobs with refcount 0 older than grace window (24h default) are deleted from filesystem.
- GC runs on scheduler every hour; batch-limited to avoid I/O spikes.
- A blob with refcount > 0 is never deleted; a blob with no refcount row at all is considered orphaned (safety net).

### Alternate backends

**Not planned.** The scope is single-node, and the local filesystem backend handles the scale target (2 TB) indefinitely. No S3, no RDBMS-for-blobs, no alternatives.

## FTS

### Choice

**Bleve** (`github.com/blevesearch/bleve/v2`) — Go-native full-text search. Lucene-analogue. Less performant than Tantivy but pure Go, mature, well-documented.

### Attachment text extraction

Attached content indexed for common formats (REQ-STORE-60):
- **PDF**: text layer via `github.com/ledongthuc/pdf` or `rsc.io/pdf`. No OCR.
- **DOCX/XLSX/PPTX**: unzip + parse OOXML XML; our own helper (small).
- **Plain text / CSV / Markdown / HTML**: stdlib + `golang.org/x/net/html`.
- **Archives (zip, tar)**: unpacked recursively (bounded depth). Non-archive files inside indexed.

Extraction bounded: per-attachment max text size (default 5 MB) + per-message max total extracted text (default 20 MB). Exceeding: silently truncated with a counter.

Extraction runs in the async indexing worker, not in the delivery hot path.

### Document shape

One FTS document per `messages` row:

```
{
  doc_id: <messages.id>,
  mailbox: <mailbox.id>,
  principal: <principal.id>,
  from, to, cc, bcc: <tokenized>,
  subject: <tokenized>,
  body: <tokenized, from text/plain + html-to-text>,
  attachment_names: <tokenized>,
  date: <date>,
  flags: <faceted>,
}
```

### Write path

- On message delivery: enqueue an indexing job (async).
- Indexer worker consumes the job, reads the blob, parses MIME, tokenizes, writes to Tantivy.
- Commits periodically (time-based or doc-count-based).

Indexing is async — search can lag. Default ceiling 5s. IMAP `SEARCH` and JMAP `Email/query` may return "pending index" signal when results are known-incomplete (implementation detail).

### Language handling

- Operator selects the primary tokenizer language in config.
- For CJK: UTF-8 bigram tokenizer (pragmatic; language-specific stemmers are too heavy for v1).
- Per-field tokenizer override possible.

### Index lifecycle

- Periodic merge (Bleve handles).
- Full rebuild via `herold diag fts rebuild` if corrupted or schema changed.
- Rebuild is online — writes go to new index, queries served from old until swap.

## Coordination: how the three stores stay consistent

Principle: **the metadata store is the source of truth.** Blobs and FTS are derived or referenced.

Consequences:

- Loss of FTS → rebuild from metadata + blobs. No user-visible data loss.
- Loss of a blob with a metadata reference → user sees "message unavailable" for that specific message. Backup restore fixes.
- Loss of metadata → catastrophic. That's why the metadata store has the strongest durability guarantees (fsync, WAL, backups).

Delivery atomicity: blob written (fsync'd) before the metadata transaction commits. If the metadata commit fails after the blob write, the blob is orphaned and GC'd. If the metadata commit succeeds and the blob write failed, we never reach here (write-blob-first ordering). Crash between blob write and metadata commit: orphan blob, no user-visible inconsistency.

## Caching

In-process caches only. Redis is not used.

- **Directory cache** — principal lookups by name/email. TTL 30s, invalidated on mutation.
- **DKIM public key cache** — keyed by `(domain, selector)`, TTL from DNS.
- **MTA-STS policy cache** — keyed by domain, TTL from policy.
- **Header parse cache** — parsed header struct per `messages.id` for hot mailboxes; LRU-bounded.
- **FTS query result cache** — not in v1.

All caches have explicit size caps; none grow unbounded.

## Encryption at rest — NOT implemented

Deliberate non-goal (NG10). Operators concerned with disk-level threats use volume-level encryption (LUKS / ZFS native encryption / FileVault / similar). This moves the threat model to OS + filesystem, where it belongs.

Implications:
- Blobs on disk are readable by anything with filesystem read access.
- SQLite/Postgres files are readable by anything with DB-host filesystem access.
- Bleve index files are readable by anything with filesystem access.

Standard Unix permissions (0600 for key files, 0700 for data dir, run-as unprivileged `herold` user) provide the same-host user-separation barrier. That's the only in-process protection.

## Multi-node: not now, not later

Non-goal. We explicitly do not design the storage layer to be trivially multi-node-able. The storage assumes single-writer, single-reader semantics and leans on that for simplicity (SQLite transactions, in-process state-change broadcast, in-process caches with no coherence problem). This is a choice — a different project that wants multi-node will make different choices from the ground up.
