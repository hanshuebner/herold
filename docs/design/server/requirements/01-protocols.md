# 01 — Protocols

Every wire protocol the server speaks, with the RFCs that define correctness. Anything not listed here is out of scope unless promoted by edit.

## In scope (v1)

| Area | Protocol | Core RFCs | Notes |
|---|---|---|---|
| MTA | SMTP (submission + relay) | 5321, 5322 | With ESMTP extensions below |
| Mailbox access | IMAP4rev2 | 9051 | rev2 preferred; rev1 (3501) as fallback for old clients |
| Modern mailbox access | JMAP Core + Mail | 8620, 8621 | Primary API for new clients |
| Managed Sieve | ManageSieve | 5804 | Script upload/validate from clients |
| Sieve | Sieve | 5228 + extensions (below) | Filter execution at delivery |

## Deferred / optional

| Protocol | RFC | Decision |
|---|---|---|
| POP3 | 1939, 2449, 5034 | Not in v1. Reconsider phase 3. |
| LMTP | 2033 | Not needed for a single-binary design; our delivery is in-process. Could be added as an ingress option later. |
| NNTP | 3977 | No. |
| Submission-over-HTTP (JMAP submission) | 8621 §7 | Yes — it's part of JMAP Mail. |

## SMTP (REQ-PROTO-SMTP)

### Listeners

- **REQ-PROTO-01** MUST listen on **25/tcp** (relay in), **465/tcp** (implicit TLS submission), **587/tcp** (STARTTLS submission). Each listener bindable independently.
- **REQ-PROTO-02** MUST support IPv4 and IPv6 on every listener.
- **REQ-PROTO-03** MUST support PROXY protocol v1 and v2 (opt-in per listener) for operation behind L4 load balancers.

### ESMTP extensions to support

| Extension | RFC | Direction |
|---|---|---|
| STARTTLS | 3207 | in + out |
| AUTH (SASL) | 4954 | submission in, relay out |
| SIZE | 1870 | in + out |
| PIPELINING | 2920 | in + out |
| 8BITMIME | 6152 | in + out |
| SMTPUTF8 | 6531 | in + out |
| CHUNKING / BDAT | 3030 | in + out |
| DSN (NOTIFY/RET/ENVID/ORCPT) | 3461 | in + out |
| ENHANCEDSTATUSCODES | 2034 | in + out |
| BURL | 4468 | deferred |
| MT-PRIORITY | 6710 | deferred |
| REQUIRETLS | 8689 | **yes** |
| Future/experimental (e.g. LIMITS, MAIL-ADDRESS-TAGS) | — | skip |

- **REQ-PROTO-04** MUST advertise only the extensions actually implemented. No stubs.
- **REQ-PROTO-05** MUST support SMTPUTF8 end-to-end (mailbox names, headers, envelope) per RFC 6531.
- **REQ-PROTO-06** MUST enforce max message size per listener (SIZE advertised value), rejecting RCPT with appropriate 552 before DATA when declared SIZE exceeds limit.
- **REQ-PROTO-07** MUST emit RFC 3461 DSN on delivery failure for relays that requested NOTIFY.
- **REQ-PROTO-08** MUST support both command-line and BDAT ingestion; DATA and BDAT paths share the same message parser.

### SASL mechanisms (for submission)

- **REQ-PROTO-09** MUST support `PLAIN` and `LOGIN` (over TLS only).
- **REQ-PROTO-10** MUST support `SCRAM-SHA-256` and `SCRAM-SHA-256-PLUS`.
- **REQ-PROTO-11** MUST support `OAUTHBEARER` (RFC 7628) and `XOAUTH2` (Google/Microsoft compat).
- **REQ-PROTO-12** MUST reject plain-text mechanisms outside of TLS (STARTTLS-accepted or implicit TLS).

### Rate limiting and abuse controls

- **REQ-PROTO-13** MUST support per-IP connection rate limiting, per-IP concurrent connection limits, per-session command rate limiting, per-account submission rate limits.
- **REQ-PROTO-14** MUST support greylisting (optional per-domain/per-IP) and tarpitting on abuse signals.
- **REQ-PROTO-15** MUST support RBL lookups at CONNECT time for relay-in; with configurable action (reject / defer / tag).

