# 11 — Attachment shares

How a large or scanner-hostile attachment is lifted out of a message, parked in the blob store as an owner-scoped capability object, and served to an external recipient over an unauthenticated HTTPS link. Behavioural requirements in `../requirements/25-attachment-shares.md`; web compose flow in `../../web/requirements/17-attachments.md` (REQ-ATT-60..73). This doc is the *how*.

## The seam: promote, don't copy

The composer already uploads every attachment to the JMAP upload endpoint to obtain a `blobId` before send (web REQ-MAIL-17). That blob is content-addressed (BLAKE3) and lives in the same store as message bodies. An offload therefore does not move or re-upload bytes — it **promotes** that already-resident blob into a `FileShare` row that holds a durable reference to it. The difference between "this blob is an attachment in a draft" and "this blob is a share" is entirely in the metadata layer: which row holds the reference that keeps the blob alive.

```
            ┌─────────────────────────────────────────────────────────┐
            │ Suite compose                                           │
            │                                                         │
            │  pick file ──► JMAP upload ──► blobId (ephemeral)       │
            │                                   │                     │
            │           large / unsafe?  yes ───┤                     │
            │                                   ▼                     │
            │                       FileShare/set create {blobId,...} │
            │                                   │                     │
            │            insert link in body ◄──┤ {id,url,expiresAt}  │
            │                                   │                     │
            │   send (EmailSubmission) ─► FileShare/set update active │
            └─────────────────────────────────────────────────────────┘
                                                │
                  ┌─────────────────────────────┴───────────────┐
                  │ metadata store                              │
                  │  file_shares row ──holds ref──► blob_hash   │
                  └─────────────────────────────────────────────┘
                                                │
   recipient ──GET /share/{id}──► public listener ──reads──► blob bytes
```

One upload. The blob is shared (deduplicated) between the draft's attachment view and the `FileShare` row; if the user also leaves it as a normal attachment, both references point at the same bytes.

## Components

### Store: `file_shares`

Owned by the metadata store (`internal/store`), with the schema in REQ-SHARE-10, carried in lock-step on SQLite and Postgres. The repository interface adds:

```go
type FileShares interface {
    Create(ctx, FileShareCreate) (FileShare, error)  // installs blob ref, state=pending
    Confirm(ctx, principal, id) (FileShare, error)    // pending -> active, resets expiry
    Revoke(ctx, principal, id) error                  // -> revoked, sets revoked_at
    Destroy(ctx, principal, id) error                 // delete row, release blob ref
    GetByID(ctx, id) (FileShare, error)               // public path: by capability token only
    ListByPrincipal(ctx, principal, ...) ([]FileShare, error)
    RecordDownload(ctx, id) (FileShare, error)        // atomic count++ , last_downloaded_at
    Sweep(ctx, now) (deleted SweepStats, error)       // pending/expired/revoked GC
}
```

`GetByID` is the only lookup the public download path uses, and it keys on the capability token alone — there is no principal in the request. It returns the row regardless of state; the handler, not the store, decides whether the row is *serveable* (REQ-SHARE-11e) so that "not serveable" is indistinguishable across all the not-serveable reasons.

### Blob liveness

The blob store GC (REQ-STORE-12) reclaims a blob when its liveness callback returns false. That callback is extended so a hash counts as live if any non-deleted `file_shares` row references it, ORed with the existing message-reference check. The share sweeper (below) is what eventually flips a blob from live to dead — by deleting the row — after which ordinary blob GC reclaims the bytes on its normal grace schedule. No new blob-GC path; one more reference source.

### Public routes on the public listener

Registered in `internal/admin/server.go` on the public mux, next to `/jmap`, `/api/v1/mail/`, and the webhook-fetch handler:

```go
publicMux.Handle("GET /share/{id}",          shareSrv.Landing())   // HTML page
publicMux.Handle("GET /share/{id}/download",  shareSrv.Download())  // bytes
publicMux.Handle("POST /share/{id}/unlock",   shareSrv.Unlock())    // password -> cookie
```

These are the only herold routes that serve principal-owned content with no authentication middleware. They model on the existing `protowebhook` fetch handler (the precedent for "the URL is the credential"), but unlike webhook-fetch they are stateful: the token is a DB key, not a stateless HMAC, because revocation, download counting, and password gating all need a row. A new package `internal/protoshare` owns the three handlers, the landing-page template, the password cookie (short-lived, HMAC-signed, scoped to one share id), and the per-IP/per-share rate limiter (in-process, REQ-SHARE-32).

### JMAP `FileShare` type

