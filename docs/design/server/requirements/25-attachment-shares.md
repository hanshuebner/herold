# 25 — Attachment shares (v1)

*(Added 2026-06-07.)*

Large or scanner-hostile attachments (ZIP, executables, disk images, anything a recipient gateway is likely to strip or quarantine) are awkward to send as MIME parts. This feature lets a sender lift such a file out of the message, park it in herold's blob store, and replace it in the body with a **capability URL** the recipient fetches over HTTPS. The file becomes a *share*: an owner-scoped, TTL-bounded, content-addressed object with its own lifecycle, independent of any mailbox.

Web-side counterpart: `../../web/requirements/17-attachments.md` § "Large or unsafe attachments: offload to a share link" (REQ-ATT-60..73) for the compose surface — offer-to-offload, link insertion, the "Shared links" strip, the management view. Architecture: `../architecture/11-attachment-shares.md`.

## Scope and rationale

- The recipient is an external, unauthenticated party. There is no account to authenticate against, so access is authorized by **possession of an unguessable URL** (a bearer capability), exactly like a "anyone with the link" share. Optional per-share password adds a second factor.
- The bytes already pass through the blob store on upload (the composer uploads every attachment to the JMAP upload endpoint to obtain a `blobId`). A share does not re-upload — it **promotes** an existing blob into a persistent, refcounted share object. One upload, no copy.
- This intentionally bypasses the recipient's MIME-level malware scanning — that is the user's explicit choice and the entire point. The mitigations are attribution (the sender is an authenticated principal), per-principal quota, expiry, and owner/admin revocation. See `### Abuse and safety`.
- v1 stores the file in plaintext (the server can read it). End-to-end encryption with the key in the URL fragment is an explicit non-goal for v1 (`### Out of scope`).

## The share object

- **REQ-SHARE-01** A share is created from a blob the principal has already uploaded. Creating a share installs a reference that protects the blob from GC (REQ-STORE-12) for the share's lifetime; destroying or expiring the share releases the reference, after which the blob is reclaimable if no other reference remains.
- **REQ-SHARE-02** A share carries: `id` (the capability token, see REQ-SHARE-10), `principal_id` (owner), `blob` (hash + size into the content-addressed store), `filename`, `content_type`, `created_at`, `expires_at`, `retention` (the per-share lifetime chosen at create, clamped to `max_ttl`; REQ-SHARE-41), optional `max_downloads`, `download_count`, optional `password_hash`, and `state` (REQ-SHARE-20). The owner's display of a share also exposes `download_count` and `last_downloaded_at`.
- **REQ-SHARE-03** A share is **immutable in its payload**: `blob`, `filename`, `content_type`, and `size` are fixed at creation. Mutable fields are limited to `state` (pending → active, or → revoked), `expires_at` (owner may shorten, never extend past the configured maximum), and `max_downloads` (owner may lower). To replace the file the owner destroys the share and creates a new one (new URL).
- **REQ-SHARE-04** A share **records a back-reference to the message it was shared from**, so the owner can later answer "why, and with whom, did I share this file?" The back-reference is a context snapshot captured at confirmation (REQ-SHARE-20), when the message is final: `source_message_id` (the JMAP `Email` id / store message id of the sent message), `source_subject` (subject snapshot), and `source_recipients` (the envelope recipient addresses — To, Cc, Bcc — snapshot). The fields are a **snapshot**, not a live join: they survive deletion of the source message so the owner retains the context, while `source_message_id` lets the management view deep-link to the message when it still exists. All three are NULL for a share that is still `pending` (not yet confirmed). The recipient-facing surfaces (the capability URL, the landing page, the download) MUST NOT expose the back-reference — it is owner-only metadata.

## Storage

