# Herold coverage

What tabard requires of herold (per `server-contract.md`) and what herold's current spec commits to. Audit run 2026-04-25 against `/Users/hans/herold/docs/`.

Per resolved Q14: herold ships before tabard implements. Where herold's spec doesn't yet commit to something tabard requires, the gap is listed below — these are the items that need to land on the herold side before (or as part of) finishing herold v1.

## Committed (8)

These are explicitly in herold's requirements / architecture:

| Requirement | Herold reference |
|-------------|------------------|
| JMAP core (RFC 8620) — methods, batched calls, push, Blob/upload | `requirements/01-protocols.md` REQ-PROTO-40, `/jmap/eventsource` and `/jmap/upload/*` in the HTTP surfaces table |
| JMAP for Mail (RFC 8621) — `Email`, `Mailbox`, `Thread`, `Identity`, `EmailSubmission`, `SearchSnippet`, `VacationResponse` | `requirements/01-protocols.md` REQ-PROTO-41 |
| JMAP submission (`urn:ietf:params:jmap:submission`) — `EmailSubmission/set` tied to outbound queue | `requirements/01-protocols.md` REQ-PROTO-42 |
| EventSource push at `/jmap/eventsource` (RFC 8620 §7) | `requirements/01-protocols.md` REQ-PROTO-44 |
| Snooze: `$snoozed` keyword + `snoozedUntil` property + server-side wake-up emitting state change | `requirements/01-protocols.md` REQ-PROTO-49 (full contract); scheduled phase 2 per `implementation/02-phasing.md` |
| `Authentication-Results` header on inbound mail (RFC 8601) | `requirements/06-filtering.md` REQ-FILT-03; SPF/DKIM/DMARC/ARC requirements in `requirements/04-email-security.md` |
| Static asset serving from the same process (admin UI precedent) | `requirements/08-admin-and-management.md` REQ-ADM-200 — the same machinery serves tabard's bundle |
| Login UI / OIDC redirect (relying-party only, not IdP) — sets a session cookie | `requirements/02-identity-and-auth.md` REQ-AUTH-50..58 (per-user OIDC federation), JMAP auth section |

## Partial (3)

In herold's spec but the property tabard needs is not explicit:

| Requirement | Status | What's missing |
|-------------|--------|----------------|
| `Mailbox.color` extension property | Mailbox is committed; the colour extension is not mentioned | Herold's mailbox storage schema needs an explicit colour column / property; the JMAP capability negotiation needs to advertise it |
| LLM-based automatic categorisation | Herold has LLM-based **spam** classification (`requirements/06-filtering.md` REQ-FILT-01..42) producing `ham`/`suspect`/`spam`. **Categorisation** (Gmail-style Primary/Social/Promotions/Updates/Forums) is not in scope | Herold needs to add a categorisation pipeline distinct from spam: per-account category set, configurable prompt, `$category-<name>` keyword application on inbound mail, bulk re-categorisation API. The plugin architecture supports this (categorisation could be a sibling plugin to spam-llm) but no requirement currently commits to it. |
| Per-Identity signature property | `Identity` is in REQ-PROTO-41 | The `signature` extension property is not specified. Herold's Identity schema needs a `signature` column and the JMAP `Identity/set` validation needs to accept it. |
| `EmailSubmission.sendAt` honoured by the outbound queue | `EmailSubmission` is committed (REQ-PROTO-41/42, `requirements/03-mail-flow.md` REQ-FLOW-60: "every accepted outbound message goes into the durable outbound queue. No synchronous delivery.") | Required by tabard's send-undo (`requirements/02-mail-basics.md` REQ-MAIL-14, `requirements/11-optimistic-ui.md` REQ-OPT-11). The `sendAt` property is part of RFC 8621 §7.5 and so implicitly required by REQ-PROTO-41, but herold's outbound queue requirements should explicitly commit to honouring it (delay until timestamp; cancel on `EmailSubmission/set { destroy }` before `sendAt`). One-line clarification in herold's `requirements/03-mail-flow.md`. |

