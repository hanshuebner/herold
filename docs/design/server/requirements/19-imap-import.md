# 19 — Inbound IMAP import (live mirror)

*(Added 2026-05-08. Distinct from 16-import.md, which covers one-shot
archive imports. This file specifies a long-running per-user worker
that mirrors an upstream IMAP account into herold in near-real-time.
The motivating use case is users whose mail arrives at gmail / fastmail
/ a work account and who want herold to be their daily-driver MUA
endpoint without changing the upstream MX. After challenging the
"two IMAP connections" wording in the request: the two-connection
shape is an implementation detail of how to keep IDLE responsive
while FETCHing concurrently. The user-facing requirement is "low
latency from upstream-arrival to herold-visible," not the connection
count. We spec the latency target.)*

*(Revised 2026-06-07 to reconcile against issue #25 "IMAP client mode".
Six design decisions taken by the maintainer drive this revision and
are called out inline at the requirements they touch:*

1. ***Store as-synced.** Mirrored messages are persisted verbatim and
   indexed for FTS; they do NOT re-run the inbound delivery pipeline
   (no spam, no Sieve, no attachment policy, no webhooks, no
   import-time external-image internalization). Only LLM categorisation
   of newly-arrived INBOX-mapped mail runs, matching the live delivery
   path's own gating. Rationale: a mirror's folder placement is dictated
   by the upstream folder mapping, and Sieve `fileinto` would fight that
   mapping; re-running spam/webhooks on a historical backfill is
   redundant and dangerous. (Supersedes the original REQ-IMAP-IMP-31.)*
2. ***App-encrypted credentials.** Upstream credentials are sealed in
   the DB with the existing `internal/secrets` data key
   (`[server.secrets].data_key_ref`), exactly as outbound submission
   credentials already are (`store.IdentitySubmission` `*CT` fields).
   This is reuse of an established pattern, not new crypto surface, and
   it is what makes user self-service possible. (Rewrites
   REQ-IMAP-IMP-02 / -04 / -70.)*
3. ***App-password first; xoauth2 is operator-provided.** The default
   credential is a password / app-password. `xoauth2` is supported but
   requires the operator to register an OAuth application carrying the
   restricted IMAP scope; it does NOT fall out of herold's login-OIDC
   plumbing, which is RP-only for login (NG11). (Rewrites
   REQ-IMAP-IMP-03 / -52.)*
4. ***Full live-mirror is the single target.** IDLE + second connection
   + the 10 s latency target + MOVE/EXPUNGE/delete write-back are in
   scope from the first cut, not phased. (REQ-IMAP-IMP-20..26 and the
   write-back section stand.)*
5. ***Upstream-authoritative conflicts.** Write-back is best-effort and
   the upstream is the source of truth. On a genuine both-sides-changed
   conflict the upstream value wins and the herold-side change is
   overwritten on the next reconcile. No hybrid-logical-clock machinery.
   (Supersedes the original REQ-IMAP-IMP-42's HLC borrow.)*
6. ***User-chosen backfill horizon (accumulate).** The user picks how
   far back the initial mirror reaches; mail older than the horizon is
   never fetched. The horizon bounds only the initial backfill — once
   mirrored, mail accumulates in herold forever (herold becomes the
   daily-driver / archive). There is NO retention GC; this is the
   explicit contrast with 18-partial-replica.md, whose `window_days` is
   a rolling cache window. (New REQ-IMAP-IMP-15..19.)*

*The "how" lives in architecture/12-imap-import.md.)*

*(Revised 2026-06-27 — re-plan and validation pass against issue #25's
recently-edited description (the "important use case" and the inline
answers to the open questions). The shipped implementation was validated
against this file; three further maintainer decisions:*

7. ***Conflict model stands; the issue's "latest wins" answer is
   corrected to match the build.** The shipped design is
   upstream-authoritative (decision 5), NOT timestamp-based "latest
   wins" — there is no per-side change clock, and decision 5 deliberately
   removed the HLC machinery that a true latest-wins rule would need.
   This is kept as the intentional simplification; instead the issue
   description's open-question answer is amended to "upstream-
   authoritative while mirroring; herold-authoritative at cutover."
   REQ-IMAP-IMP-42 is unchanged.*
8. ***Complete-migration cutover is now first-class.** The "important use
   case" — trial in client mode, then fully migrate off the upstream
   while preserving the state the user built up in herold — was not
   modelled by the perpetual-mirror design. New requirement set
   REQ-IMAP-IMP-90..96 specifies a one-shot complete backfill followed by
   an authority transfer to herold and retirement of the upstream
   connection. This is the lifecycle boundary at which upstream-
   authoritative conflict handling (decision 5) stops applying.*
9. ***True Gmail per-message labels via X-GM-LABELS is planned, not
   permanently deferred.** The folder-based placement in
   REQ-IMAP-IMP-50/51 is an interim measure forced by the pinned
   `go-imap` beta.8's inability to fetch the X-GM-EXT-1 `X-GM-LABELS`
   msg-att. New REQ-IMAP-IMP-53 mandates upgrading/patching the client so
   per-message label placement becomes the Gmail path, with folder-based
   placement retained as the fallback for non-Gmail servers.*

*Three doc defects found during validation are corrected in this
revision: a dangling REQ-IMAP-IMP-30bis cross-reference (the mechanism
is REQ-IMAP-IMP-51), a duplicated REQ-IMAP-IMP-52 row, and the
architecture doc's "migration 0057" (the migration shipped as 0058 after
a number collision with `file_shares_source`).)*

