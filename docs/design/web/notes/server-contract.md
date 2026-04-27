# Server contract

What the suite expects herold to deliver — capabilities, behaviours, and integration points beyond bare RFC 8621 conformance. This file is the single place where "this is herold's job" claims live.

Herold is operationally ready for the suite development; the suite treats every capability listed below as available. Coverage status against herold's spec lives in `herold-coverage.md`. For local development, a running herold instance is assumed (`apps/suite/README.md`).

## Deployment

The suite and herold are deployed at the same origin (resolved Q1). Herold ships the suite's SPA as embedded static assets in the herold binary — `REQ-DEPLOY-COLOC-01..05` in herold's spec rev 9. The deployment topology splits into two listeners:

- **Public listener** (default `0.0.0.0:443`). What the suite talks to. Serves:
  - The suite's static bundle (HTML / JS / CSS / fonts) at the suite root and per-app paths (`/mail/`, `/chat/`, eventually `/calendar/`, `/contacts/`).
  - The JMAP API: `/.well-known/jmap`, `/jmap`, `/jmap/eventsource`, `/jmap/upload/*`, `/jmap/download/*`.
  - The chat ephemeral WebSocket: `/chat/ws`.
  - The HTTP send API + incoming-mail webhooks: `/api/v1/mail/*`, `/api/v1/hooks/*`.
  - The image proxy: `/proxy/image`.
  - The login + logout surface: `/login`, `/logout`, `/auth/oidc/*`.
- **Admin listener** (default `127.0.0.1:9443`, loopback by default). What the operator talks to via CLI / curl / a dedicated admin browser session. Serves admin REST (`/api/v1/principals/*`, `/api/v1/domains/*`, `/api/v1/queue/*`, etc.), the future admin web UI, `/healthz/*`, `/metrics`. **The suite never touches the admin listener.** Per `REQ-OPS-ADMIN-LISTENER-01..03` in herold's spec.

The operator can publish the admin listener if they need remote admin access — but the default is loopback, with TLS, and the explicit framing is "end-user compromise of the public surface cannot escalate to operator-level damage".

### Auth scopes

Herold's session cookies and API keys carry a closed-enum scope set (`REQ-AUTH-SCOPE-01..04` in herold's spec rev 9). The suite's session cookies are issued under the **user** scope; admin operations require an **admin** scope obtained via TOTP step-up at the admin listener. Cross-scope rejection runs at every handler boundary, so a user-scope cookie cannot reach admin endpoints even if a path collision tried to expose them.

The suite's only relevant scope is `user`. The suite never asks for the admin scope, never reads admin endpoints, and never carries admin tokens. If a suite-side need emerged for an admin operation (it doesn't currently), that operation would happen via the operator's own tooling, not via the suite.

### Authentication flow

Herold authenticates users (password+TOTP locally, or OIDC redirect through an external provider as a relying party — herold is not itself an OIDC issuer). On successful authentication herold sets a session cookie scoped to the suite origin; the suite's JMAP requests carry the cookie automatically (`credentials: 'include'`).

### Target scale

The suite (herold + the suite) is sized for "small family / association / group" — 5–50 users on a single VPS — not enterprise scale. The suite's bundle target (~200 KB) and the assumption of one JMAP server per deployment both reflect this. Multi-tenant deployments are out of scope on both sides.

## JMAP capabilities required

