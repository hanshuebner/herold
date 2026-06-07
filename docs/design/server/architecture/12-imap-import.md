# 12 — Inbound IMAP import (live mirror)

How herold connects *out* to an external IMAP server as a client,
mirrors its mail into a principal's mailboxes byte-for-byte, indexes
it, and propagates a limited, best-effort set of changes back upstream.
Behavioural requirements in `../requirements/19-imap-import.md`
(REQ-IMAP-IMP-*). This doc is the *how*. It implements issue #25
("IMAP client mode") and is governed by six maintainer decisions
recorded at the head of the requirements file; each is referenced
below where it shapes the design.

This is the first subsystem in which herold acts as an IMAP **client**.
Every other IMAP surface (`internal/protoimap`) is the server side. The
client uses the `emersion/go-imap/v2` client that is already a
dependency (today only its server-protocol types are used).

## The seam: ingest as-synced, do not re-deliver

The single most important design decision (requirements decision 1) is
that a mirrored message is **stored as-synced**, not re-delivered. The
SMTP inbound path runs a long pipeline in `internal/protosmtp`
(`deliver.go` → `deliverOne`): mail-auth, spam `classify()`, Sieve
`runSieve()`, attachment policy `applyAttPol*`, `extimg.Internalize()`,
`InsertMessage`, then webhook `dispatchSynthetic()`. The mirror
deliberately bypasses all of it and calls only the lowest-level store
append that IMAP APPEND itself uses:

```
store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{target})
```

`InsertMessage` atomically writes the `messages` row + the
`message_mailboxes` rows, advances the mailbox UID/MODSEQ, increments
the blob refcount, and **appends an `(EntityKindEmail, ChangeOpCreated)`
state-change entry**. That last clause is why FTS comes for free: the
existing `storefts.Worker` reads the change feed and indexes the new
message asynchronously, exactly as it does for SMTP-delivered mail. No
new FTS plumbing.

```
            upstream IMAP server (gmail / fastmail / dovecot / cpanel)
                        │  IDLE + FETCH (TLS)
                        ▼
   ┌──────────────────────────────────────────────────────────────┐
   │ internal/imapimport — per-account worker                       │
   │                                                                │
   │  IDLE conn ──notify──► fetch round ──► raw RFC822 bytes        │
   │                                          │                     │
   │                              dedupe by Message-ID / blob_hash  │
   │                                          │ new?                │
   │                                          ▼                     │
   │   Blobs().Put(bytes) ──► Meta().InsertMessage(msg, [target])   │
   │                                          │                     │
   │           map upstream UID ──► imapimport_message_state        │
   │                                          │                     │
   │              INBOX + new? ──► categorise.CategoriseRich (opt)  │
   └──────────────────────────────────────────────────────────────┘
                        │ InsertMessage appends change-feed entry
                        ▼
          storefts.Worker ──reads change feed──► Bleve index   (free)
```

What the mirror does **not** call: `classify`, `runSieve`,
`applyAttPolHeaderCheck` / `applyAttPolPostAcceptance`,
`extimg.Internalize`, `WebhookDispatcher.DispatchSynthetic`. The only
pipeline step retained is `categorise.CategoriseRich`, and only under
the same gate the live path uses (mapped to INBOX, not classified spam,
newly-arrived) — never across the historical backfill, where running an
LLM over 100k messages would be ruinous.

Byte-fidelity is load-bearing: because the stored bytes equal the
upstream bytes, the upstream's DKIM signatures and
`Authentication-Results` survive and remain independently verifiable
(REQ-IMAP-IMP-32/33). This is why the worker does **not** prepend a
synthetic `Received:` header — provenance lives in the import-state row,
not in the message.

## Components

### Package: `internal/imapimport`

A new package owning the worker pool, the per-account state machine, the
IMAP client wrapper, and the write-back driver. It is *not* owned by
`imap-implementor` (that agent owns the server, `internal/protoimap`);
this is a distinct client subsystem, though wire-level review of the
client FETCH/STORE/SEARCH handling belongs with `imap-implementor` and
`conformance-fuzz-engineer`.

Goroutine model (REQ-IMAP-IMP-26): a single pool sized to
`[imap_import] concurrent_accounts` (default 16). Each enabled account
owns one supervising goroutine that holds the primary IDLE connection
and, when the upstream allows it, a second connection for concurrent
FETCH (REQ-IMAP-IMP-21). One slow upstream cannot starve others.