## Scope

A herold principal can configure one or more **upstream IMAP
accounts**. A per-account worker maintains a long-lived authenticated
IMAP connection, observes new-mail notifications via IDLE, fetches
new messages **bounded by a user-chosen backfill horizon**, stores
them **as-synced** (verbatim bytes) in the principal's mailboxes,
and indexes them for full-text search. Flag changes the user makes in
herold (mark read, star) propagate back upstream via IMAP STORE on a
best-effort basis. Folder mappings are configurable per account.

Mirrored messages do **not** re-run herold's inbound delivery
pipeline (decision 1): spam classification, Sieve, attachment policy,
webhooks, and import-time external-image internalization are all
skipped. The bytes that arrive over IMAP are the bytes that land in
the store, so the upstream's DKIM signatures and `Authentication-Results`
stay intact and verifiable. LLM categorisation runs only for
newly-arrived mail mapped into INBOX, mirroring the live delivery
path's own INBOX-only / not-spam gating.

Out of scope:

- POP3. The lossy-delivery semantics make it a pain to integrate
  with herold's idempotent ingest.
- ManageSieve / IMAP-on-IMAP / IMAP-Submit hybrid roles. The
  upstream account is treated as a read source and a flag-write
  target only.
- Replication of herold-side keywords (custom keywords) back upstream.
  Only `\Seen` and `\Flagged` (= `$flagged` in JMAP) round-trip.
  Custom keywords stay herold-local.
- Sending mail through the upstream's submission server. If the
  user wants outbound to go via the upstream, they configure that
  separately via the existing identity / smarthost path.
- Retention / aging-out of mirrored mail. The backfill horizon bounds
  the *initial* reach only; herold keeps everything it has mirrored.
  Rolling-window retention with GC is 18-partial-replica.md's job, not
  this worker's.

## Motivating user flows

1. **Gmail user moving to herold.** Keeps gmail's MX and spam
   filtering, herold becomes the daily MUA. Folders mapped:
   gmail INBOX → herold INBOX, gmail All Mail → herold All Mail,
   etc. Gmail Takeout (file 16-import.md) gets the historical
   archive in once; live IMAP import takes over from there. The user
   typically sets a short backfill horizon (e.g. 90 days) because the
   deep archive came in via Takeout.
2. **Multi-provider consolidation.** A user has work + personal +
   alias accounts. Each is an upstream IMAP source; all mirror into
   one herold principal's mailboxes. Sent mail still goes via
   herold's identity / smarthost surface; the upstream accounts are
   one-way feeds for inbound and two-way for `\Seen`/`\Flagged`.
3. **Archive-from-elsewhere.** A user's old hosted mail provider
   doesn't have a takeout export. They configure an upstream IMAP
   pointing at it and choose a backfill horizon (often `all`); herold
   mirrors the account from the horizon forward on first connect, then
   keeps following. A large account is never mirrored in full by
   accident — the horizon is a required choice at account creation
   (REQ-IMAP-IMP-15).