## Missing (5)

Tabard requires; herold's spec does not currently mention:

| Requirement | Why it matters | Herold side change needed |
|-------------|----------------|---------------------------|
| `urn:ietf:params:jmap:sieve` (RFC 9007) — `Sieve/get`, `Sieve/set`, `Sieve/validate` over JMAP | Filters UI in `requirements/04-filters.md` requires it | Herold currently commits to **ManageSieve** (the standalone protocol on port 4190 — REQ-PROTO-50..52). The JMAP Sieve datatype is a separate capability and is not yet committed. Worth treating as additive: a JMAP datatype handler that wraps the existing Sieve interpreter and script storage. |
| `urn:ietf:params:jmap:calendars` (RFC 8984 + JMAP-Calendars binding draft) | tabard-calendar's substrate; iMIP RSVP in tabard-mail (`requirements/15-calendar-invites.md`) | Herold's NG3 currently says "groupware dropped entirely". Per the suite plan, this needs to flip: JMAP for Calendars is committed (CalDAV/CardDAV remain out — different protocol family). The forward-compat work in herold (`internal/store/types.go` `StateChange` shape, JMAP capability registry) sized for this. |
| `urn:ietf:params:jmap:contacts` (RFC 9553 + JMAP-Contacts binding draft) | tabard-contacts's substrate; recipient autocomplete in tabard-mail (`requirements/02-mail-basics.md` REQ-MAIL-11) | Same as calendars — flip NG3. |
| Image proxy at `<origin>/proxy/image?url=...` | `requirements/13-nonfunctional.md` REQ-SEC-07; without it external images stay blocked unconditionally | New endpoint on herold's HTTP listener. Full contract in tabard's `notes/server-contract.md` § Image proxy (resolved Q4): in-process v1, HTTPS-only upstream, header stripping, 24h cache cap, 25 MB per-fetch cap, per-user and per-origin rate limits, accurate-status-code failure mode. Slot into herold's `requirements/12-http-mail-api.md` or a new requirement. |
| iMIP REPLY pass-through in outbound queue | `requirements/15-calendar-invites.md` RSVP path | The outbound queue and `EmailSubmission/set` do not currently say anything about MIME parts of type `text/calendar`. Almost certainly works already (queue is body-shape-agnostic), but worth a one-line confirmation in herold's `requirements/01-protocols.md` to avoid future "we strip unknown MIME types" surprises. |

## Herold's NG3 — needs revising

**Current** (`/Users/hans/herold/docs/00-scope.md` NG3): "Groupware (CalDAV/CardDAV/WebDAV) dropped entirely. Users wanting calendar/contacts run Radicale / Baikal alongside."

**Required revision:** CalDAV/CardDAV/WebDAV stay out (DAV is not the substrate for tabard-calendar / tabard-contacts). **JMAP for Calendars and JMAP for Contacts move into scope** as part of the suite plan agreed with tabard. The architectural justification for choosing JMAP over DAV is recorded in tabard's `notes/server-contract.md`; the forward-compat work already done in herold's protocol architecture and storage schema means adding the JMAP datatypes is additive (no migration of existing tables).

The user is handling the NG3 update separately on the herold side.

## How tabard treats gaps

Per resolved Q14, tabard does **not** ship runtime feature-detection for required capabilities. The bootstrap reads the session descriptor and verifies the contract; missing capabilities surface in the About panel as deployment configuration errors, not as gracefully-degraded UI. The intent is that herold ships the full contract before tabard hits production; the About panel is for diagnosing the operator's misconfiguration.

The exception is the suite — tabard-calendar and tabard-contacts are not v1 of tabard-mail. Until those apps ship, `urn:ietf:params:jmap:calendars` and `urn:ietf:params:jmap:contacts` are advertised by herold but only used by tabard-mail's iMIP RSVP path and recipient autocomplete (respectively). Those features feature-detect: missing capability → RSVP buttons hidden, autocomplete falls back to seen-addresses history.