## IMAP (REQ-PROTO-IMAP)

### Listeners

- **REQ-PROTO-20** MUST listen on **993/tcp** (implicit TLS) and **143/tcp** (STARTTLS). 143 may be disabled by config.

### Capabilities to advertise and implement

Baseline (IMAP4rev2 / rev1 interop):
- `IMAP4rev2`, `IMAP4rev1`
- `STARTTLS`, `AUTH=…` (same mechanisms as SMTP submission)
- `IDLE` (2177)
- `LIST-EXTENDED`, `LIST-STATUS`, `SPECIAL-USE`, `CREATE-SPECIAL-USE` (5258, 5819, 6154)
- `ENABLE` (5161), `UTF8=ACCEPT` (6855), `LITERAL+` (7888)
- `UIDPLUS` (4315), `ESEARCH` (4731), `SEARCHRES` (5182), `SORT` (5256), `THREAD` (5256)
- `CONDSTORE` (7162), `QRESYNC` (7162)
- `MOVE` (6851), `UNSELECT` (3691), `NAMESPACE` (2342)
- `ACL` (4314) — for shared mailboxes (phase 2; see REQ-PROTO-33)
- `QUOTA`, `QUOTA=RES-STORAGE` (9208)
- `BINARY` (3516), `CATENATE` (4469), `MULTIAPPEND` (3502)
- `COMPRESS=DEFLATE` (4978)
- `ID` (2971) — advertise with server name/version
- `METADATA` (5464), `METADATA-SERVER` — for JMAP interop
- `OBJECTID` (8474) — threading / JMAP interop
- `WEBPUSH` (not standardized yet) — skip
- `NOTIFY` (5465)

Deferred: `LIST-MYRIGHTS`, `CONTEXT=SEARCH`, `URLAUTH`, `PREVIEW` (RFC 9051 §6.4.5 FETCH `PREVIEW` data item — herold advertises `IMAP4rev2` but does not yet generate previews; until implemented, FETCH `PREVIEW` returns `BAD`. Surfacing the gap is the imaptest `imap4rev2=1` profile in `test/interop/`).

- **REQ-PROTO-30** MUST pass the `imapserver-tests` public interop matrix for all listed capabilities.
- **REQ-PROTO-31** MUST handle `IDLE` for ≥2,000 concurrent sessions without per-session threads.
- **REQ-PROTO-32** MUST implement `CONDSTORE`/`QRESYNC` correctly — this is a hard requirement for modern clients (Apple Mail) and tricky to get right. See architecture/05-sync-and-state.md.
- **REQ-PROTO-33** Shared mailboxes and IMAP `ACL` (RFC 4314: `SETACL` / `GETACL` / `MYRIGHTS` / `LISTRIGHTS`) are **in scope for phase 2** (pre-v1.0). Schema carries per-mailbox ACL entries; SELECT / STATUS / fanout respect them; JMAP sharing surface aligned.
- **REQ-PROTO-34** MUST implement IMAP `NOTIFY` (RFC 5465). Clients subscribe to state-change events across mailboxes without holding a SELECT + IDLE per mailbox, which is how modern clients stay efficient across large folder sets. NOTIFY draws from the same per-principal change feed that backs IDLE and JMAP push — one event source, three consumers. Phase 2 alongside CONDSTORE/QRESYNC and MOVE.

## JMAP (REQ-PROTO-JMAP)