## Configuration

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-01 | Each principal MAY configure zero-or-more upstream IMAP accounts. The configuration is stored per-principal in the metadata store (not in `system.toml`); a JMAP `IMAPImport/set` surface mutates it. |
| REQ-IMAP-IMP-02 | Each upstream-account record carries non-secret fields stored plaintext — `id`, `account_name` (operator-visible label), `host`, `port`, `tls_mode` (`starttls` / `implicit`), `username`, `auth_method` (`password`, `app_password`, `xoauth2`), `backfill_horizon` (REQ-IMAP-IMP-15), `state` (`enabled` / `disabled` / `errored`), `last_success_at`, `last_error` — plus exactly one **sealed** credential column (`credential_ct`) holding the password / app-password / OAuth-refresh-token encrypted with the `internal/secrets` data key. The non-secret/secret split mirrors `store.IdentitySubmission` (plaintext config fields + `*CT` sealed fields). (Decision 2.) |
| REQ-IMAP-IMP-03 | `auth_method = xoauth2` is the OAuth path for gmail / microsoft. It requires the **operator** to have registered an OAuth application that carries the restricted IMAP scope (e.g. Google's `https://mail.google.com/`, Microsoft's `https://outlook.office.com/IMAP.AccessAsUser.All`). This is a different grant from herold's login-OIDC federation (which is RP-for-login only, NG11) and is configured separately under `[imap_import.oauth.<provider>]` in `system.toml`. The per-account record stores a sealed refresh-token (`credential_ct`); the worker exchanges it for an access token before each connect and re-seals a rotated refresh token if the provider issues one. (Decision 3.) |
| REQ-IMAP-IMP-04 | `auth_method = app_password` and `auth_method = password` both store a static credential, sealed in `credential_ct`. The difference is the upstream's intended audience (app-passwords are the apple / fastmail / gmail-with-2FA flow; `password` is the cpanel / bare-IMAP flow). The IMAP layer treats them identically. Self-service users enter the credential through the suite; it is sealed before it touches the DB (REQ-IMAP-IMP-70). (Decision 2.) |
| REQ-IMAP-IMP-05 | Operators MAY block plain-password auth with an explicit `[imap_import] allow_password = false` config knob (default true so existing setups keep working; future iterations should flip the default). `app_password` and `xoauth2` are unaffected by this knob. |
| REQ-IMAP-IMP-06 | The connection MUST use TLS. `tls_mode = none` is rejected at config-time. STARTTLS that fails to negotiate is treated as a connection error. (Operators with internal-only legacy IMAP servers can VPN to them.) |

## Backfill horizon

*(Decision 6. The horizon bounds how far back the **initial** mirror
reaches; it is not a retention window. Contrast 18-partial-replica.md
REQ-REPL-10, whose `window_days` continuously evicts old mail — here
nothing is ever evicted.)*

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-15 | Every upstream-account record carries a `backfill_horizon`. It is a **required choice at account creation** — there is no implicit default that would mirror an entire large account by accident. The suite offers presets `30d` / `90d` / `1y` / `all` / `custom date`. |
| REQ-IMAP-IMP-16 | A relative horizon (`30d`, `90d`, `1y`) is **resolved to an absolute floor date at enable-time** and stored as that absolute date, so the horizon does not silently drift as time passes. `all` is stored as a sentinel meaning "no floor". |
| REQ-IMAP-IMP-17 | On initial sync of each mapped folder the worker issues `UID SEARCH SINCE <floor-date>` (INTERNALDATE-based) and fetches only the resulting UID set. The lowest fetched UID is recorded as the folder cursor's **low-water mark**; UIDs below it are never re-examined on subsequent passes. (The high-water mark — UIDNEXT / last-seen-UID — governs forward sync as usual; see REQ-IMAP-IMP-34.) |
| REQ-IMAP-IMP-18 | The horizon bounds only the historical backfill. **New** mail (UID ≥ the high-water mark) is always mirrored regardless of the horizon. Mirrored mail is retained indefinitely; there is no aging-out, eviction, or GC pass over imported messages. |
| REQ-IMAP-IMP-19 | Lowering the horizon (moving the floor date earlier) is allowed and triggers a **bounded re-scan**: the worker re-issues `UID SEARCH SINCE <new-floor>` and fetches the UID range between the new floor and the existing low-water mark, then lowers the low-water mark. Raising the horizon (moving the floor later) is a no-op — already-mirrored older mail is kept (REQ-IMAP-IMP-18). |

## Folder mapping

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-10 | Each upstream-account record carries a folder mapping table: `(upstream_folder_name, herold_mailbox_name)` pairs. Default mapping when absent: name-equals-name. The default produces gmail's `Inbox` ↔ herold's `INBOX`, `Sent Mail` ↔ `Sent Mail` (note the case mismatch is preserved verbatim — herold is case-insensitive). |
| REQ-IMAP-IMP-11 | Operators MAY supply a system-wide default mapping table per upstream-host pattern (e.g. "imap.gmail.com → these mappings"). Per-account overrides win. |
| REQ-IMAP-IMP-12 | Folders that exist upstream but have no mapping (and where the default name-equals-name lookup misses) are created in herold under the upstream name. The user can rename via the suite. |
| REQ-IMAP-IMP-13 | Folders that exist in herold but not upstream are unaffected by import — they hold whatever herold puts there (other-account imports, locally-created drafts). |
| REQ-IMAP-IMP-14 | Gmail's `[Gmail]/All Mail` is treated as a special case (REQ-IMAP-IMP-50/50b cross-reference the Gmail-specific behaviour). It is synced LAST with envelope-first dedup: only messages not already placed by a label folder (archived/unlabeled mail) are body-fetched and placed into the "Archive" herold mailbox. Each message's primary mailbox is determined by which label IMAP folder(s) it appears in (folder-based placement, Option B), not from per-message label headers. |

