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

## Scope

A herold principal can configure one or more **upstream IMAP
accounts**. A per-account worker maintains a long-lived authenticated
IMAP connection, observes new-mail notifications via IDLE, fetches
new messages, runs them through the herold inbound pipeline (spam,
sieve, webhooks, attachment policy), and stores them in the
principal's mailboxes. Flag changes the user makes in herold (mark
read, star) propagate back upstream via IMAP STORE. Folder
mappings are configurable per account.

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

## Motivating user flows

1. **Gmail user moving to herold.** Keeps gmail's MX and spam
   filtering, herold becomes the daily MUA. Folders mapped:
   gmail INBOX → herold INBOX, gmail All Mail → herold All Mail,
   etc. Gmail Takeout (file 16-import.md) gets the historical
   archive in once; live IMAP import takes over from there.
2. **Multi-provider consolidation.** A user has work + personal +
   alias accounts. Each is an upstream IMAP source; all mirror into
   one herold principal's mailboxes. Sent mail still goes via
   herold's identity / smarthost surface; the upstream accounts are
   one-way feeds for inbound and two-way for `\Seen`/`\Flagged`.
3. **Archive-from-elsewhere.** A user's old hosted mail provider
   doesn't have a takeout export. They configure an upstream IMAP
   pointing at it; herold mirrors the entire account on first
   connect, then keeps following.