- **REQ-PROTO-40** MUST implement JMAP Core (RFC 8620): session, request/response envelope, error model, push.
- **REQ-PROTO-41** MUST implement JMAP Mail (RFC 8621): `Mailbox`, `Email`, `EmailSubmission`, `Identity`, `Thread`, `SearchSnippet`, `VacationResponse`. Upload/download endpoints.
- **REQ-PROTO-42** MUST implement `EmailSubmission` tied to the outbound SMTP queue (not a parallel submission path). Includes `sendAt` per RFC 8621 §7.5; see REQ-PROTO-58.
- **REQ-PROTO-43** MUST serve the session endpoint at `/.well-known/jmap` per RFC 8620.
- **REQ-PROTO-44** MUST support push via EventSource (SSE) at minimum; WebSocket push per RFC 8887 is optional.
- **REQ-PROTO-45** MUST NOT require a separate authentication subsystem — JMAP auth shares the identity model with IMAP/SMTP.
- **REQ-PROTO-46** `VacationResponse` object maps to a Sieve vacation rule (REQ-FILT-sieve).
- **REQ-PROTO-47** `Email/query` + `Email/get` MUST back onto the same FTS index used for IMAP `SEARCH`. One index, two query paths.
- **REQ-PROTO-48** **Web Push** (RFC 8030 + RFC 8620 §7.2 `PushSubscription` + RFC 8291 encryption + RFC 8292 VAPID auth). Advanced from "phase 3" to **phase 1** per the suite plan: the suite v1 ships browser push notifications and depends on herold delivering them. Detail in REQ-PROTO-120..127 (the JMAP datatype shape, the Suite-specific notification rules extension, the outbound push gateway, VAPID key management).
- **REQ-PROTO-49** MUST implement the JMAP snooze extension. Modern mail clients (Apple Mail, Fastmail, etc.) expose "snooze until <time>" as a first-class user action; the de-facto JMAP shape is a `$snoozed` keyword on `Email` plus a `snoozedUntil` (UTCDate) extension property, with a server-side wake-up timer that clears the keyword + the property when `snoozedUntil <= now`. Phase 2.

  Concrete contract:
  - `Email/get` exposes `keywords["$snoozed"] == true` while snoozed and `snoozedUntil: "<UTC ISO 8601>"`.
  - `Email/set` may set `snoozedUntil` (with or without the matching keyword); the server normalises so the keyword + the property are atomic — setting one sets/clears the other.
  - The store carries a nullable `snoozed_until_us BIGINT` column on the email row; an in-process scheduler tick (default every 60 s; floor 5 s, configurable) sweeps `WHERE snoozed_until_us <= now AND $snoozed`, clears the keyword + nulls the column, increments `JMAPStates.Email`, and appends a `state_changes` row so JMAP push / IMAP IDLE / NOTIFY clients see the wake.
  - The IMAP keyword `$snoozed` is reserved as a system keyword: clients can SEARCH KEYWORD `$snoozed` and observe FLAGS containing `$snoozed`; setting or clearing it via STORE keeps the JMAP `snoozedUntil` in sync via the same atomicity invariant.
  - Optional move-on-snooze: per-principal config, default off — when on, the server moves the message into a mailbox with role `\Snoozed` (creating it lazily if absent) at snooze time and back to its prior mailbox at wake. v1 ships move-off as the default; the move-on path is a Phase-3 candidate.
  - The IMAP SNOOZE extension (draft-ietf-extra-imap-snooze) is **deferred to Phase 3**. The JMAP path covers Apple Mail and Fastmail; raw-IMAP clients can still set the keyword.
  - References: draft-ietf-extra-jmap-snooze (the JMAP extension), Fastmail's published JMAP-snooze deployment notes (the de-facto reference implementation).

### JMAP — additional datatypes for the suite

These are additive over REQ-PROTO-41. Each is its own JMAP capability, registered at the session-descriptor level. Phasing per the rev-4 scope position.

