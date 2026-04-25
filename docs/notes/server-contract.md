# Server contract

What tabard expects herold to deliver — capabilities, behaviours, and integration points beyond bare RFC 8621 conformance. This file is the single place where "this is herold's job" claims live.

Per resolved Q14/Q15: herold ships before tabard implements, and tabard's spec treats herold capabilities as available. Where herold's current requirements don't yet commit to something tabard requires, the gap is recorded in `herold-coverage.md` so it can be addressed on the herold side.

## Deployment

Tabard and herold are deployed at the same origin (resolved Q1). Herold serves both:

- The JMAP API at `/jmap`, `/jmap/eventsource`, `/jmap/upload/*`, `/jmap/download/*`, plus `/.well-known/jmap` for the session descriptor.
- Tabard's static bundle (HTML / JS / CSS / fonts) at the suite's root and per-app paths (`/mail/`, eventually `/calendar/`, `/contacts/`).
- The login surface at `/login` and the logout endpoint at `/logout`.

This is the "deployed together, no separate IdP" stance. Herold authenticates users (password+TOTP locally, or OIDC redirect through an external provider as a relying party — herold is not itself an OIDC issuer). On successful authentication herold sets a session cookie scoped to the suite origin; tabard's JMAP requests carry the cookie automatically (`credentials: 'include'`).

## JMAP capabilities required

| Capability URI | Why tabard needs it |
|----------------|---------------------|
| `urn:ietf:params:jmap:core` | RFC 8620 base — methods, batched calls, EventSource push, `Blob/upload`. |
| `urn:ietf:params:jmap:mail` | RFC 8621 — `Email`, `Mailbox`, `Thread`, `Identity`, `EmailSubmission`, `SearchSnippet`, `VacationResponse`. |
| `urn:ietf:params:jmap:submission` | `EmailSubmission/set` for sending. |
| `urn:ietf:params:jmap:sieve` (RFC 9007) | Filter management — `Sieve/get`, `Sieve/set`, `Sieve/validate`. Required by `../requirements/04-filters.md`. |
| `urn:ietf:params:jmap:contacts` (RFC 9553 + binding draft) | Required by tabard-mail's compose autocomplete (`../requirements/02-mail-basics.md` REQ-MAIL-11) and by tabard-contacts. |
| `urn:ietf:params:jmap:calendars` (RFC 8984 + binding draft) | Required by iMIP RSVP (`../requirements/15-calendar-invites.md`) and by tabard-calendar. |
| `https://tabard.dev/jmap/snooze` | Snooze contract — see Behaviours. |
| `https://tabard.dev/jmap/categorise` | LLM-driven categorisation — see Behaviours. |

## Capabilities tabard does NOT require

`urn:ietf:params:jmap:websocket` (RFC 8887) — tabard uses EventSource. WebSocket support is fine for the server to advertise; tabard ignores it in v1.

## Behaviours

### Auth and session (resolved Q1)

- `GET /login` serves the login form (or initiates the OIDC redirect, depending on per-user policy).
- On successful auth, herold sets a session cookie: `HttpOnly; Secure; SameSite=Strict; Path=/`.
- All JMAP endpoints accept the cookie. No `Authorization` header required for browser sessions.
- `POST /logout` clears the cookie and redirects to `/login`.
- Cookie lifetime, idle timeout, refresh policy: herold's responsibility. Tabard does not implement client-side timeout logic.
- Bearer-token auth on JMAP endpoints stays available for non-browser clients (CLI, tests). Tabard does not use it.

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
- Expose methods for the user (via tabard) to update the category set, the prompt, and to trigger bulk re-categorisation of recent inbox.
- Treat user `Email/set` updates that change `$category-*` keywords as feedback signal for the classifier (mechanism internal).

This is distinct from herold's spam classification (which produces `$junk` and the spam mailbox). Categorisation runs after spam — only mail that lands in inbox gets categorised.

### Mailbox colour

Tabard sets `Mailbox.color` (a hex string) on label create / edit. Herold persists and returns this property. See `../requirements/03-labels.md`.

### Image proxy

For inline `<img>` references in HTML mail, tabard renders the image via a server-side proxy URL of the shape `<origin>/proxy/image?url=<encoded-original>`. The proxy fetches the image, strips tracking-relevant request headers (Cookie, Referer, User-Agent → fixed string), enforces a size cap, and serves it back. Same origin as the JMAP API so the CSP can `img-src 'self'` (`../requirements/13-nonfunctional.md` REQ-SEC-07).

### Per-Identity signature

`Identity` carries an extension property `signature` (plain-text body, plus optional HTML in phase 2). Tabard reads it to populate compose; updates it via `Identity/set`. See `../requirements/20-settings.md` REQ-SET-03.

### EventSource push

Per RFC 8620 §7. Tabard expects:

- `GET /jmap/eventsource?types=Email,Mailbox,Thread,Identity,EmailSubmission&closeafter=no&ping=30` to return `text/event-stream`.
- Heartbeat events at the configured interval to keep proxies from idling out.
- Reconnect via `Last-Event-ID` to resume the change stream without losing events.

### Search snippets

`SearchSnippet/get` per RFC 8621 §7.1 — required to render the per-result snippet in search results.

### iMIP REPLY pass-through

Per `../requirements/15-calendar-invites.md`. When tabard sends an `EmailSubmission/set` containing an `Email` whose body has a `text/calendar; method=REPLY` MIME part, herold's outbound queue passes it through transparently — the REPLY is just a normal multipart email from herold's perspective. No special handling required, but the path must not strip the `text/calendar` part.

### Authentication-Results header

Herold writes `Authentication-Results` per RFC 8601 during inbound mail processing. Tabard parses this header to drive `../requirements/18-authentication-results.md`. The `authserv-id` token in the header MUST be configurable so tabard can identify "this server's verdict" vs upstream relays.

## Cross-reference to herold

Herold-side gaps and current commitment status: `herold-coverage.md`. When this file is updated, mirror substantive changes there and on the herold side.