## Configuration

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-01 | Each principal MAY configure zero-or-more upstream IMAP accounts. The configuration is stored per-principal in the metadata store (not in `system.toml`); a JMAP `IMAPImport/set` surface mutates it. |
| REQ-IMAP-IMP-02 | Each upstream-account record carries: `id`, `account_name` (operator-visible label), `host`, `port`, `tls_mode` (`starttls` / `implicit` / `none`), `username`, `auth_method` (`plain`, `login`, `app_password`, `xoauth2`), `auth_secret_ref` (secret reference per the operator's secrets config), `state` (`enabled` / `disabled` / `errored`), `last_success_at`, `last_error`. |
| REQ-IMAP-IMP-03 | `auth_method = xoauth2` is the OAuth path used by gmail and microsoft. The OAuth token is refreshed via the operator's existing OIDC provider plumbing (REQ-OIDC-*); the upstream-account record stores a refresh-token reference, the worker exchanges it for an access token before each connect. |
| REQ-IMAP-IMP-04 | `auth_method = app_password` and `auth_method = plain` both write a static credential to the secrets store; the difference is the upstream's intended audience. App-passwords are the apple / fastmail flow; plain is the cpanel / bare-IMAP flow. The IMAP layer treats them identically. |
| REQ-IMAP-IMP-05 | Operators MAY block plain-text auth with an explicit `[imap_import] allow_plaintext = false` config knob (default true so existing setups keep working; future iterations should flip the default). |
| REQ-IMAP-IMP-06 | The connection MUST use TLS. `tls_mode = none` is rejected at config-time. STARTTLS that fails to negotiate is treated as a connection error. (Operators with internal-only legacy IMAP servers can VPN to them.) |

## Folder mapping

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-10 | Each upstream-account record carries a folder mapping table: `(upstream_folder_name, herold_mailbox_name)` pairs. Default mapping when absent: name-equals-name. The default produces gmail's `Inbox` ↔ herold's `INBOX`, `Sent Mail` ↔ `Sent Mail` (note the case mismatch is preserved verbatim — herold is case-insensitive). |
| REQ-IMAP-IMP-11 | Operators MAY supply a system-wide default mapping table per upstream-host pattern (e.g. "imap.gmail.com → these mappings"). Per-account overrides win. |
| REQ-IMAP-IMP-12 | Folders that exist upstream but have no mapping (and where the default name-equals-name lookup misses) are created in herold under the upstream name. The user can rename via the suite. |
| REQ-IMAP-IMP-13 | Folders that exist in herold but not upstream are unaffected by import — they hold whatever herold puts there (other-account imports, locally-created drafts). |
| REQ-IMAP-IMP-14 | Gmail's `[Gmail]/All Mail` is treated as a special case (REQ-IMAP-IMP-50 cross-references the gmail-specific behaviour) — its messages ARE indexed but not duplicated into a separate mailbox; instead each message's primary mailbox is determined by its `X-Gmail-Labels` exactly as the takeout importer does. |

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
| REQ-IMAP-IMP-30 | Each upstream message is persisted in herold with the canonical `Message-ID` header preserved. The worker dedupes against the principal's existing `env_message_id` index; a message that already exists in herold (via prior takeout, prior IMAP-import session, or any other source) is not duplicated. |
| REQ-IMAP-IMP-31 | When a message is fetched from upstream, the bytes are passed through the **same** SMTP-inbound pipeline used for direct mail delivery: spam classification (REQ-FLOW-SPAM-*), Sieve (REQ-PROTO-50..53), inbound attachment policy (REQ-FLOW-ATTPOL-01), webhook dispatch (REQ-HOOK-02), DKIM verification, external-image internalization (REQ-EXTIMG-02). The fetched message is stored exactly as if it had arrived over SMTP. |
| REQ-IMAP-IMP-32 | The `Received:` header chain is preserved verbatim from upstream, plus the worker prepends one additional `Received: from <upstream-host> via imap-import; <date>` so the audit trail records the IMAP-import path. |
| REQ-IMAP-IMP-33 | Authentication-Results: the upstream's verdict is preserved. The worker does NOT re-run DKIM verification on top of the upstream's verdict — DKIM signatures over the body are likely still valid (we have not modified the body) but the upstream-recorded result is the authoritative one for this hop. (Note: if the upstream rewrote the body, the signature would be invalid; see REQ-EXTIMG-46 for the parallel discussion of body modification stripping signatures.) |
| REQ-IMAP-IMP-34 | UID continuity: the worker stores the upstream UID per message in a `imapimport_message_state(account_id, upstream_uid, herold_message_id, herold_modseq)` table so flag-write-back can address upstream messages. The herold-side message_id is independent and stable across upstream UID changes (e.g. UIDVALIDITY rollover). |
| REQ-IMAP-IMP-35 | UIDVALIDITY rollover on an upstream mailbox triggers a forced re-sync of that mailbox: previous UID mapping is invalidated; the worker re-fetches the entire mailbox and reconciles by Message-ID. |

## Bidirectional flag sync

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-40 | The worker syncs `\Seen` and `\Flagged` bidirectionally. A herold-side `MessageFlagSeen` change replicates to the upstream message via IMAP STORE; an upstream STORE notification replicates back to herold. |
| REQ-IMAP-IMP-41 | Custom keywords (`$category-foo`, `$snoozed`, etc.) are stored locally on herold only. They are NOT replicated upstream. (Some upstreams accept arbitrary keywords; gmail's labels are something else; the safe default is "don't write custom keywords upstream.") |
| REQ-IMAP-IMP-42 | Conflict resolution: bidirectional flag sync uses a "last-writer-wins by HLC" approach mirroring REQ-REPL-40 (see 18-partial-replica.md). When the user toggles a flag in herold while the upstream simultaneously toggles via another client, the later-HLC value wins. |
| REQ-IMAP-IMP-43 | Move semantics: when the user moves a message between mailboxes in herold, the change replicates to upstream as an IMAP MOVE (or COPY+EXPUNGE on servers without MOVE — RFC 6851). |
| REQ-IMAP-IMP-44 | Delete semantics: a herold-side `Email/set destroy` removes the message from herold AND issues an IMAP STORE +FLAGS \Deleted + EXPUNGE upstream. Operators can opt into a "delete-locally-only" mode (`delete_propagates = false`) for users who use herold to declutter without removing upstream history. |
| REQ-IMAP-IMP-45 | Snooze, reactions, and other herold-specific datatypes are local-only and never propagate upstream. |

## Operator surface

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-60 | Per-principal admin REST: `GET /api/v1/principals/<id>/imap-imports`, `POST` to add, `DELETE` to remove. The body of an add lists every config field from REQ-IMAP-IMP-02 except secrets, which use the standard secret-reference syntax. |
| REQ-IMAP-IMP-61 | The principal's own JMAP surface exposes `IMAPImport/get` and `IMAPImport/set` so a user-driven UI can manage upstream accounts. The capability advertises under `https://netzhansa.com/jmap/imap-import`. |
| REQ-IMAP-IMP-62 | A `herold imapimport status` admin command summarises every active worker: account_id, principal, upstream host, last-fetch timestamp, messages-fetched-today, last-error. |
| REQ-IMAP-IMP-63 | Per-account Prometheus metrics: `imapimport_messages_fetched_total{account}`, `imapimport_flags_propagated_total{account,direction}`, `imapimport_idle_seconds{account}` (gauge), `imapimport_fetch_duration_seconds{account}` (histogram), `imapimport_connection_errors_total{account,kind}`. |
| REQ-IMAP-IMP-64 | Logs name `account_id` and `principal_id` on every line; activity = `imap-import`. |

## Security and operational concerns

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-70 | Upstream credentials live in the operator's secrets store via secret reference (`$IMAP_GMAIL_OAUTH`, `file:/run/secrets/...`). Inline credentials are rejected. |
| REQ-IMAP-IMP-71 | The worker MUST NOT log credentials or auth headers. The IMAP wire trace (when enabled at debug level) redacts AUTHENTICATE / LOGIN payloads. |
| REQ-IMAP-IMP-72 | The worker MUST NOT trust upstream-supplied bytes for SSRF-shaped lookups. (External-image internalization continues to apply per REQ-EXTIMG-02; the SSRF guard there is the relevant fence.) |
| REQ-IMAP-IMP-73 | Upstream rate limits: when an upstream returns a "too-many-connections" or "rate-limited" error, the worker drops to a single connection and increases its backoff exponent. Repeated rate-limiting flips the account to `errored` and surfaces the last_error to the operator. |
| REQ-IMAP-IMP-74 | The worker survives a herold restart cleanly: state is persisted (cursors, account state); on boot the worker pool reconnects every `enabled` account, falling back to forced re-sync if its persisted UIDVALIDITY no longer matches the upstream's. |

## Gmail specifics

| ID | Requirement |
|----|-------------|
| REQ-IMAP-IMP-50 | Gmail's `[Gmail]/All Mail` contains every message the account ever received, with `X-Gmail-Labels:` indicating the user's chosen labels. The worker treats All Mail as the canonical source of message bytes; visited per-label folders (`Inbox`, `Important`, etc.) are skipped on the assumption every message there is also in All Mail. This avoids fetching the same body N times for messages tagged into many labels. |
| REQ-IMAP-IMP-51 | Mailbox mapping for gmail uses the same locale-aware label table as the takeout importer (REQ-IMPORT-10..14). Gmail's UI labels are localised; the IMAP folder names are NOT (gmail always serves IMAP folder names in English: `Inbox`, `Sent Mail`, etc.). The worker does not need locale detection for the IMAP path; the labels inside `X-Gmail-Labels:` are localised so the takeout label-translation table applies there. |
| REQ-IMAP-IMP-52 | `auth_method = xoauth2` MUST be used for gmail. App passwords stopped working in 2024. The OAuth refresh-token storage uses the operator's existing OIDC plumbing. |

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