| Capability URI | Why the suite needs it |
|----------------|---------------------|
| `urn:ietf:params:jmap:core` | RFC 8620 base — methods, batched calls, EventSource push, `Blob/upload`. |
| `urn:ietf:params:jmap:mail` | RFC 8621 — `Email`, `Mailbox`, `Thread`, `Identity`, `EmailSubmission`, `SearchSnippet`, `VacationResponse`. |
| `urn:ietf:params:jmap:submission` | `EmailSubmission/set` for sending. |
| `urn:ietf:params:jmap:sieve` (RFC 9007) | Filter management — `Sieve/get`, `Sieve/set`, `Sieve/validate`. Required by `../requirements/04-filters.md`. |
| `urn:ietf:params:jmap:contacts` (RFC 9553 + binding draft) | Required by the suite's compose autocomplete (`../requirements/02-mail-basics.md` REQ-MAIL-11) and by the contacts app. |
| `urn:ietf:params:jmap:calendars` (RFC 8984 + binding draft) | Required by iMIP RSVP (`../requirements/15-calendar-invites.md`) and by the calendar app. |
| `https://tabard.dev/jmap/snooze` | Snooze contract — see Behaviours. |
| `https://tabard.dev/jmap/categorise` | LLM-driven categorisation — see Behaviours. |
| `https://tabard.dev/jmap/chat` | Chat datatypes (`Conversation`, `Message`, `Membership`) plus the ephemeral WebSocket and call-signaling endpoints — see Behaviours. |
| `https://tabard.dev/jmap/email-reactions` | `Email.reactions` extension property + cross-server reaction email propagation — see Behaviours. |
| `https://tabard.dev/jmap/shortcut-coach` | `ShortcutCoachStat` per-principal datatype backing the shortcut coach — see Behaviours. |
| `https://tabard.dev/jmap/push` | Web Push delivery (RFC 8030 + RFC 8620 §7.2 `PushSubscription` + the suite's enriched-content payload contract) — see Behaviours. |

## Capabilities the suite does NOT require

`urn:ietf:params:jmap:websocket` (RFC 8887) — the suite uses EventSource. WebSocket support is fine for the server to advertise; the suite ignores it in v1.

## Behaviours

### Auth and session (resolved Q1)

- `GET /login` serves the login form (or initiates the OIDC redirect, depending on per-user policy).
- On successful auth, herold sets a session cookie: `HttpOnly; Secure; SameSite=Strict; Path=/`.
- All JMAP endpoints accept the cookie. No `Authorization` header required for browser sessions.
- `POST /logout` clears the cookie and redirects to `/login`.
- Cookie lifetime, idle timeout, refresh policy: herold's responsibility. The suite does not implement client-side timeout logic.
- Bearer-token auth on JMAP endpoints stays available for non-browser clients (CLI, tests). The suite does not use it.

### Snooze (`https://tabard.dev/jmap/snooze`)

Herold advertises this capability when it implements all of:

- Accepts `keywords/$snoozed: true` on `Email/set`.
- Accepts a `snoozedUntil` extension property (ISO 8601 datetime in UTC) on `Email/set`.
- At wall-clock `snoozedUntil`, atomically:
  1. Clears `$snoozed` from `Email.keywords`.
  2. Clears `snoozedUntil`.
  3. Re-adds the principal's inbox mailbox to `Email.mailboxIds`.
  4. Emits a state-change event on the affected types (`Email`, `Mailbox`).

### LLM categorisation (`https://tabard.dev/jmap/categorise`)

Per `../requirements/05-categorisation.md`. Herold's responsibilities:

- Run an LLM classifier on each delivered Email; apply at most one `$category-<name>` keyword.
- Persist the per-account category set (default: Primary, Social, Promotions, Updates, Forums) and the classifier prompt.
- Expose methods for the user (via the suite) to update the category set, the prompt, and to trigger bulk re-categorisation of recent inbox.
- Treat user `Email/set` updates that change `$category-*` keywords as feedback signal for the classifier (mechanism internal).

This is distinct from herold's spam classification (which produces `$junk` and the spam mailbox). Categorisation runs after spam — only mail that lands in inbox gets categorised.

### Mailbox colour

The suite sets `Mailbox.color` (a hex string) on label create / edit. Herold persists and returns this property. See `../requirements/03-labels.md`.

### Image proxy (resolved Q4)

For inline `<img>` references in HTML mail, the suite renders the image via a server-side proxy URL of the shape `<origin>/proxy/image?url=<encoded-original>`. The proxy fetches the image, strips tracking-relevant request headers, enforces caps, and serves the result back. Same origin as the JMAP API so the CSP can `img-src 'self'` (`../requirements/13-nonfunctional.md` REQ-SEC-07).

**Where it runs (v1):** in-process inside herold. The simplest fit for the single-node target. May graduate to a herold plugin (sidecar) later if operators want pluggable replacement; not v1.

**Request handling:**

- **Auth:** the proxy endpoint requires the suite session cookie. No anonymous use.
- **Scheme:** only `https://` upstreams accepted. `http://` upstreams return `400`. URL length cap: 2048 chars.
- **Redirects:** at most 3 redirect hops followed; further redirects abort with a `502`.
- **Outgoing request shape:**
  - `Cookie`: not sent.
  - `Referer`: not sent.
  - `User-Agent`: a fixed generic string (e.g. `suite-image-proxy/1`). Same value for every request — no per-user fingerprinting.
  - No other identifying headers.
- **Content-Type validation:** upstream `Content-Type` must start with `image/`; otherwise the proxy returns `415`. Prevents the proxy from being used as a generic content tunnel.
- **Size cap:** 25 MB per response (configurable). Upstreams larger than the cap get `413` from the proxy.
- **Timeouts:** 10s connect, 30s total.

**Caching:**

- Honour upstream `Cache-Control`. Cap retention at 24 hours regardless.
- Shared cache keyed by URL hash. Cross-user sharing is acceptable: the URL is the cache key, and a cache hit for user B doesn't leak that user A opened the same image (the sender already got their open count from user A's first fetch).
- Cache evicts on size pressure (LRU); operator-configurable max size.