### Store: new tables (migration 0057)

Carried in lock-step on SQLite (`internal/storesqlite/migrations`) and
Postgres (`internal/storepg/migrations`), next free number `0057`.

```
imapimport_account(
    id, principal_id, account_name, host, port, tls_mode,
    username, auth_method, backfill_floor_date NULL,   -- NULL = "all"
    credential_ct BLOB,                                -- secrets.Seal output, v1: prefixed
    state, last_success_at, last_error,
    delete_propagates BOOL, created_at, updated_at)

imapimport_folder_map(account_id, upstream_folder, herold_mailbox_name)

imapimport_folder_cursor(
    account_id, upstream_folder,
    uidvalidity, uidnext,
    low_water_uid,            -- backfill floor (REQ-IMAP-IMP-17)
    high_water_uid,           -- last fully-synced UID forward
    highest_modseq)           -- CONDSTORE resume

imapimport_message_state(
    account_id, upstream_folder, upstream_uid,
    herold_message_id, herold_mailbox_id,
    last_synced_flags)        -- \Seen/\Flagged snapshot for conflict reconcile
```

The non-secret/secret split on `imapimport_account` mirrors
`store.IdentitySubmission` (plaintext config columns + one sealed `*_ct`
column). New repository methods are added directly to the `store.Store`
interface following the `FileShares` precedent (no separate sub-interface):
`CreateIMAPImportAccount`, `UpdateIMAPImportAccount`,
`ListIMAPImportAccountsByPrincipal`, `ListEnabledIMAPImportAccounts`
(boot reconnect), `GetIMAPImportFolderCursor` / `UpsertIMAPImportFolderCursor`,
`UpsertIMAPImportMessageState`, `GetIMAPImportMessageStateByMessage`
(write-back lookup), `DeleteIMAPImportAccount` (cascade).
`CreateIMAPImportAccount` validates `credential_ct` carries the `v1:`
prefix before insert, mirroring `ValidateIdentitySubmissionCTs`.

### Credentials: reuse `internal/secrets` (decision 2)

No new crypto. The credential (password / app-password / OAuth refresh
token) is sealed with `secrets.Seal(dataKey, plaintext)` →
`v1:`-prefixed ChaCha20-Poly1305 ciphertext, stored in
`credential_ct`. The data key is loaded once at boot via
`secrets.LoadDataKey(cfg.Server.Secrets)` from `[server.secrets]
data_key_ref`. The worker calls `secrets.Open(dataKey, ct)` immediately
before a connect and discards the plaintext after the auth exchange
(REQ-IMAP-IMP-70). This is the exact pattern `store.IdentitySubmission`
already uses for outbound submission credentials, so the threat model in
`internal/secrets/doc.go` (protects against raw-DB-read without runtime
access) carries over unchanged. It is what makes self-service possible:
a suite user typing a Fastmail app-password has it sealed server-side
before it touches the DB — they never need write access to `system.toml`
or `/run/secrets`, resolving the contradiction the prior requirements
left open.

`security-reviewer` reviews the credential path even though the
primitive is established, because the write-only JMAP/REST field and the
"never echo on read" rule are new surfaces.

### OAuth (xoauth2) — operator-provided app (decision 3)

`xoauth2` is the gmail/microsoft path. It does **not** ride herold's
login-OIDC federation (RP-for-login only, NG11): IMAP access needs the
restricted scope (`https://mail.google.com/`,
`https://outlook.office.com/IMAP.AccessAsUser.All`) under an OAuth app
the operator registers and — for Google — gets verified (CASA). That app
is configured separately:

```toml
[imap_import.oauth.google]
client_id     = "..."
client_secret_ref = "$HEROLD_GOOGLE_IMAP_CLIENT_SECRET"
token_endpoint = "https://oauth2.googleapis.com/token"
```

