# Server contract

What tabard expects herold to advertise and behave like, beyond bare RFC 8621 conformance. This file is the single place where "this is the server's job" claims live.

Herold is the only target server in v1 (`../00-scope.md`). The contract is split into capabilities (advertised in the JMAP session descriptor) and behaviours (what each capability has to do).

## Capabilities required

| Capability URI | Why tabard needs it |
|----------------|---------------------|
| `urn:ietf:params:jmap:core` | RFC 8620 base — methods, batched calls, EventSource push, `Blob/upload`. |
| `urn:ietf:params:jmap:mail` | RFC 8621 — `Email`, `Mailbox`, `Thread`, `Identity`, `EmailSubmission`, `SearchSnippet`, `VacationResponse`, etc. |
| `urn:ietf:params:jmap:submission` | `EmailSubmission/set` for sending (also covered by RFC 8621 §7, but listed separately because some servers gate it). |
| `urn:ietf:params:jmap:sieve` (RFC 9007) | Filter management — `Sieve/get`, `Sieve/set`, `Sieve/validate`. Without this, tabard hides the Filters UI entirely (see `../requirements/04-filters.md` REQ-FLT-22). |
| `https://tabard.dev/jmap/snooze` (proposed) | Snooze contract — see below. Tabard requires a stable URI even though the underlying mechanism is server-internal. |

## Capabilities tabard-mail does NOT require

`urn:ietf:params:jmap:websocket` (RFC 8887) — tabard-mail uses EventSource. WebSocket support is fine for the server to advertise; tabard ignores it in v1.

## Capabilities the broader suite WILL require

These are not blockers for tabard-mail, but the herold roadmap needs to plan for them since the planned sibling apps (`../00-scope.md` § "Tabard is a suite") depend on them:

- `urn:ietf:params:jmap:calendars` — for tabard-calendar. Object model: JSCalendar (RFC 8984). Binding is currently a draft; pin to a specific revision when the work starts.
- `urn:ietf:params:jmap:contacts` — for tabard-contacts. Object model: JSContact (RFC 9553). Same draft caveat.

The forward-compat work in herold (`internal/store/types.go`'s `StateChange` shape and the JMAP capability registry described in herold's `docs/architecture/03-protocol-architecture.md` and `docs/architecture/05-sync-and-state.md`) was sized for this — it now has actual customers, not hypothetical ones.

## Behaviours

### Snooze (`https://tabard.dev/jmap/snooze`)

Herold advertises this capability when both:

- It accepts `keywords/$snoozed: true` on `Email/set`.
- It accepts a `snoozedUntil` extension property (ISO 8601 datetime in UTC) on `Email/set`.

When the wall clock reaches `snoozedUntil`, herold MUST atomically:

1. Clear `$snoozed` from `Email.keywords`.
2. Clear `snoozedUntil`.
3. Re-add the principal's inbox mailbox to `Email.mailboxIds`.
4. Emit a state-change event on the affected types (`Email`, `Mailbox`).

Tabard does not implement client-side wake-ups (`../requirements/06-snooze.md` rationale).

### Mailbox colour

Tabard sets `Mailbox.color` (a hex string) on label create / edit. Herold MUST persist and return this property. If absent on read, tabard derives a deterministic fallback colour from the mailbox ID and surfaces a one-time notice ("colours not persisted on this server"). See `../requirements/03-labels.md`.

### Image proxy

For inline `<img>` references in HTML mail, tabard renders the image via a server-side proxy URL of the shape `<jmap-base>/proxy/image?url=<encoded-original>`. The proxy fetches the image, strips tracking-relevant request headers (Cookie, Referer, User-Agent → fixed string), enforces a size cap, and serves it back. Tabard requires the proxy origin to be the same origin as the JMAP API so its CSP can `img-src 'self'` (`../requirements/13-nonfunctional.md` REQ-SEC-07). If the proxy is absent, external images stay blocked unconditionally.

### EventSource push

Per RFC 8620 §7. Tabard expects:

- `GET /jmap/eventsource?types=Email,Mailbox,Thread,Identity,EmailSubmission&closeafter=no&ping=30` to return `text/event-stream`.
- Heartbeat `: ping` events at the interval from `ping=` to keep proxies from idling out.
- Reconnect via `Last-Event-ID` to resume the change stream without losing events.

### Search snippets

`SearchSnippet/get` per RFC 8621 §7.1 is required to render the per-result snippet in search results. Without it, tabard falls back to the email's preview text.

## What happens when the contract is unmet

Tabard reads the session descriptor's `capabilities` and `accountCapabilities` on connect and degrades feature-by-feature:

- Missing `:sieve` → Filters UI hidden (REQ-FLT-22).
- Missing snooze capability → Snooze button hidden, `b` shortcut disabled, "Snoozed" sidebar entry hidden.
- Missing image proxy → external images blocked unconditionally (REQ-SEC-05 stays in force; per-sender opt-in becomes a no-op with explanatory text).
- Missing `Mailbox.color` round-trip → fallback as above.

Tabard never silently emulates a server-side feature client-side. Either the server provides it, or the feature is hidden.

## Cross-reference to herold

When this file is updated, mirror the change into herold's requirements:

- Snooze contract → `../../herold/docs/requirements/05-storage.md` (mailbox/email schema) and `../../herold/docs/requirements/01-protocols.md` (JMAP).
- Mailbox colour → herold's mailbox storage schema.
- Image proxy → not yet specified in herold; would slot into `../../herold/docs/requirements/12-http-mail-api.md` or a new requirement section.

The capabilities tabard expects are also a forcing function for herold's JMAP capability registry (see `../../herold/docs/architecture/03-protocol-architecture.md`'s "Capability and account registration" subsection).