- **REQ-SHARE-10** Schema additions (forward-only, SQLite + Postgres parity):
  - `file_shares(id, principal_id, blob_hash, blob_size, filename, content_type, created_at_us, expires_at_us, retention_us, max_downloads, download_count, password_hash, state, last_downloaded_at_us, revoked_at_us)`:
    - `id` TEXT primary key — the capability token, URL-safe, generated from a CSPRNG with ≥128 bits of entropy (REQ-SHARE-11b). It is the only secret protecting the file; it is never derived from the blob hash, the principal, or a counter.
    - `principal_id` FK to `principals.id`, ON DELETE CASCADE — destroying a principal destroys their shares (and releases the blob references).
    - `blob_hash` TEXT NOT NULL, `blob_size` INTEGER NOT NULL — the `BlobRef` into the content-addressed store (REQ-STORE-10). Multiple shares MAY point at the same blob (dedup is preserved).
    - `filename` TEXT NOT NULL, `content_type` TEXT NOT NULL — what the recipient sees and is served. Stored verbatim; `content_type` is what the server emits on download, never re-sniffed into an executable type.
    - `expires_at_us` INTEGER NOT NULL — absolute expiry. There is no "never expires" value in v1.
    - `retention_us` INTEGER NOT NULL, default 0 — the per-share lifetime chosen at create (REQ-SHARE-41), clamped to `max_ttl`. Zero means unset: the `pending` → `active` transition falls back to `default_ttl` (REQ-SHARE-20).
    - `max_downloads` INTEGER NULL — when non-null, the share is exhausted once `download_count >= max_downloads`.
    - `password_hash` TEXT NULL — Argon2id PHC string (reuse `internal/directory` hashing). NULL means no password.
    - `state` TEXT NOT NULL, one of `pending`, `active`, `revoked` (REQ-SHARE-20).
    - `source_message_id` TEXT NULL, `source_subject` TEXT NULL, `source_recipients` TEXT NULL (JSON array of addresses) — the message back-reference (REQ-SHARE-04), populated at confirmation. NULL while pending. `source_message_id` is a soft reference (no FK): the snapshot context outlives deletion of the message, and the management view treats a now-missing message as a dead deep-link rather than an error.
  - Index on `(principal_id, created_at_us)` for the owner's management list; index on `(state, expires_at_us)` for the sweeper.
- **REQ-SHARE-11a** Public share routes (REQ-SHARE-30) MUST NOT perform account or session authentication. Authorization is established solely by possession of the share's capability URL, plus the optional password (REQ-SHARE-11c).
- **REQ-SHARE-11b** The `id` MUST be generated from a CSPRNG with ≥128 bits of entropy and MUST be treated as a secret. IDs MUST NOT be sequential, monotonic, or otherwise enumerable.
- **REQ-SHARE-11c** A share MAY require a password. When set, the bytes MUST NOT be served until the supplied password verifies against `password_hash` (Argon2id, constant-time outcome). A missing or wrong password yields the same response as a missing share (REQ-SHARE-11e) — no oracle distinguishes "exists but locked" from "does not exist".
- **REQ-SHARE-11d** Because capability URLs leak through `Referer` headers, proxy and access logs, and mail forwarding, download responses MUST be HTTPS-only and MUST set `Referrer-Policy: no-referrer` and `Cache-Control: private, no-store`. The full `id` MUST NOT be written to access logs; logs record a truncated id or its hash.
- **REQ-SHARE-11e** Expired, revoked, download-exhausted, `pending`, and never-existed shares MUST all return an identical `410 Gone` with no distinguishing body or timing signal. Only an `active`, unexpired, unexhausted, correctly-authorized share serves bytes.
- **REQ-SHARE-12** Per-principal hard caps: at most **1000 active+pending shares** per principal, and a per-principal **share storage quota** (REQ-SHARE-50). Creation past either cap fails with `forbidden { too_many_shares }` or `over_quota`.

## Lifecycle and states

- **REQ-SHARE-20** A share is created in state `pending` with `pending_ttl` (default 48h, matched to `default_ttl` so a still-pending share shows the same lifetime it will keep once sent). Every create carries a `retention` — the lifetime chosen at create (REQ-SHARE-41), clamped to `max_ttl` — stored on the row alongside `pending_ttl`. `default_ttl` is the default lifetime the create argument offers when the caller supplies none; it is not applied at confirmation once a `retention` is on record. The composer confirms the share to `active` only after the carrying message is successfully submitted. This two-phase create means a share whose draft is abandoned — or whose client crashes before send — expires on its own within `pending_ttl` and never becomes a durable orphan; the composer also destroys pending shares on discard (REQ-SHARE-21), so the longer window only matters for an abnormal exit.
  - `pending` → `active`: on send confirmation. `expires_at` is set from the share's stored `retention` (falling back to `default_ttl` when no `retention` was recorded), clamped to `max_ttl`. The lifetime the share carries once active is the lifetime chosen at create, not a value reimposed at confirmation.
  - `active` → `revoked`: owner or admin revocation (REQ-SHARE-22). Sets `revoked_at`.
  - Terminal access conditions (expiry, exhaustion, revocation) are evaluated at fetch time (REQ-SHARE-11e); they are not separate stored states beyond `revoked`.