- **REQ-PROTO-53** MUST implement the `urn:ietf:params:jmap:sieve` JMAP datatype (RFC 9007). Methods: `Sieve/get`, `Sieve/set`, `Sieve/validate`. Wraps the existing Sieve interpreter and script storage already required by REQ-PROTO-50..52 (ManageSieve over port 4190); the JMAP datatype is the same store with a different access path. Required by the suite's filter UI (`docs/design/web/requirements/04-filters.md`). Phase 1.
- **REQ-PROTO-54** MUST implement the `urn:ietf:params:jmap:calendars` JMAP datatype (RFC 8984 JSCalendar object model + the JMAP-Calendars binding draft). Methods: `Calendar/*`, `CalendarEvent/*` per the binding. Object model: JSCalendar. Phase 2; pin to a specific binding-draft revision when work starts. Required by the calendar app (when that app starts) and by the suite's iMIP RSVP path (`docs/design/web/requirements/15-calendar-invites.md`).
- **REQ-PROTO-55** MUST implement the `urn:ietf:params:jmap:contacts` JMAP datatype (RFC 9553 JSContact object model + the JMAP-Contacts binding draft). Methods: `AddressBook/*`, `Contact/*` per the binding. Object model: JSContact. Phase 2; pin to a specific binding-draft revision when work starts. Required by the contacts app and by the suite's recipient autocomplete (`docs/design/web/requirements/02-mail-basics.md` REQ-MAIL-11).
- **REQ-PROTO-56** MUST persist a `color` extension property on `Mailbox` (string; hex like `#5B8DEE`). Read on `Mailbox/get`; mutated on `Mailbox/set`. Used by the suite's user-defined label colours (`docs/design/web/requirements/03-labels.md` REQ-LBL-04). Phase 1.
- **REQ-PROTO-57** MUST persist a `signature` extension property on `Identity` (plain-text body; HTML signature deferred). Read on `Identity/get`; mutated on `Identity/set`. Used by the suite's per-identity signatures (`docs/design/web/requirements/02-mail-basics.md` REQ-MAIL-100..103, `docs/design/web/requirements/20-settings.md` REQ-SET-03). Phase 1.
- **REQ-PROTO-58** MUST honour `EmailSubmission.sendAt` (RFC 8621 §7.5) — when set, the outbound queue holds the submission until the indicated UTCDate and only then begins delivery. `EmailSubmission/set { destroy }` issued before `sendAt` MUST cancel delivery (the submission is removed from the queue and no message leaves). Pairs with REQ-FLOW-63. Used by the suite's send-undo (`docs/design/web/requirements/02-mail-basics.md` REQ-MAIL-14, `docs/design/web/requirements/11-optimistic-ui.md` REQ-OPT-11). Phase 1.
- **REQ-PROTO-59** MUST pass `text/calendar` MIME parts through the outbound queue without stripping or rewriting. iMIP messages (`text/calendar; method=REQUEST/CANCEL/REPLY/COUNTER/REFRESH`) are ordinary multipart/alternative emails from the queue's perspective; no special handling required, but the path must not silently drop unknown MIME types. Used by the suite's iMIP RSVP path (`docs/design/web/requirements/15-calendar-invites.md`). Phase 1.

### Email reactions extension

Per `docs/design/web/notes/server-contract.md` § Email reactions. Capability `https://herold.dev/jmap/email-reactions`. Phase 2.

- **REQ-PROTO-100** MUST persist a `reactions` extension property on `Email`. Shape: `{ "<emoji>": ["<principal-id>", ...] }`. Sparse — emojis with no current reactors are absent rather than empty arrays. Stored as a JSON column or normalised table (implementation choice; the JMAP shape is the contract).
- **REQ-PROTO-101** `Email/set` MUST accept JSON-patch paths under `reactions/`. Specifically: `reactions/<emoji>/<principal-id>: true` to add, `: null` to remove. Paths MUST validate that the requesting principal is `<principal-id>` — a user can only add or remove their own reaction. Other paths return `forbidden`.
- **REQ-PROTO-102** Reactions advance the `Email` state string per the standard JMAP rules; pushed via the EventSource channel like other Email mutations.
- **REQ-PROTO-103** Outbound emission of reactions to non-local recipients is herold's responsibility — see REQ-FLOW-100..108. The suite does not see the cross-server-vs-local-only distinction; it just calls `Email/set` and herold dispatches.

### Shortcut coach datatype

Per `docs/design/web/requirements/23-shortcut-coach.md` and `docs/design/web/notes/server-contract.md` § Shortcut coach. Capability `https://herold.dev/jmap/shortcut-coach`. Phase 2.

Backs the suite's always-on keyboard-shortcut coach: a per-principal store of (action, keyboard-vs-mouse counters in 14d / 90d sliding windows, last-used timestamps, dismiss state) used to decide which shortcut hints to surface and when.