**Retries:**

- One retry on transient upstream failure (5xx, network error) after 1 s.
- No retries on 4xx.
- After exhausted retries: return the upstream status (or `502` for network failures).

**Abuse limits:**

- 200 fetches per user per minute (a typical newsletter is ~30 images; this is generous but bounded).
- 10 fetches per (user, upstream origin) per minute — prevents hammering a single CDN.
- 8 concurrent fetches per user.
- Operator-configurable; the values above are defaults.

**Failure-mode UX:**

The proxy returns accurate HTTP status codes (404, 502, 413, 415, 408, etc.). The suite's HTML iframe renders the broken-image placeholder natively per browser. No suite-side custom placeholder image and no transparent-PNG-on-failure substitution — accurate failure communication beats hidden failures.

### Per-Identity signature

`Identity` carries an extension property `signature` (plain-text body, plus optional HTML in phase 2). The suite reads it to populate compose; updates it via `Identity/set`. See `../requirements/20-settings.md` REQ-SET-03.

### EventSource push

Per RFC 8620 §7. The suite expects:

- `GET /jmap/eventsource?types=Email,Mailbox,Thread,Identity,EmailSubmission&closeafter=no&ping=30` to return `text/event-stream`.
- Heartbeat events at the configured interval to keep proxies from idling out.
- Reconnect via `Last-Event-ID` to resume the change stream without losing events.

### Search snippets

`SearchSnippet/get` per RFC 8621 §7.1 — required to render the per-result snippet in search results.

### Delayed send via `EmailSubmission.sendAt`

Per RFC 8621 §7.5. `EmailSubmission` carries an optional `sendAt` UTCDate property; when set, herold's outbound queue MUST hold the submission and only deliver at or after the indicated time. When `sendAt` is `null` or absent, the submission is delivered immediately as today.

Cancellation: `EmailSubmission/set { destroy: [<id>] }` issued before `sendAt` MUST cancel delivery — the submission is removed from the queue and no message leaves. After `sendAt` (or after the message has actually been handed off to remote SMTP), destroy is a best-effort no-op; the message has already left.

The suite uses this to back the send-undo feature (`../requirements/02-mail-basics.md` REQ-MAIL-14, `../requirements/11-optimistic-ui.md` REQ-OPT-11). The same mechanism is the substrate for user-facing scheduled send when that ships.

### Chat (`https://tabard.dev/jmap/chat`)

Per `../requirements/08-chat.md` and `../architecture/07-chat-protocol.md`. Herold's responsibilities:

- **Datatypes.** New JMAP entity kinds: `Conversation` (DMs and Spaces), `Message` (per-conversation messages), `Membership` (per-conversation participation incl. role and read-through). Each gets standard JMAP methods: `Foo/get`, `/query`, `/changes`, `/set`. State strings advance per the standard rules; push fans out via the EventSource channel that already serves mail.
- **Ephemeral channel.** WebSocket endpoint at `wss://<origin>/chat/ws`. Authenticated by the suite session cookie. Carries typing indicators, presence, and WebRTC call signaling per `../architecture/07-chat-protocol.md` § Ephemeral channel. Server-side rate limits and heartbeat (30s ping / 90s timeout).
- **Presence.** Server tracks per-user presence (online / away / offline) derived from WebSocket connection state and the user's "show me as offline" setting. Presence pushed to interested peers (anyone in a conversation with the user) over the ephemeral channel.
- **TURN credentials.** Herold mints short-lived (~5 min TTL) TURN credentials on demand for each call, scoped to the requesting user. Credentials authenticate against a coturn (or equivalent) deployment configured by the operator. The mint endpoint is over the chat WebSocket: `{"op": "call.credentials", "callId": "..."}`.
- **Inline image attachments.** Reuse the JMAP `Blob/upload` path; chat messages reference uploaded blobs by id. No separate chat-blob storage.
- **Retention.** Operator-configurable per Space (and globally for DMs). Default: forever. The suite surfaces the active retention via the chat capability metadata if herold reports it.