- **REQ-SHARE-21** Confirmation is idempotent. Confirming an already-`active` share is a no-op success; confirming a `revoked` or non-existent share fails. The composer destroys (`FileShare/set destroy`) any `pending` shares belonging to a discarded draft; the `pending_ttl` sweeper is the backstop.
- **REQ-SHARE-22** Revocation is immediate: the next fetch returns 410. Revocation does not delete the row at once (so the owner's management view can show "revoked"); the sweeper removes revoked rows after `revoked_grace` (default 24 h), releasing the blob reference.
- **REQ-SHARE-23** A background sweeper MUST delete: `pending` shares older than `pending_ttl`, `active` shares past `expires_at`, and `revoked` shares past `revoked_grace`. Deletion releases the blob reference; the blob is then reclaimable by the existing blob GC (REQ-STORE-12, REQ-STORE-103). The sweeper runs on the same cadence as other metadata sweepers.

## Download surface

- **REQ-SHARE-30** Two public, unauthenticated routes on the public listener (`../architecture/03-protocol-architecture.md`):
  - `GET /share/{id}` — a minimal landing page: filename, human size, expiry, download count remaining (if capped), a password field when the share is locked, and a Download button. No principal identity, no other share, and no server internals are exposed.
  - `GET /share/{id}/download` — streams the bytes with `Content-Type` = the stored `content_type`, `Content-Length` = `blob_size`, `Content-Disposition: attachment; filename="..."` (RFC 5987 encoded), and the headers of REQ-SHARE-11d. On success, `download_count` is incremented and `last_downloaded_at` is set atomically; the increment MUST be committed before the body finishes streaming so a concurrent fetch cannot exceed `max_downloads`.
  - When a share is password-protected, `/share/{id}/download` requires the verified password (carried as a POST form field or a short-lived signed cookie minted by the landing page after a correct password); a bare GET without it returns 410 per REQ-SHARE-11e.
- **REQ-SHARE-31** Streaming MUST honour HTTP range requests (`Accept-Ranges: bytes`) so large shares resume; a range request counts as one download for the purpose of `download_count` only on the first range of a transfer (range continuations of the same client transfer do not re-increment). HEAD is supported and never increments the counter.
- **REQ-SHARE-32** Public-download rate limiting, independent of the owner-side download limits (REQ-STORE-20): per-share and per-source-IP bandwidth and request-rate caps (defaults: 50 requests / 10 min per IP per share; global per-share bandwidth ceiling configurable). On exceed: `429` with `Retry-After`. State is in-process (REQ-STORE-25 posture). This blunts a leaked URL being used as a bulk-distribution origin.

## Wire (JMAP)

- **REQ-SHARE-40** A new JMAP object type `FileShare` mirrors the storage row (minus `password_hash`, which is write-only). Methods: `FileShare/get`, `FileShare/set` (create + update + destroy), `FileShare/changes`, `FileShare/query` (owner's own shares, filterable by `state`, sortable by `createdAt`). Capability `https://netzhansa.com/jmap/file-shares` advertises availability; absent capability means the suite hides the offload affordance entirely.
- **REQ-SHARE-41** `FileShare/set create` arguments: `blobId` (an existing uploaded blob in the same account), `name`, `type`, `expiresIn` (seconds, clamped to `max_ttl`), optional `maxDownloads`, optional `password`. The server resolves `blobId` to a `BlobRef`, installs the reference, clamps `expiresIn` to `max_ttl`, and persists it as the share's `retention` (REQ-SHARE-02, REQ-SHARE-20) rather than discarding it once clamped. The returned object includes the absolute `url` (`{public_base_url}/share/{id}`), `expiresAt`, and `expiresIn` reporting the (possibly clamped) retention — never the originally requested value when the two differ. Every `FileShare` object, on every method that returns one (`get`, `set`, `query`, `changes`), reports `expiresIn`: for a `pending` share this is the retention it will receive on confirmation, not a value derived from its pending-state `expiresAt`. The blob MUST belong to the requesting account; a `blobId` the account cannot read is rejected with `forbidden`.
- **REQ-SHARE-42** `FileShare/set update` accepts only `state` (the value `active`, i.e. confirm — REQ-SHARE-20; or `revoked`, i.e. revoke — REQ-SHARE-22), a shortened `expiresAt`, or a lowered `maxDownloads`. Any attempt to mutate `blobId`, `name`, `type`, or to extend expiry past `max_ttl` is rejected with `invalidProperties`. The confirm update (`state: "active"`) MAY additionally carry the message back-reference (REQ-SHARE-04): `sourceMessageId`, `sourceSubject`, `sourceRecipients` (array of addresses). These are accepted only on the confirm transition and are otherwise immutable; the `FileShare` object exposes them read-only and never on the recipient-facing surfaces.
- **REQ-SHARE-43** `password` and `password_hash` are never returned on the wire. A share reports a boolean `hasPassword`. There is no "read the password back" path.
- **REQ-SHARE-44** State-change events: `FileShare/set` create/update/destroy advances `FileShare/changes`; a public download does NOT push a per-fetch event (the per-download cost would dwarf its value), but `download_count` is reconciled into the owner's view on the next `FileShare/get` (the management view polls on open; live per-download push is out of scope for v1).
- **REQ-SHARE-45** The `https://netzhansa.com/jmap/file-shares` session capability descriptor advertises `default_ttl_seconds` and `max_ttl_seconds`, sourced from the deployment's `default_ttl` and `max_ttl` (`[server.attachment_shares]`, sysconfig). The composer's expiry picker offers presets and a default lifetime drawn from these values rather than a client-side constant, so an operator's `max_ttl` override reaches the picker and a preset above it is not offered. The descriptor MAY also advertise `quota_max_bytes` when a deployment-wide `share_quota_per_principal` is configured; it does not carry per-principal runtime state (a principal's current usage) — that is not expressible in a per-server capability descriptor.

## Configuration knobs

```toml
[server.attachment_shares]
# Master switch. When false the JMAP capability is not advertised and the
# /share routes 404; the composer's offload affordance disappears.
enabled = true

# Absolute URL prefix recipients receive. Must be the externally reachable
# HTTPS origin of the public listener. Required when enabled.
public_base_url = "https://mail.example.com"

# Lifecycle.
default_ttl  = "48h"   # default lifetime offered at create when the caller supplies no expiresIn (REQ-ATT-65); the caller's chosen expiresIn is what applies at confirmation (REQ-SHARE-20)
max_ttl      = "90d"   # ceiling the owner cannot exceed
pending_ttl  = "48h"   # unconfirmed (compose-abandoned) shares expire here; matched to default_ttl
revoked_grace = "24h"  # how long a revoked share lingers as "revoked" before deletion

# Per-principal caps.
max_shares_per_principal = 1000
share_quota_per_principal = "5 GiB"

# Public-download rate limiting (REQ-SHARE-32).
download_requests_per_ip_per_share = 50
download_requests_window = "10m"
```

## Quotas and accounting

- **REQ-SHARE-50** Share storage counts against a dedicated per-principal **share quota** (`share_quota_per_principal`), separate from the mailbox quota (REQ-STORE-50): a share holds bytes that are not in any mailbox. Because blobs are deduplicated, a share whose blob is already referenced by one of the principal's messages adds **zero** marginal quota cost; quota is measured over the set of distinct blobs the principal's shares reference, not the sum of share sizes.
- **REQ-SHARE-51** Quota is enforced at `FileShare/set create`. Over quota returns `over_quota`. The `pending`/`active` distinction does not exempt pending shares from quota (a burst of pending creates still consumes bytes until they expire).
- **REQ-SHARE-52** An admin override flag (`ignore_share_quota`, per principal) lifts the cap; setting it is audit-logged. Admin can also raise a single principal's quota in application config.

## Abuse and safety

- **REQ-SHARE-60** Every share create / confirm / revoke / destroy is audit-logged with the principal id, share id, blob hash, filename, content type, and size. Public downloads are NOT individually audited (REQ-SHARE-44 rationale); an aggregate per-share `download_count` is the record.
- **REQ-SHARE-61** Admin surface: an operator MAY list all shares (across principals), see owner + filename + size + content type + download count, and **revoke** any share. Admin revocation is audit-logged and takes effect at the next fetch (REQ-SHARE-22). This is the lever for a takedown request or a principal distributing malware.
- **REQ-SHARE-62** The server does not scan share contents in v1 (no AV, no LLM classifier on the offload path). If a future deployment wants pre-publication scanning it hooks the spam/scan plugin surface at `FileShare/set create`; that integration is out of v1 scope and noted only so the seam is not foreclosed.
- **REQ-SHARE-63** Content-type integrity: the server serves the stored `content_type` verbatim and sets `X-Content-Type-Options: nosniff` so a `.zip` cannot be coerced by a downloading browser into an active type. HTML and SVG shares are served as `application/octet-stream` with `Content-Disposition: attachment` regardless of stored type, so a share URL can never be used to host script executing on the server's origin.

## Observability

- **REQ-SHARE-70** Metrics: counters `file_share_create_total`, `file_share_confirm_total`, `file_share_revoke_total`, `file_share_download_total{result=ok|gone|ratelimited}`, `file_share_sweep_deleted_total{reason=expired|pending|revoked}`; gauges `file_share_active` and `file_share_bytes` per principal (sampled, for spotting principals near the cap or abusing the surface).
- **REQ-SHARE-71** A `herold diag` check verifies share integrity: every `active`/`pending` share's `blob_hash` exists in the blob store, and every share blob is counted by the GC liveness callback (no share whose blob the GC would reap). Surfaced alongside `herold diag fsck` (REQ-STORE-110).

## Interactions

- **Blob GC (REQ-STORE-12, REQ-STORE-103):** the GC liveness callback MUST treat a blob as referenced while any non-deleted `file_shares` row points at it. The orphaned-blob startup scan and the share sweeper cooperate: the sweeper deletes share rows; ordinary blob GC reclaims the bytes after the grace window.
- **Download rate limiting (REQ-STORE-20..25):** owner-side limits govern the principal pulling their own data; REQ-SHARE-32 governs the public, anonymous side. They are separate buckets.
- **Quota (REQ-STORE-50..54):** share bytes are a distinct quota dimension (REQ-SHARE-50); a principal can be within mailbox quota yet over share quota, and vice versa.
- **Compose / send (web REQ-MAIL-*):** offload happens during compose; the carrying message references the share only as a link in its body — there is no MIME attachment and no `Email`→`FileShare` foreign key. A sent message and its shares have independent lifetimes; deleting the message never revokes the share, and a revoked share leaves a dead link in an already-sent message (unavoidable and documented).
- **HTTP send API (REQ-SEND-*):** non-suite senders using the REST send API do not get the offload affordance in v1 (it is a suite-side compose feature). A REST `POST /api/v1/shares` parallel to `FileShare/set create` is a plausible later addition and is left out of v1.

## Out of scope (v1)

- End-to-end encryption (key in the URL fragment, browser-side decrypt, server-blind storage). v1 stores plaintext; the server can read share contents. Noted as the obvious phase-2 privacy upgrade but explicitly not built now.
- Server-side malware/AV scanning of share contents before publication (REQ-SHARE-62).
- Live per-download push to the owner; the owner's `download_count` reconciles on management-view poll (REQ-SHARE-44).
- Shares created by anything other than an authenticated principal (no anonymous uploads, no inbound-mail-triggered shares).
- S3 or any remote blob backend for shares — they use the same local blob store as everything else (REQ-STORE-03).
- "Request a file" inbound shares, expiring upload links, folder/multi-file bundles. Single-file, sender-initiated only.
- Per-recipient access control or named-recipient gating. The capability URL plus optional password is the entire access model; anyone with the link can fetch.