Implemented in `internal/protojmap` alongside the other account-scoped types (mirrors the `TaggedAddressFilter` shape from the tagged-address feature). `get`/`set`/`changes`/`query`, capability `https://netzhansa.com/jmap/file-shares`. `set create` resolves `blobId` → `BlobRef` within the requesting account and calls `FileShares.Create`; `set update` is the confirm/shorten/lower path (REQ-SHARE-42); `password` is write-only (REQ-SHARE-43).

### Sweeper

A periodic metadata sweeper (same machinery as session-eviction and other TTL sweeps) runs `FileShares.Sweep`, which in one pass deletes pending shares past `pending_ttl`, active shares past `expires_at`, and revoked shares past `revoked_grace`, emitting `file_share_sweep_deleted_total{reason}`. Released blobs are reclaimed by the next blob-GC cycle.

## Request flows

### Create + confirm (suite)

1. `FileShare/set create { blobId, name, type, expiresIn, maxDownloads?, password? }`.
2. Server resolves `blobId` within the account; rejects if not readable (`forbidden`).
3. `FileShares.Create` inserts a `pending` row with a CSPRNG `id`, `expires_at = now + pending_ttl`, optional Argon2id `password_hash`; installs the blob reference; enforces count + quota caps in the same transaction.
4. Returns `{ id, url: public_base_url + "/share/" + id, expiresAt, hasPassword, ... }`.
5. Suite inserts the link into the HTML body and a plain-text URL into the `text/plain` alternative, removes the file from the MIME attachment set.
6. On send, after `EmailSubmission/set` succeeds, the suite batches `FileShare/set update { <id>: { state: "active" } }`. `Confirm` flips `pending → active` and resets `expires_at = now + default_ttl` (clamped to `max_ttl`).
7. If the draft is discarded before send, the suite issues `FileShare/set destroy`; any it misses expire at `pending_ttl`.

### Download (recipient)

1. `GET /share/{id}` → `GetByID`. If not serveable (REQ-SHARE-11e) → `410` with a generic page. If locked → landing page with a password field; otherwise landing page with metadata + Download button.
2. (locked only) `POST /share/{id}/unlock` with the password → Argon2id verify (constant-time outcome) → set a short-lived signed cookie scoped to this share; redirect to the landing page in the unlocked state.
3. `GET /share/{id}/download` → re-check serveability + (if locked) the unlock cookie → `RecordDownload` (atomic `download_count++`, committed before the stream completes so `max_downloads` cannot be raced) → stream bytes with `Content-Type` = stored type (or `application/octet-stream` for HTML/SVG, REQ-SHARE-63), `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: private, no-store`, `Accept-Ranges: bytes`.

## Why stateful capability tokens, not stateless HMAC

The webhook-fetch precedent signs `(blob, exp, deliveryID)` with an HMAC and carries no DB row — cheap, but it cannot revoke, count, or password-gate without a lookup. Shares need all three (REQ-SHARE-22 revocation, REQ-SHARE-02 download counts, REQ-SHARE-11c password). So a share is a random DB-keyed token: the `id` is the capability (≥128 bits, unguessable), and the row carries the policy. The cost is one indexed lookup per fetch, which is dwarfed by the byte streaming that follows.

## Why no E2E encryption in v1

Key-in-fragment E2E (Firefox-Send shape) would make the server blind to share contents — attractive for a privacy-first mailer. It is deferred because: (1) it forecloses server-side password gating and download caps as implemented here (those would move into the encrypted envelope), (2) the recipient can no longer `wget` the URL — a JS download/decrypt page becomes mandatory, and (3) it is a self-contained upgrade that can be layered on later by storing ciphertext blobs and serving a decrypting page, without changing the lifecycle, quota, or sweeper machinery this doc describes. The architecture leaves the seam open (the blob is opaque to everything except the download handler) but v1 stores and serves plaintext.

## Failure and edge behaviour

- **Blob vanished under a live share** (operator corruption, manual deletion): `Download` returns `500` (not `410` — the share is valid, the store is broken); `herold diag` (REQ-SHARE-71) reports the dangling share.
- **Confirm after expiry**: a `pending` share whose `pending_ttl` already elapsed and was swept cannot be confirmed; `Confirm` fails and the suite surfaces "share expired, re-attach" on send.
- **Concurrent downloads racing `max_downloads`**: `RecordDownload` increments under the row lock and the handler re-reads the post-increment count before committing to stream; the `(count+1) > max` loser gets `410`.
- **Principal deleted**: `ON DELETE CASCADE` drops the rows; blob references release; blob GC reclaims. No public route can resolve a deleted owner's shares.