## Connection management and IDLE

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-20 | The worker maintains a primary always-on IDLE connection on each upstream account's INBOX. Notifications (`* 42 EXISTS`, `* 41 EXPUNGE`) interrupt the IDLE; the worker re-issues IDLE after each fetch round. |
| REQ-IMAP-IMP-21 | Concurrent FETCH while IDLE is active uses a **second connection** when the upstream supports it. Most servers do (gmail, fastmail, dovecot); exotic servers may have low connection caps. The worker auto-falls-back to single-connection mode when a second-connection auth fails with rate-limit / quota errors. |
| REQ-IMAP-IMP-22 | Latency target: notification-to-store must complete within 10 s p95 under normal conditions (typical-sized message, no auth refresh in flight). Operators concerned with latency can configure aggressive RTT and pipeline limits; the default is "polite." |
| REQ-IMAP-IMP-23 | When IDLE is unsupported by the upstream (RFC 2177 not advertised), the worker falls back to NOOP-poll every `poll_interval` seconds (default 60). Latency in this mode is bounded by the poll interval. |
| REQ-IMAP-IMP-24 | The worker uses CONDSTORE / QRESYNC when the upstream advertises them so re-sync after a disconnect is incremental rather than a full mailbox scan. |
| REQ-IMAP-IMP-25 | Connection failures use exponential backoff with jitter (2 s, 4 s, 8 s, … cap 5 min). After M consecutive failures (default 20) the account flips to `state = "errored"`; the operator-visible `IMAPImport/get` surface shows the last_error and the user can re-enable. The worker does not re-connect after `errored` until the user explicitly re-enables. |
| REQ-IMAP-IMP-26 | Workers per principal share a single `imapimport` package goroutine pool sized to `[imap_import] concurrent_accounts` (default 16). One slow upstream does not block other accounts on the same herold instance. |

## Mirror semantics

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-30 | Each upstream message is persisted in herold with the canonical `Message-ID` header preserved. The worker dedupes against the principal's existing `env_message_id` index; a message that already exists in herold (via prior takeout, prior IMAP-import session, dual SMTP+IMAP delivery during migration, or any other source) is not duplicated. Messages with no usable `Message-ID` fall back to a body content-hash (`blob_hash`) for dedup, matching the importer. |
| REQ-IMAP-IMP-31 | **(Decision 1 — supersedes the original "run the full inbound pipeline" wording.)** A fetched message is stored **as-synced**: the exact upstream bytes are written to the blob store and a `messages` row is inserted into the mapped mailbox via the same low-level store append that IMAP APPEND uses (`InsertMessage`). The mirror does **not** invoke spam classification, Sieve, attachment policy, mail-arrival webhooks, or import-time external-image internalization. FTS indexing happens automatically off the store change feed (as for any inserted message). LLM categorisation (`$category-*`) runs only for messages mapped into INBOX and only on newly-arrived mail, mirroring the live path's INBOX-only / not-classified-spam gating — it is NOT run across the historical backfill. External-image internalization still occurs on demand at view time via the existing on-demand path; it is not forced at import so the stored bytes stay byte-identical to upstream. |
| REQ-IMAP-IMP-32 | The `Received:` header chain is preserved verbatim from upstream. The worker does NOT prepend a synthetic `Received:` header, because rewriting the message would change the stored bytes and could invalidate the upstream's DKIM signature (decision 1 requires byte-fidelity). The IMAP-import provenance is recorded out-of-band in the per-message import-state row (account_id, upstream folder, upstream UID) and surfaced in the message-inspect view, not by mutating the message. |
| REQ-IMAP-IMP-33 | Authentication-Results: the upstream's verdict is preserved verbatim. The worker does NOT re-run DKIM verification on top of the upstream's verdict — and because the body is stored unmodified, the upstream's signatures remain independently verifiable by any client that wants to check them. |
| REQ-IMAP-IMP-34 | UID continuity: the worker stores the upstream UID per message in an `imapimport_message_state(account_id, upstream_folder, upstream_uid, herold_message_id, herold_mailbox_id, last_synced_flags)` table so flag-write-back can address upstream messages. The herold-side message_id is independent and stable across upstream UID changes (e.g. UIDVALIDITY rollover). Per-folder cursors (UIDVALIDITY, UIDNEXT, low-water mark, high-water mark, HIGHESTMODSEQ) live in `imapimport_folder_cursor`. |
| REQ-IMAP-IMP-35 | UIDVALIDITY rollover on an upstream mailbox triggers a forced re-sync of that mailbox: previous UID mapping is invalidated; the worker re-fetches the mailbox (from the backfill-horizon floor, not from UID 1 — REQ-IMAP-IMP-17 still bounds it) and reconciles by Message-ID against already-stored messages so no duplicates are created. |

## Bidirectional flag sync (upstream-authoritative)