- **REQ-PROTO-110** MUST implement the `ShortcutCoachStat` JMAP datatype. Methods: `ShortcutCoachStat/get`, `/query`, `/changes`, `/set`. Shape per the suite's server-contract — one row per `(principal, action)` pair.
- **REQ-PROTO-111** MUST roll forward windowed counters on `ShortcutCoachStat/set { update }`. The suite sends incremental patches (e.g. `+3 keyboard, +1 mouse, lastKeyboardAt: <ts>`); herold integrates them into the rolling 14d / 90d counters. Implementation choice between a per-row activity log with windowed aggregation at read time, or eagerly-decaying counters; either is acceptable as long as the read-side numbers reflect the window correctly.
- **REQ-PROTO-112** Authorisation: a principal can only `get` / `set` / `destroy` their own `ShortcutCoachStat` rows. Admin / operator reads of coach data are out — the data is private to the user (`docs/design/web/requirements/23-shortcut-coach.md` REQ-COACH-04).
- **REQ-PROTO-113** State-change-feed integration is OPTIONAL. The state string advances per the standard rules but the suite does not subscribe to changes (the client-writes-only-its-own pattern; no echoes needed). Implementations MAY skip emitting state-change feed rows for coach mutations to save broadcaster work — this is a deliberate exemption from the otherwise-uniform rule that every mutation appends to the feed.
- **REQ-PROTO-114** Storage volume is small: ~30 actions × ~1k principals = trivial. A single SQLite/Postgres table with `(principal_id, action)` PRIMARY KEY and the counter columns suffices.

### Web Push delivery

Per `docs/design/web/requirements/25-push-notifications.md` and `docs/design/web/notes/server-contract.md` § Web Push. Capability `https://herold.dev/jmap/push` (advertised when the deployment has VAPID configured). Phase 1 — herold ships push delivery as part of v1 because the suite v1 depends on it.

- **REQ-PROTO-120** MUST implement the JMAP `PushSubscription` datatype per RFC 8620 §7.2 with standard methods (`get`, `set`). Standard properties: `id`, `deviceClientId`, `url` (the push endpoint), `expires`, `types` (subscribed JMAP types), `keys: { p256dh, auth }`, plus `verificationCode` per the verification handshake.
- **REQ-PROTO-121** MUST persist suite-specific extension properties on `PushSubscription`: `notificationRules` (JSON blob; the suite's per-event-type preference set), `quietHours` (`{ startHourLocal, endHourLocal, tz }` or null), and `vapidKeyAtRegistration` (the VAPID public key the client registered against, so herold can pick the right key pair when rotating). Validation: rules must be a known JSON shape; quiet hours' tz must be a valid IANA timezone or null.
- **REQ-PROTO-122** MUST maintain a deployment-level VAPID key pair. Public key exposed in the JMAP session descriptor's capabilities data (under `urn:ietf:params:jmap:core` an extension field, OR under `https://herold.dev/jmap/push`'s capability data — pick one and document; the suite reads from wherever herold puts it). Private key stored in herold's secrets store (per REQ-OPS-160..163). Operator may rotate the key pair manually; rotation invalidates existing subscriptions on next push attempt (clients re-subscribe).
- **REQ-PROTO-123** Outbound push dispatcher: for each state-change-feed event affecting a principal, the dispatcher iterates that principal's `PushSubscription` rows. Per subscription:
  1. Evaluate `notificationRules` against the event type and content. Result: `enriched` (build a suite payload with title / body / actions per `docs/design/web/requirements/25-push-notifications.md` REQ-PUSH-40..47), `minimal` (RFC 8620 §7.2 state-change envelope only), or `suppress` (rule says no push).
  2. If `quietHours` is set and the event is in-window: downgrade `enriched` to `suppress` unless the event is high-priority (incoming video call; calendar invite for an event starting within 60 min).
  3. Build the payload (JSON), check size ≤ 2.5 KB plaintext.
  4. Encrypt per RFC 8291 using the subscription's `p256dh` and `auth` keys.
  5. Build the `Authorization` header per RFC 8292 (VAPID JWT signed with the deployment's VAPID private key; `aud` claim from the endpoint URL's origin; `sub` claim from operator config).
  6. POST the encrypted body + headers to the subscription's `url`.
  7. Outcome: 201 / 200 / 204 = success; 410 / 404 = subscription gone, destroy via `PushSubscription/set { destroy }`; 4xx other = log and retry once with exponential backoff; persistent failure logs and destroys after 3 attempts; 5xx = retry with backoff up to 5 attempts then give up (the push is lost; future events will re-push as they happen).
