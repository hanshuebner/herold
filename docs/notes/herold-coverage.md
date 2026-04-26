# Herold coverage

What tabard requires of herold (per `server-contract.md`) and what herold provides. Last refreshed 2026-04-26 against `/Users/hans/herold/docs/design/`.

Herold is operationally ready for tabard implementation purposes — every capability tabard relies on is committed in herold's spec (rev 1–8) and treated as available during tabard development. A locally running herold is assumed when developing and manually testing tabard (`../implementation/03-testing-strategy.md`, `apps/suite/README.md`).

## Committed (everything tabard v1 needs)

| Requirement | Herold reference |
|-------------|------------------|
| JMAP core (RFC 8620) — methods, batched calls, push, Blob/upload | `requirements/01-protocols.md` REQ-PROTO-40, `/jmap/eventsource` and `/jmap/upload/*` in the HTTP surfaces table |
| JMAP for Mail (RFC 8621) — `Email`, `Mailbox`, `Thread`, `Identity`, `EmailSubmission`, `SearchSnippet`, `VacationResponse` | `requirements/01-protocols.md` REQ-PROTO-41 |
| JMAP submission (`urn:ietf:params:jmap:submission`) — `EmailSubmission/set` tied to outbound queue | `requirements/01-protocols.md` REQ-PROTO-42 |
| EventSource push at `/jmap/eventsource` (RFC 8620 §7) | `requirements/01-protocols.md` REQ-PROTO-44 |
| Snooze: `$snoozed` keyword + `snoozedUntil` property + server-side wake-up | `requirements/01-protocols.md` REQ-PROTO-49 (full contract; phase 2) |
| `Authentication-Results` header on inbound mail (RFC 8601) | `requirements/06-filtering.md` REQ-FILT-03; `requirements/04-email-security.md` |
| Static asset serving from the same process | `requirements/08-admin-and-management.md` REQ-ADM-200 (admin UI precedent — same machinery serves tabard's bundle) |
| Login UI / OIDC redirect (RP only, not IdP) — sets a session cookie | `requirements/02-identity-and-auth.md` REQ-AUTH-50..58 |
| **`urn:ietf:params:jmap:sieve` JMAP datatype (RFC 9007)** | `requirements/01-protocols.md` REQ-PROTO-53 (phase 1; wraps existing ManageSieve store) |
| **`urn:ietf:params:jmap:calendars` capability (RFC 8984 + binding)** | `requirements/01-protocols.md` REQ-PROTO-54 (phase 2) |
| **`urn:ietf:params:jmap:contacts` capability (RFC 9553 + binding)** | `requirements/01-protocols.md` REQ-PROTO-55 (phase 2) |
| **`Mailbox.color` extension property** | `requirements/01-protocols.md` REQ-PROTO-56, `requirements/05-storage.md` REQ-STORE-34 |
| **Per-`Identity` signature property** | `requirements/01-protocols.md` REQ-PROTO-57, `requirements/05-storage.md` REQ-STORE-35 |
| **`EmailSubmission.sendAt` honoured by the outbound queue, with cancel-before-sendAt semantics** | `requirements/01-protocols.md` REQ-PROTO-58, `requirements/03-mail-flow.md` REQ-FLOW-63 |
| **iMIP REPLY pass-through (text/calendar MIME parts not stripped)** | `requirements/01-protocols.md` REQ-PROTO-59 |
| **Image proxy at `/proxy/image`** — auth, https-only upstream, header stripping, Content-Type validation, 25 MB cap, 24h cache, per-user rate limits | `requirements/12-http-mail-api.md` Part C, REQ-SEND-70..78 (phase 1) |
| **LLM-based automatic categorisation (distinct from spam)** | `requirements/06-filtering.md` Part C, REQ-FILT-200..231 (phase 2). Pipeline placement, per-account category set + prompt, $category-* keyword application, re-classification, failure isolation. |
| **Chat datatypes (`Conversation`, `Message`, `Membership`)** | `requirements/14-chat.md` REQ-CHAT-01..06 (phase 2). Net-new entity kinds in herold's storage; capability `https://tabard.dev/jmap/chat`. Additive on the existing JMAP capability registry and the open entity_kind enum — no migration of existing tables. |
| **Chat ephemeral WebSocket at `/chat/ws`** | `requirements/14-chat.md` REQ-CHAT-40..46, `architecture/08-chat.md` § Ephemeral channel protocol (phase 2). Carries typing, presence, WebRTC call signaling. |
| **TURN credential minting** | `requirements/15-video-calls.md` REQ-CALL-20..24 (phase 2). HMAC long-term-credential mechanism against a coturn shared secret; ~5 min TTL; mint over the chat WebSocket. |
| **Multi-user presence tracking** | `requirements/14-chat.md` REQ-CHAT-50..54 (phase 2). Server-derived from WebSocket connection state; "show as offline" mode supported. |
| **coturn deployment guidance** | `requirements/09-operations.md` § coturn, REQ-OPS-170..174. Reference configuration for both herold and coturn sides. |

## Server-side localisation

Server-generated text (vacation responder default, chat system messages, bounce DSN content, rate-limit error messages) is localised by herold based on the active locale tabard sends in the session. Tabard surfaces what herold returns; the en-US fallback covers strings herold hasn't localised yet. Locale set: en-US, en-GB, de-{DE,AT,CH}, fr-{FR,BE,CA,CH}. Cross-reference: `requirements/22-internationalization.md` REQ-I18N-13.

## Subsequent additions committed in herold rev 6–8

The same coverage table applies: rev 6 (email reactions REQ-PROTO-100..103 + REQ-FLOW-100..108), rev 7 (shortcut coach REQ-PROTO-110..114), rev 8 (Web Push REQ-PROTO-48 advanced + REQ-PROTO-120..127 + REQ-OPS-180..184). All committed.

## Notes

- This doc is a reference index — the implementation can assume herold provides all of the listed capabilities. Specific details live in herold's own requirement docs (linked in the table) or in tabard's `notes/server-contract.md`.
- coturn is operator-deployed, not bundled. Herold's deploy/ docs include a reference configuration; production deployments require operator-supplied TLS certificates and shared-secret rotation policy.