### Email reactions (`https://tabard.dev/jmap/email-reactions`)

Per `../requirements/02-mail-basics.md` § Reactions. Shape mirrors chat's `Message.reactions` (`08-chat.md` REQ-CHAT-30..33).

**Local-only (same-server) path:**

- `Email.reactions` is an extension property: `{ "<emoji>": ["<principal-id>", ...] }`. Sparse.
- Mutated via `Email/set { update: { "<email-id>": { "reactions/<emoji>/<my-principal-id>": true | null } } }`. Add or remove the requesting user's reaction. JSON-patch path semantics.
- Authorisation: a user can only patch their own principalId in any reactor list. Attempts to patch another user's reaction return `forbidden`.
- State string for `Email` advances on reaction changes; pushed via the standard EventSource channel.

**Cross-server (recipient on another herold or third-party server) path:**

When a reactor's `Email/set` adds a reaction to a message whose other recipients are on different servers, herold's outbound queue MUST emit a reaction email to each external recipient. Wire format:

```
From: <reactor address>
To: <each recipient of the original>
Subject: Re: <original subject>
In-Reply-To: <original Message-ID>
References: <original References + original Message-ID>
Date: <now>
Message-ID: <new id>
X-Tabard-Reaction-To: <original Message-ID>
X-Tabard-Reaction-Emoji: <utf-8 emoji>
X-Tabard-Reaction-Action: add
Content-Type: multipart/alternative; boundary="..."

--bound
Content-Type: text/plain; charset=utf-8

<reactor display name> reacted with <emoji> to your message.

--bound
Content-Type: text/html; charset=utf-8

<p><reactor display name> reacted with <span style="font-size:1.5em"><emoji></span> to your message.</p>

--bound--
```

A herold-aware inbound pipeline detects the `X-Tabard-Reaction-*` headers, looks up the referenced original `Message-ID` in the recipient's mailbox, and:

- If found AND the reactor (`From` address) corresponds to a known correspondent (sender or recipient of the original): apply as a native `Email.reactions` mutation; suppress the reaction email from inbox delivery.
- If not found OR reactor isn't recognised: deliver as a normal email (the human-readable body shows it correctly to the recipient).

A non-herold receiver sees the email as plain mail. Threading via `In-Reply-To` puts it in the same thread as the original.

**Removal does not propagate cross-server.** When a user removes a reaction, the change is applied locally and to other herolds *that originally received the reaction email*; there is no follow-up "un-react" email to non-herold receivers. Reactions are ephemeral signals; the asymmetry is acceptable.

### Web Push (`https://tabard.dev/jmap/push`)

Per `../requirements/25-push-notifications.md`. Browser-level push notifications for new mail / chat / calendar invites / video calls / reactions. RFC 8030 transport + RFC 8620 §7.2 PushSubscription datatype + a suite-specific enriched payload shape.

**Subscription:**

- The suite registers a Web Push subscription via the standard `PushSubscription/set { create }` (RFC 8620 §7.2). Properties: `endpoint`, `keys: { p256dh, auth }`, `expires`, `types` (the JMAP types whose state changes should be pushed — for the suite typically `["Email", "Message", "EmailSubmission", "Conversation", "Membership"]`), plus the Suite-specific properties below.
- The suite adds extension properties on the subscription:
  - `notificationRules`: a JSON blob expressing the user's preferences (`{ mail: { categories: ["primary"], vipSenders: [...], inboxOnly: true }, chat: { dmsAlways: true, spacesOnMention: true }, calendar: true, calls: true, reactions: true }`). Herold uses this to decide whether to enrich the push or fall through to a minimal state-change push.
  - `quietHours`: `{ startHourLocal: 22, endHourLocal: 7, tz: "Europe/Berlin" }` — herold suppresses non-critical pushes during this window.
  - `vapidKeyAtRegistration`: the VAPID public key the client used at subscription time, so herold knows which key pair to encrypt against (key rotation is a herold concern; see § VAPID).

**Outbound push gateway:**