*(Decision 5. Write-back is best-effort; the upstream is the source of
truth. There is no hybrid-logical-clock; the original REQ-IMAP-IMP-42
HLC borrow from 18-partial-replica.md is withdrawn.)*

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-40 | The worker syncs `\Seen` and `\Flagged`. A herold-side flag change (observed on the store change feed as an `EntityKindEmail` update) is pushed to the upstream message via IMAP STORE promptly and best-effort. An upstream flag change (observed via IDLE / CONDSTORE / poll) is applied to the herold-side message. |
| REQ-IMAP-IMP-41 | Custom keywords (`$category-foo`, `$snoozed`, etc.) are stored locally on herold only. They are NOT replicated upstream. (Some upstreams accept arbitrary keywords; gmail's labels are something else; the safe default is "don't write custom keywords upstream.") |
| REQ-IMAP-IMP-42 | **(Decision 5 — supersedes the prior HLC wording.)** Conflict resolution is **upstream-authoritative**. The worker keeps `last_synced_flags` per message (REQ-IMAP-IMP-34). Each reconcile compares three states: herold-now, upstream-now, last-synced. If only herold changed, push to upstream. If only upstream changed, apply to herold. If **both** changed since the last sync (a genuine conflict), the **upstream value wins** and the herold-side flag is overwritten to match; `last_synced_flags` is then set to the upstream value. A best-effort herold→upstream STORE that fails (connection drop, rate limit) leaves `last_synced_flags` unchanged so the push is retried on the next reconcile. |
| REQ-IMAP-IMP-43 | Move semantics: when the user moves a message between mailboxes in herold, the change replicates to upstream as an IMAP MOVE (or COPY+EXPUNGE on servers without MOVE — RFC 6851), best-effort. On conflict (the message also moved upstream) the upstream location wins and herold's membership is reconciled to it on the next pass. |
| REQ-IMAP-IMP-44 | Delete semantics: a herold-side `Email/set destroy` removes the message from herold AND issues a best-effort IMAP STORE +FLAGS `\Deleted` + EXPUNGE upstream. Operators / users can opt into a "delete-locally-only" mode (`delete_propagates = false`) for users who use herold to declutter without removing upstream history. Because the design is upstream-authoritative, a destroy whose upstream EXPUNGE failed to land will see the message re-mirrored on a later pass (it still exists upstream); this is the documented best-effort consequence, surfaced in the worker log. |
| REQ-IMAP-IMP-45 | Snooze, reactions, and other herold-specific datatypes are local-only and never propagate upstream. |

## Complete migration (cutover)

*(Decision 8. The issue's "important use case": a user trials herold in
client mode, then completely migrates off the upstream — bulk-importing
the complete mailbox and preserving the state they built up in herold
during the trial. The perpetual-mirror design above does not, by itself,
model the terminal state where the upstream is retired and herold becomes
the source of truth. This section specifies that cutover. Contrast
16-import.md: that importer is for accounts reachable only as an offline
Takeout archive; this is the path when the upstream is still reachable
over live IMAP. Both share the Message-ID / blob_hash dedup, so a user
MAY combine them, e.g. Takeout for the deep archive plus a live-IMAP
cutover for everything since.)*

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-90 | A principal MAY request **complete migration** of an upstream account. The account transitions `enabled` -> `migrating` -> `migrated`. `migrated` is a terminal state in which herold is authoritative for the mirrored mail and no further upstream I/O occurs. The transition is exposed on the same surfaces as the other state mutations (JMAP `IMAPImport/set`, admin REST `PATCH`). |
| REQ-IMAP-IMP-91 | Entering `migrating` forces the account's backfill floor to `all` (no floor) and runs a **one-shot complete backfill** of every mapped folder: the worker re-issues the bounded re-scan of REQ-IMAP-IMP-19 down to the earliest upstream UID so the entire mailbox — not just mail above the prior horizon — is mirrored before cutover. Progress is observable via `herold_imapimport_backfill_remaining` draining to zero and the live snapshot's `migrating` phase. |
| REQ-IMAP-IMP-92 | **State preservation (the load-bearing clause for the important use case).** All herold-side state accumulated during the trial is preserved verbatim across cutover: `\Seen` / `\Flagged`, custom keywords (`$category-*`, `$snoozed`), mailbox / label memberships, snooze and reaction datatypes. Once `migrating` begins, the reconcile MUST NOT apply the upstream-authoritative overwrite of REQ-IMAP-IMP-42 to herold-side values — authority has transferred to herold. The final backfill only **adds** not-yet-mirrored messages (deduped by Message-ID / blob_hash, REQ-IMAP-IMP-30/35); it never rewrites the flags or memberships of already-mirrored mail. |
| REQ-IMAP-IMP-93 | On reaching `migrated` the worker stops cleanly: IDLE / poll loops end, the change-feed write-back driver detaches, and both upstream connections close. The `imapimport_account` row, folder cursors, and `imapimport_message_state` are retained for provenance (surfaced in the message-inspect view) but drive no further upstream traffic. |
| REQ-IMAP-IMP-94 | Cutover is **resumable and idempotent.** A herold restart during `migrating` resumes the complete backfill from the persisted cursors (REQ-IMAP-IMP-74); re-running the complete backfill never duplicates (dedup) and never regresses herold-side state (REQ-IMAP-IMP-92). |
| REQ-IMAP-IMP-95 | A `migrated` account MAY be **re-opened** to mirroring by an explicit user action, returning it to `enabled` and re-asserting upstream-authoritative conflict handling (REQ-IMAP-IMP-42) from that point. This covers the user who cut over prematurely. Re-opening does not re-fetch already-mirrored mail (cursors persist). |
| REQ-IMAP-IMP-96 | Deleting (`DELETE`) a `migrated` account follows the keep-or-purge choice of REQ-IMAP-IMP-102, defaulting to **keep**: the mirrored mail stays in the principal's mailboxes (it is now herold-native) and only the upstream-account configuration, cursors, and sealed credential are removed. Keeping is the expected end state once a migration is confirmed good, and is distinct from disabling. |

## Provenance label and account removal

*(Added 2026-06-27 at the maintainer's request. Two coupled needs: a
removed account must let the user decide whether the mail it brought in
goes with it, and that is only tractable if imported mail is **labelled
by the channel it arrived through**. Today provenance lives only in the
out-of-band `imapimport_message_state` row — not visible to the user and
not a label they can browse or bulk-act on. This section makes the import
channel a first-class, user-visible label and defines the removal flow.)*

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-100 | Every message ingested by an account is tagged with a per-account **provenance label** — an additional herold mailbox membership added at ingest, alongside the folder-mapped mailbox(es). The label makes the import channel / identity visible and browsable in the suite and is the handle for the bulk-removal of REQ-IMAP-IMP-102. It is applied to backfilled and newly-arrived mail alike, and to a message placed in K folders (the provenance label is one membership on top of the K). It does not replace folder mapping or the `imapimport_message_state` provenance row. |
| REQ-IMAP-IMP-101 | The provenance label is a normal herold mailbox (JMAP `Mailbox`, no special `role`) created when the account is first enabled and named from `account_name` (e.g. "Gmail (work)"). Renaming the account renames the label. Implementations MAY nest these under a parent "Imported" label. A message imported by two accounts carries both provenance labels. |
| REQ-IMAP-IMP-102 | Removing an account (JMAP `IMAPImport/set destroy` / admin `DELETE`) carries a `delete_imported_mail` boolean, default **false**. The web SPA MUST present this as an explicit choice at removal time (REQ-SET-IMAPIMP-04): **keep** the imported mail (default) or **also delete** the mail imported through this account, shown with the message count. The flag is local-only: it never propagates `\Deleted`/EXPUNGE upstream (upstream deletion while the account is live is governed by REQ-IMAP-IMP-44; removal is a herold-side operation). |
| REQ-IMAP-IMP-103 | **Purge semantics (dedup-safe).** With `delete_imported_mail = true`, for each message tracked in this account's `imapimport_message_state`: if the account's provenance label is the message's **only** non-system mailbox membership and no other source claims it (no other import account's `message_state`, no native-delivery membership), the message is destroyed (`Email/set destroy` equivalent, blob refcount decremented, FTS removed via the change feed). Otherwise only this account's provenance-label membership and its `message_state` rows are removed and the message itself survives (it is also present from another channel). This protects mail that arrived both via this account and via SMTP, Takeout, or a second import account. |
| REQ-IMAP-IMP-104 | **Keep semantics.** With `delete_imported_mail = false` the imported mail stays in place. The provenance label is retained as an ordinary user label (the account is gone, but the user can still browse / rename / delete the label themselves). The account configuration, sealed credential, folder cursors, and `imapimport_message_state` rows are removed. |
| REQ-IMAP-IMP-105 | The removal flow (both keep and purge) is **idempotent and crash-safe**: it is performed in the store transaction that deletes the account, or as a resumable post-delete sweep keyed on the (now-detached) provenance label, so a herold restart mid-purge completes the purge rather than orphaning half-deleted mail. |