- **REQ-PROTO-124** Coalescing: when multiple push-eligible events affect the same `(subscription, conversation-or-thread)` pair within 30 seconds, herold MUST combine them into a single push using the same `tag` so the browser replaces rather than stacks notifications. The replacement payload reflects the latest aggregated state (e.g. "3 new messages on Re: Project X", not three separate notifications).
- **REQ-PROTO-125** Payload privacy: payloads MUST NOT contain full message bodies. Mail: subject + ≤ 80 chars of preview. Chat: ≤ 80 chars of message text or `[image]` / `[reaction]` markers. Calendar: structured fields only (sender, event title, time, location). Reactions: emoji + sender display name. The cap is enforced at payload-build time.
- **REQ-PROTO-126** Rate limiting: per (principal, subscription), MUST cap pushes at 60 per minute and 1000 per day. Sustained excess (e.g. a runaway notification scenario from a malformed Sieve filter) triggers a per-subscription cooldown — the dispatcher logs and stops pushing for 5 minutes — preventing user wake-up storms.
- **REQ-PROTO-127** Rules engine for `notificationRules`: herold parses and stores the JSON blob; the rules grammar matches the suite's `25-push-notifications.md` REQ-PUSH-80..83 (per-event-type toggles, mail-by-category, mail-by-sender-VIP, chat DM-vs-Space, calendar invites, calls, reactions). Unknown fields in the blob are preserved verbatim so future suite versions can extend the rules without herold needing schema changes.

## ManageSieve (REQ-PROTO-MGSV)

- **REQ-PROTO-50** MUST listen on **4190/tcp** (STARTTLS) with the capabilities from RFC 5804.
- **REQ-PROTO-51** MUST validate scripts on upload using the same Sieve parser used at delivery — no divergence between "accepted" and "runnable".
- **REQ-PROTO-52** MUST support HAVESPACE, CHECKSCRIPT, and GETSCRIPT/PUTSCRIPT/LISTSCRIPTS/SETACTIVE/DELETESCRIPT/RENAMESCRIPT.

## Sieve (REQ-PROTO-SIEVE)

Sieve runs at delivery. See `06-filtering.md` for the pipeline; here we list language support.

Core:
- **REQ-PROTO-60** MUST implement Sieve (RFC 5228) base language.
- **REQ-PROTO-61** MUST implement extensions: `fileinto` (5228), `reject` (5429), `envelope` (5228), `imap4flags` (5232), `body` (5173), `vacation` (5230), `relational` (5231), `subaddress` (5233), `regex` (draft / de facto), `copy` (3894), `include` (6609), `variables` (5229), `date` (5260), `mailbox` (5490), `mailboxid` (9042), `encoded-character` (5228), `editheader` (5293), `duplicate` (7352).
- **REQ-PROTO-62** Notification (`enotify` — RFC 5435) limited to mailto at v1; XMPP/SMS notifiers out of scope.
- **REQ-PROTO-63** `vacation-seconds` (6131) supported.
- **REQ-PROTO-64** `extlists` (6134), `foreverypart` (5703), `mime` (5703) — yes, required for modern content rules.
- **REQ-PROTO-65** `spamtest`/`spamtestplus` (5235) — yes; maps to our spam filter score.
- **REQ-PROTO-66** Non-standard Sieve extensions that break the sandbox — **no**: no `execute` / `extprograms` (spawn subprocess), no `llm` action (user script calling an external LLM), no arbitrary shell-out. The server's LLM spam classifier (REQ-FILT-10) is a *separate*, server-side pipeline step that runs *before* Sieve and produces a score Sieve scripts can read via `spamtestplus` (REQ-PROTO-65) — that is the only way LLM output reaches a Sieve script.
- **REQ-PROTO-67** Per-user scripts; one active script at a time (ManageSieve `SETACTIVE`). Global/admin scripts run *before* user script.
- **REQ-PROTO-68** Sieve execution MUST be sandboxed: no filesystem access, no network (except `redirect`), bounded CPU + memory per invocation.

## TLS (wire concern — detailed in 04-email-security.md and 09-operations.md)