- When state changes affect a user with active subscriptions, herold's push dispatcher decides whether to push and what payload to use:
  1. Look up the principal's subscriptions (`PushSubscription/query`).
  2. For each subscription, evaluate `notificationRules` against the event. If the rule says "no", deliver only the minimal RFC 8620 §7.2 state-change envelope (so the client wakes its caches if open).
  3. If the rule says "yes" and the event qualifies for enriched content, build the suite payload (`{ kind, threadId, emailId, ... }` per `../requirements/25-push-notifications.md` REQ-PUSH-40..47).
  4. Encrypt the payload per RFC 8291 using the subscription's `p256dh` and `auth` keys.
  5. POST to the subscription's `endpoint` with the VAPID `Authorization` header per RFC 8292.
  6. On 410 (Gone) or 404 from the push service: destroy the subscription (`PushSubscription/set { destroy }`).
  7. On other 4xx: log and retry once with backoff; persistent failure destroys the subscription.

**VAPID:**

- Herold maintains a VAPID key pair at the deployment level (one per server, not per user). Public key is exposed in the JMAP session descriptor under `urn:ietf:params:jmap:core` capability data so the suite can include it in the browser's `pushManager.subscribe` call.
- VAPID key rotation: not a v1 feature; manual operator process if needed. The key is long-lived in normal operation.
- VAPID `sub` claim: `mailto:<operator-admin-address>` from herold's deployment config.

**Privacy and safety:**

- Per-subscription delivery of one push event MUST NOT leak data about other principals to the push service. Each subscription is independently encrypted; the push service never sees plaintext content.
- The push payload is bounded to ~2.5 KB plaintext to leave headroom for RFC 8291 encryption overhead.
- Body content sent in the payload follows the per-event-type contract (subject + 80-char preview for mail; first 80 chars of body for chat). Full message bodies are NEVER pushed.

### Shortcut coach (`https://tabard.dev/jmap/shortcut-coach`)

Per `../requirements/23-shortcut-coach.md`. Per-principal stats backing the always-on shortcut coach.

Datatype: `ShortcutCoachStat`. One row per (principal, action) pair.

**Properties:**

```
{
  id:                  String,                // server-assigned
  action:              String,                // action name, e.g. "archive", "reply", "nav_inbox"
  keyboardCount14d:    Number,                // server-rolled count over the trailing 14 days
  mouseCount14d:       Number,
  keyboardCount90d:    Number,                // trailing 90 days
  mouseCount90d:       Number,
  lastKeyboardAt:      UTCDate?,
  lastMouseAt:         UTCDate?,
  dismissCount:        Number,                // hint-dismiss events; never decremented
  dismissUntil:        UTCDate?               // suppression deadline per REQ-COACH-33..34
}
```

**Behaviour:**

- `ShortcutCoachStat/get`, `/query`, `/changes`, `/set` per standard JMAP. State string advances per the standard rules but the suite does not subscribe (REQ-COACH-64).
- `ShortcutCoachStat/set { update }` accepts incremental patches — typically a flush of recent invocations: `{ "<action>": { keyboard: +N, mouse: +M, lastKeyboardAt: <ts>, lastMouseAt: <ts>, dismissCount: +K } }`. Herold rolls them into the 14d/90d counters using a per-row timestamp ring (or equivalent windowed-counter machinery — implementation choice).
- `ShortcutCoachStat/set { destroy }` deletes a single stat row; destroying everything for a principal is the "Reset coach data" path.
- Authorisation: a principal can only read/write their own ShortcutCoachStat rows. Admin reads are out (the data is private to the user — `../requirements/23-shortcut-coach.md` REQ-COACH-04).
- Storage shape on herold: a small per-principal table. The 14d/90d counters can be derived from a per-row activity log or maintained as decaying counters; either is fine. Volume is small (~30 actions × ~1k principals = trivial).

### iMIP REPLY pass-through

Per `../requirements/15-calendar-invites.md`. When the suite sends an `EmailSubmission/set` containing an `Email` whose body has a `text/calendar; method=REPLY` MIME part, herold's outbound queue passes it through transparently — the REPLY is just a normal multipart email from herold's perspective. No special handling required, but the path must not strip the `text/calendar` part.

### Authentication-Results header

Herold writes `Authentication-Results` per RFC 8601 during inbound mail processing. The suite parses this header to drive `../requirements/18-authentication-results.md`. The `authserv-id` token in the header MUST be configurable so the suite can identify "this server's verdict" vs upstream relays.

## Cross-reference to herold

Herold-side gaps and current commitment status: `herold-coverage.md`. When this file is updated, mirror substantive changes there and on the herold side.