## Operator surface

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-60 | Per-principal admin REST: `GET /api/v1/principals/<id>/imap-imports`, `POST` to add, `PATCH` to edit, `DELETE` to remove. The body of an add lists every non-secret config field from REQ-IMAP-IMP-02 plus the credential as a write-only field that is sealed server-side before persistence (never returned on read). |
| REQ-IMAP-IMP-61 | The principal's own JMAP surface exposes `IMAPImport/get` and `IMAPImport/set` so a user-driven UI can manage upstream accounts, including entering the credential (write-only; sealed server-side). The capability advertises under `https://netzhansa.com/jmap/imap-import`. |
| REQ-IMAP-IMP-62 | A `herold imapimport status` admin command summarises every active worker: account_id, principal, upstream host, backfill floor, last-fetch timestamp, messages-fetched-today, low/high-water marks per folder, last-error. |
| REQ-IMAP-IMP-63 | Per-account Prometheus metrics: `herold_imapimport_messages_fetched_total{account}`, `herold_imapimport_flags_propagated_total{account,direction}`, `herold_imapimport_conflicts_total{account,kind}`, `herold_imapimport_idle_seconds{account}` (gauge), `herold_imapimport_fetch_duration_seconds{account}` (histogram), `herold_imapimport_connection_errors_total{account,kind}`, `herold_imapimport_backfill_remaining{account}` (gauge, UIDs left below the high-water mark). |
| REQ-IMAP-IMP-64 | Logs name `account_id` and `principal_id` on every line; activity = `imap-import`. |
| REQ-IMAP-IMP-65 | The worker pool MUST expose a **live, point-in-time status snapshot** of every account worker — not just the persisted coarse `state` and the Prometheus counters, but what each worker is doing *right now*: live phase (`starting` / `connecting` / `backoff` / `syncing` / `idle` / `polling` / `writeback` / `stopped` / `errored`), the folder currently being synced (if any), connection mode (`single` / `dual`), connected flag, time the current phase began, last successful sync time, consecutive-failure count, next scheduled poll (when polling), cumulative messages fetched and flags propagated this run, and the last error. The snapshot is served from an in-memory `Pool.Snapshot()` (no DB round-trip on the read path) and is the substrate consumed by the `herold imapimport status` CLI (REQ-IMAP-IMP-62) and a read-only admin endpoint `GET /api/v1/imap-imports/status`. Snapshot reads are concurrency-safe and never block a worker. (Added 2026-06-07 at the maintainer's request: "the account workers need to be observable.") |

## Security and operational concerns

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-70 | **(Decision 2.)** Upstream credentials are sealed at rest with the `internal/secrets` data key (`[server.secrets].data_key_ref`) using `secrets.Seal`, stored only in the `credential_ct` column, and validated to carry the `v1:` ciphertext prefix before insert (mirroring `ValidateIdentitySubmissionCTs`). Plaintext credentials are rejected at the store layer. The worker calls `secrets.Open` to obtain the live credential immediately before connect and never holds it longer than a connection attempt. Self-service users may enter their own credentials (through the suite / JMAP `IMAPImport/set`); the credential is sealed server-side and never echoed back on read. Operators who prefer external secret material MAY instead point the account at a secret-reference resolved at connect time, but the default and self-service path is the sealed column. |
| REQ-IMAP-IMP-71 | The worker MUST NOT log credentials or auth headers. The IMAP wire trace (when enabled at debug level) redacts AUTHENTICATE / LOGIN payloads. |
| REQ-IMAP-IMP-72 | The worker MUST NOT trust upstream-supplied bytes for SSRF-shaped lookups. (On-demand external-image internalization continues to apply per REQ-EXTIMG-02 when the user views a message; the SSRF guard there is the relevant fence. Import itself fetches no remote content beyond the IMAP connection.) |
| REQ-IMAP-IMP-73 | Upstream rate limits: when an upstream returns a "too-many-connections" or "rate-limited" error, the worker drops to a single connection and increases its backoff exponent. Repeated rate-limiting flips the account to `errored` and surfaces the last_error to the operator. |
| REQ-IMAP-IMP-74 | The worker survives a herold restart cleanly: cursors, account state, and `last_synced_flags` are persisted; on boot the worker pool reconnects every `enabled` account, falling back to forced re-sync (bounded by the horizon floor) if its persisted UIDVALIDITY no longer matches the upstream's. |

## Gmail specifics

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-50 | Gmail exposes per-message labels as IMAP folders — one folder per label. The worker uses **folder-based label placement** (Option B): INBOX, [Gmail]/Sent Mail, [Gmail]/Drafts, [Gmail]/Spam, [Gmail]/Trash, and every user-label folder are each synced into a mapped herold mailbox. A message present in K label folders lands in K herold mailbox memberships via the multi-mailbox-on-dedup mechanism (REQ-IMAP-IMP-51). Virtual/flag folders ([Gmail]/Important, [Gmail]/Starred, [Gmail]/Chats) are skipped — they contain no unique content and no body is fetched from them. [Gmail]/All Mail is synced LAST using envelope-first dedup (REQ-IMAP-IMP-50b) to capture archived/unlabeled mail without re-downloading bodies already fetched from label folders. The backfill horizon (REQ-IMAP-IMP-17) applies to all folders. |
| REQ-IMAP-IMP-50b | **Envelope-first dedup for [Gmail]/All Mail (archived mail capture).** After all label folders are synced, the worker syncs [Gmail]/All Mail as a catch-all. For each UID in the horizon, it fetches ENVELOPE + UID + INTERNALDATE + FLAGS (no body). If the Message-ID is already mirrored in herold (via GetMessageByMessageIDHeader), the body download is skipped entirely. Only for messages NOT yet mirrored (archived mail with no label folder), the worker body-fetches (BODY.PEEK[]) and ingests into the "Archive" herold mailbox. This avoids re-downloading bodies already placed by label folders while still capturing archived/unlabeled mail. |
| REQ-IMAP-IMP-51 | **Multi-mailbox-on-dedup.** When a message (identified by Message-ID) is already in herold but the current sync folder maps to a different herold mailbox, the worker adds the additional membership via `AddMessageToMailbox` instead of treating it as a no-op. This is the foundation of folder-based label placement: a message in K upstream folders gets K herold mailbox memberships. The mechanism is general — it applies to ALL IMAP accounts, not just Gmail. Re-fetching the same folder is idempotent (the membership already exists; `AddMessageToMailbox` returns `ErrConflict` which is caught and ignored). Gmail's per-message labels are NOT accessed: `X-Gmail-Labels:` is a Takeout/Vault export artifact absent from IMAP FETCH responses, and `X-GM-LABELS` (X-GM-EXT-1 fetch item) cannot be fetched by the pinned `emersion/go-imap/v2 v2.0.0-beta.8` client (its FETCH parser rejects unknown msg-att names). Folder-based placement is the interim approach pending the X-GM-LABELS client upgrade (REQ-IMAP-IMP-53) and is tested end-to-end against the in-process memory server. A best-effort `X-Gmail-Labels:` header parser is retained for sources (e.g. Takeout-style providers) that do include the header. |
| REQ-IMAP-IMP-52 | **(Decision 3.)** For gmail, `auth_method = app_password` remains viable (app-passwords still work for accounts with 2-step verification; only the legacy "less secure app access" toggle was removed). `auth_method = xoauth2` is the cleaner long-term path but requires the operator to register a Google Cloud OAuth application with the restricted `https://mail.google.com/` scope and pass Google's verification (CASA security assessment) — a real operator burden, configured under `[imap_import.oauth.google]`. herold does NOT obtain this scope from its login-OIDC federation (NG11). The suite surfaces both options with the operator burden documented. |
| REQ-IMAP-IMP-53 | **(Decision 9 — planned client upgrade.)** True per-message Gmail label placement SHALL be implemented by fetching Gmail's `X-GM-LABELS` (the X-GM-EXT-1 FETCH data item), which requires upgrading or locally patching `emersion/go-imap/v2` beyond the pinned `v2.0.0-beta.8` whose FETCH parser rejects the unknown msg-att. When `X-GM-LABELS` is available the worker derives each message's herold mailbox memberships directly from its label set, superseding the folder-based interim placement of REQ-IMAP-IMP-50/51 for Gmail and removing the need to sync per-label folders and to body-fetch `[Gmail]/All Mail` separately. Folder-based placement (REQ-IMAP-IMP-51) is retained as the fallback for servers that do not advertise X-GM-EXT-1. The pin bump is reviewed by `imap-implementor` + `conformance-fuzz-engineer` (wire-surface change). |

## Out of scope (deferred / future iterations)

- **REQ-IMAP-IMP-80** — POP3 import. The lossy-deletion semantics make
  it materially harder to integrate idempotently. Defer.
- **REQ-IMAP-IMP-81** — Bidirectional sync of custom keywords. Some
  upstreams (gmail labels, dovecot keywords) could carry them; v1
  only ships `\Seen` / `\Flagged`.
- **REQ-IMAP-IMP-82** — Outbound-via-upstream hybrid (use the upstream's
  submission server for sending). Today, herold's identity /
  smarthost path is the only way to send.
- **REQ-IMAP-IMP-83** — JMAP-on-JMAP source (the upstream is itself
  a JMAP server). The protocols differ enough to be a distinct
  worker; deferred.
- **REQ-IMAP-IMP-84** — A "pause for the night" scheduling layer
  for operators who want to reduce upstream-API load during sleep
  hours.
- **REQ-IMAP-IMP-85** — Rolling-retention mode (age mirrored mail out
  of herold past a window). Deliberately NOT this feature; see
  18-partial-replica.md for the edge/home cache-window design if a
  user actually wants eviction.
- **REQ-IMAP-IMP-86** — Re-running Sieve over imported mail as an
  opt-in per account. Decision 1 stores as-synced; a future iteration
  could offer "run my Sieve over newly-imported INBOX mail" once the
  fileinto-vs-mapping reconciliation is designed.