- **REQ-PROTO-70** MUST support TLS 1.2 and TLS 1.3 on all protocols. TLS 1.0/1.1 rejected.
- **REQ-PROTO-71** Cipher suites: Mozilla "intermediate" by default, "modern" opt-in.
- **REQ-PROTO-72** SNI-based certificate selection across all listeners.
- **REQ-PROTO-73** ALPN advertised where relevant (HTTP/1.1, h2 for JMAP).

## HTTP surfaces the server DOES serve

All HTTPS, all on one port (default 443) with path-based routing unless the operator configures a dedicated admin port. SNI-aware cert selection per hostname.

| Path / host | Purpose | REQ refs |
|---|---|---|
| `POST /jmap`, `/jmap/eventsource`, `/jmap/upload/*`, `/jmap/download/*` | JMAP Core + Mail + SSE push | REQ-PROTO-40..48 |
| `/.well-known/jmap` | JMAP session discovery | RFC 8620 |
| `/api/v1/mail/send`, `/send-raw`, `/send-batch`, `/send/quota`, `/send/stats` | **HTTP send API** (mail-submission for apps) | REQ-SEND-01..61 |
| `/api/v1/hooks/*` | Mail-arrival webhook subscriptions (register/list/delete) | REQ-HOOK-01..05 |
| `/api/v1/mail/blobs/<id>/raw?sig=…` | Signed fetch URL served to webhook receivers fetching message bodies | REQ-HOOK-30..31 |
| `/proxy/image?url=<encoded>` | Inbound HTML mail image proxy — fetches upstream, strips tracking headers, enforces caps, serves back. Same-origin so the suite's CSP can `img-src 'self'`. | REQ-SEND-70..75 |
| `/chat/ws` (WebSocket upgrade) | Chat ephemeral channel — typing indicators, presence, WebRTC call signaling, TURN credential mint. Auth via suite session cookie. | REQ-CHAT-40..46 |
| `/api/v1/principals/*`, `/domains/*`, `/queue/*`, `/spam/*`, `/tls/*`, `/reports/*`, `/audit-log`, `/server/*` | Admin REST API | REQ-ADM-10..22 |
| `/api/openapi.json` | OpenAPI 3.1 spec | REQ-ADM-05 |
| `/admin/*` (phase 2) | Admin web UI (HTMX + Go templates) | REQ-ADM-200..202 |
| `/settings/*` (phase 3) | User self-service panel | REQ-ADM-203 |
| `/auth/oidc/<provider>/link`, `/auth/oidc/<provider>/callback` | External OIDC federation flow (RP only) | REQ-AUTH-51 |
| `/healthz/live`, `/healthz/ready` | Health endpoints (unauthenticated) | REQ-OPS-110..112 |
| `/metrics` | Prometheus exposition (unauthenticated, separate bind by default) | REQ-OPS-90..92 |
| `mta-sts.<hosted-domain>/.well-known/mta-sts.txt` | MTA-STS policy for each hosted domain (separate vhost, cert via ACME) | REQ-SEC-54 |
| `/.well-known/acme-challenge/<token>` (port 80) | ACME HTTP-01 challenge responder; ephemeral, only during ACME dance | REQ-OPS-50 |

## What we don't do on the wire

HTTP is one of our primary surfaces (see the table above). What we *don't* serve or speak:

- **No HTTP endpoints outside the table above.** Specifically: no CalDAV/CardDAV/WebDAV (groupware dropped, NG3), no generic file-serving, no OIDC-issuer endpoints (we are a relying party only, NG11), no arbitrary plugin-registered HTTP routes, no "POST an RFC 5322 message to /inbox" (the send API is structured, not raw-upload), no SES receive-rule DSL.
- **No DNS server.** We're a resolver client only.
- **No LDAP**, not even as client — out of scope.
- **No SNMP.**
- **No gRPC.**
- **No WebSockets except `/chat/ws`** (REQ-CHAT-40, phase 2 — chat ephemeral signals + WebRTC call signaling + TURN credential mint). JMAP-over-WebSocket (RFC 8887) remains out: the suite's design uses EventSource for JMAP push, and the WebSocket carries chat-only signals, not JMAP method calls.

## Open questions

- Sieve `notify` via Web Push: worth it, or mailto-only forever? (Web Push itself is now phase 1 per the rev-7 advancement of REQ-PROTO-48; revisit whether Sieve scripts should be able to trigger push directly.)