The per-account `credential_ct` holds the sealed **refresh token**; the
worker exchanges it for a short-lived access token before connect and,
if the provider rotates the refresh token, re-seals the new one. Because
gmail app-passwords still work for 2FA accounts (only "less secure app
access" died), `app_password` remains the lighter default and xoauth2 is
documented as the heavier, cleaner option (REQ-IMAP-IMP-52).

### JMAP `IMAPImport` type

Implemented under `internal/protojmap/mail/imapimport/` following the
`fileshare` package shape: `register.go` (`Register(reg, store, logger,
clk, secrets, ...)`), `methods.go`, `types.go`, `doc.go`. Capability
`https://netzhansa.com/jmap/imap-import` declared in
`internal/protojmap/registry.go`. `get` / `set` / `changes`; `set
create`/`update` accept the credential as a **write-only** field sealed
server-side; reads never return it. `set` mutations validate the
backfill horizon is present (REQ-IMAP-IMP-15) and resolve a relative
horizon to an absolute floor date at enable-time (REQ-IMAP-IMP-16).

### Admin REST

Per-principal sub-resource on the admin listener, following the
`/api/v1/principals/{pid}/...` pattern in `internal/protoadmin/routes.go`:

```
GET    /api/v1/principals/{pid}/imap-imports
POST   /api/v1/principals/{pid}/imap-imports
PATCH  /api/v1/principals/{pid}/imap-imports/{aid}
DELETE /api/v1/principals/{pid}/imap-imports/{aid}
```

The self-service subset is also exposed on the public listener's
protoadmin server (the same split Phase 4 used for `/settings`), gated
to the owning principal.

### Metrics

`internal/observe/metrics_subsystems.go`, `RegisterIMAPImportMetrics()`
behind a `sync.Once`, names per REQ-IMAP-IMP-63
(`herold_imapimport_*`). Closed label set: `account` (the account id is
bounded per principal; acceptable), `direction` (`up`/`down`), `kind`.

## The per-account state machine

```
        disabled ──user enable──► connecting
                                      │ auth ok
                                      ▼
   errored ◄──M failures── (backoff) running ──┐
      ▲                                  │      │ IDLE / poll
      │ user re-enable                   │      ▼
      └──────────────────────────  fetch round ─┘
```

`running` has two interleaved duties on the supervising goroutine:

1. **Forward sync (down).** On entry, for each mapped folder: SELECT,
   compare stored `uidvalidity`; on mismatch force re-sync
   (REQ-IMAP-IMP-35). Backfill: if `low_water_uid` is unset or above the
   horizon floor, `UID SEARCH SINCE <floor>` and fetch the resulting set
   below the current low-water mark, lowering it (REQ-IMAP-IMP-17/19).
   Forward: fetch UIDs ≥ `high_water_uid` (or use CONDSTORE
   `CHANGEDSINCE highest_modseq` when advertised, REQ-IMAP-IMP-24),
   advancing the high-water mark. Each fetched body is deduped
   (Message-ID, blob_hash fallback), `Blobs().Put`, `InsertMessage`,
   and a `imapimport_message_state` row written with the current
   upstream flags as `last_synced_flags`.

2. **Flag/membership reconcile (both directions).** See below.

IDLE (REQ-IMAP-IMP-20) on the primary connection blocks until an
`EXISTS`/`EXPUNGE`/`FETCH` unsolicited response or the IDLE timeout;
each wake triggers a fetch round on the second connection
(REQ-IMAP-IMP-21), then IDLE is re-armed. Without RFC 2177 the worker
NOOP-polls every `poll_interval` (REQ-IMAP-IMP-23). Latency target 10 s
p95 notification-to-store (REQ-IMAP-IMP-22).

## Write-back: change feed → upstream (upstream-authoritative)

Herold-side changes are discovered by subscribing to the **store change
feed** with `ReadChangeFeedAll` (the cause-blind reader IMAP IDLE and
the FTS worker already use) for the account's principal, filtered to
`EntityKindEmail`. A durable per-account cursor
(`GetMaxChangeSeqForPrincipal` style) tracks position so a restart never
re-replays.

For each `EntityKindEmail` change the worker looks up
`imapimport_message_state` by herold message id:

- **flag update** (`\Seen`/`\Flagged` differs from `last_synced_flags`)
  → IMAP `UID STORE` on the mapped upstream folder.
- **membership update** (mailbox changed) → IMAP `UID MOVE` (or
  COPY+EXPUNGE, RFC 6851) (REQ-IMAP-IMP-43).
- **destroy** → `UID STORE +FLAGS \Deleted` + `UID EXPUNGE`, unless
  `delete_propagates = false` (REQ-IMAP-IMP-44).

All write-back is **best-effort** (decision 5). Conflict resolution is
**upstream-authoritative**, implemented with the three-way compare in
REQ-IMAP-IMP-42 using `last_synced_flags` as the base:

```
        last_synced   herold-now   upstream-now   action
        a             a            a              none
        a             b            a              STORE herold→upstream; last:=b
        a             a            b              apply upstream→herold; last:=b
        a             b            c              upstream wins: herold:=c; last:=c   (conflict++)
```

A failed herold→upstream STORE leaves `last_synced_flags` untouched, so
the push is retried next reconcile. A destroy whose EXPUNGE failed leaves
the message present upstream, so it re-mirrors on a later pass — the
documented, logged consequence of upstream-authoritative best-effort
(REQ-IMAP-IMP-44). No HLC, no per-field clocks; the simplification
versus 18-partial-replica.md is intentional and only possible because
the upstream is declared the source of truth.

## Backfill horizon (decision 6)

The horizon is a one-time floor on historical reach, **not** a retention
window — nothing is ever evicted (REQ-IMAP-IMP-18). This is the explicit
contrast with `18-partial-replica.md`, whose `window_days` continuously
GCs old mail off the edge. Concretely:

- `backfill_floor_date` is required at creation (REQ-IMAP-IMP-15);
  relative presets resolve to an absolute date at enable-time
  (REQ-IMAP-IMP-16).
- The floor is enforced with `UID SEARCH SINCE` (INTERNALDATE), and the
  lowest fetched UID becomes `low_water_uid`. UIDs below it are never
  examined again.
- Lowering the floor re-scans the gap and lowers the mark
  (REQ-IMAP-IMP-19); raising it is a no-op.
- `herold_imapimport_backfill_remaining{account}` lets the operator
  watch a large initial mirror drain.

## Gmail folder-based label placement (Option B)

Gmail exposes per-message labels as IMAP folders. The worker uses
**folder-based label placement** rather than per-message label metadata
(REQ-IMAP-IMP-50/51):

**Why not per-message labels?** `X-Gmail-Labels:` is a Takeout/Vault
export artifact — it is NOT present in messages FETCHed over live IMAP.
`X-GM-LABELS` (the X-GM-EXT-1 FETCH data item) cannot be fetched by the
pinned `emersion/go-imap/v2 v2.0.0-beta.8` client because its FETCH
parser errors on unknown msg-att names.

**Folder classification:**

| Class     | Folders                                                                          | Action                          |
|-----------|----------------------------------------------------------------------------------|----------------------------------|
| Skip      | `[Gmail]/Important`, `[Gmail]/Starred`, `[Gmail]/Chats`                          | No sync — virtual/flag, no unique content |
| Normal    | `INBOX`, `[Gmail]/Sent Mail` → Sent, `[Gmail]/Drafts` → Drafts, `[Gmail]/Spam` → Junk, `[Gmail]/Trash` → Trash, every user-label folder → same name | Sync into mapped herold mailbox |
| AllMail   | `[Gmail]/All Mail`                                                                | Synced LAST, envelope-first dedup (see below) |

**Multi-mailbox-on-dedup:** A message appearing in K label folders gets K
herold mailbox memberships. When `ingestMessage` hits a dedup (Message-ID
already in herold), it checks whether the message is already a member of
the current target mailbox. If not, `AddMessageToMailbox` adds the
membership. Re-syncing the same folder is idempotent (`AddMessageToMailbox`
returns `ErrConflict` for existing memberships; caught and ignored). This
mechanism is general — it applies to all IMAP accounts, not just Gmail.

**All Mail envelope-first dedup (archived mail capture):** After all Normal
folders are synced, `[Gmail]/All Mail` is synced last. For each UID in the
horizon the worker fetches ENVELOPE + UID + INTERNALDATE + FLAGS (no body).
If the Message-ID is already in herold (placed by a label folder), the body
download is skipped entirely. Only for un-mirrored messages (archived mail
with no label), the worker body-fetches (BODY.PEEK[]) and ingests into
"Archive". This captures archived-unlabeled mail without re-downloading
bodies already placed by label folders.

**Category tabs:** Gmail category tabs are NOT replicated — they are not
consistent IMAP folders across all accounts. herold's own LLM categoriser
runs on imported INBOX mail (REQ-IMAP-IMP-31) and produces `$category-*`
keywords locally.

**Testability:** The Gmail-specific path (`syncAllFoldersGmail`,
`syncFolderGmailAllMailEnvelopeDedup`) is not exercised by the automated
test suite because the in-process `imapmemserver` does not implement
X-GM-EXT-1 or `[Gmail]/All Mail` semantics. The multi-mailbox-on-dedup
mechanism IS tested end-to-end via `TestMultiMailboxPlacement` (normal IMAP
folders, same Message-ID in two folders → two herold memberships, idempotent
on re-sync). The All Mail envelope-dedup decision is unit-tested in
`gmail_test.go` via `TestEnvelopeDedupDecision_*`.

## Observability of the workers (REQ-IMAP-IMP-65)

Three layers, each answering a different question:

- **Prometheus** (`herold_imapimport_*`) — "how much, over time": fetch
  counts, flag propagation, conflicts, connection errors, idle seconds,
  backfill remaining. Aggregate and alertable, but not introspective.
- **Persisted account state** (`imapimport_account.state` / `last_error` /
  `last_success_at`) — "is it enabled / did it give up": coarse, durable,
  survives restart.
- **Live snapshot** (`Pool.Snapshot()`) — "what is each worker doing right
  *now*": the missing middle. Each `accountWorker` owns a small status
  value it mutates as it moves through phases (`starting`, `connecting`,
  `backoff`, `syncing` + current folder, `idle`, `polling`, `writeback`,
  `errored`, `stopped`), recording connection mode (single/dual), the
  phase-start time, last-successful-sync time, consecutive failures, next
  poll time, and run-cumulative fetched/propagated counts. The Pool holds
  these behind a mutex (or per-worker atomics) and `Snapshot()` copies them
  out — a pure in-memory read, no DB round-trip, never blocking a worker.

The snapshot is the substrate the operator surfaces consume: the
`herold imapimport status` CLI (REQ-IMAP-IMP-62) and the read-only
`GET /api/v1/imap-imports/status` admin endpoint render it directly. This
keeps the "what's happening now" view honest — it reflects the worker's
actual in-loop state rather than a value written to the DB on a timer.

## Failure and edge behaviour

- **UIDVALIDITY rollover** — invalidate the folder's UID map, re-fetch
  from the horizon floor (not UID 1), reconcile by Message-ID so no
  duplicates appear (REQ-IMAP-IMP-35).
- **Dual delivery during migration** — if herold is also the MX while
  the mirror runs, the same message can arrive twice. Dedup by
  `env_message_id` (blob_hash fallback) collapses it (REQ-IMAP-IMP-30).
- **Second connection refused / rate-limited** — fall back to
  single-connection mode and raise the backoff exponent
  (REQ-IMAP-IMP-21/73); repeated rate-limiting → `errored`.
- **M consecutive connection failures** (default 20) → `state =
  errored`, surfaced via `IMAPImport/get` and `herold imapimport
  status`; no reconnect until the user re-enables (REQ-IMAP-IMP-25).
- **Restart mid-mirror** — cursors, account state, and
  `last_synced_flags` are durable; boot reconnects every `enabled`
  account from its persisted cursor, forcing a horizon-bounded re-sync
  only if UIDVALIDITY drifted (REQ-IMAP-IMP-74).
- **Credential rejected after a working period** (password changed
  upstream, OAuth refresh revoked) → connection error → backoff →
  `errored`; `last_error` tells the user to update the credential.
- **Data key absent / wrong** — `secrets.Open` fails; the account cannot
  connect and reports an unrecoverable credential error rather than
  silently skipping (an operator who rotated `data_key_ref` without
  re-sealing must re-enter credentials).

## Why a distinct package, not an extension of protoimap

`internal/protoimap` is a server: it answers client commands. The mirror
is a client: it issues them, manages long-lived outbound connections,
IDLE supervision, OAuth token exchange, and a write-back loop driven by
the store change feed. Sharing the `emersion/go-imap/v2` dependency is
enough; fusing the two would entangle the server's session lifecycle
with an outbound connection pool that has nothing to do with it. The
clean boundary also keeps the credential-handling and SSRF concerns
(REQ-IMAP-IMP-70/72) in one auditable package.
