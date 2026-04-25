# Herold coverage

What tabard requires of herold (per `server-contract.md`) and what herold's spec commits to. Last refreshed 2026-04-25 against `/Users/hans/herold/docs/`.

Per resolved Q14: herold ships before tabard implements. With the herold scope rev 4 commit (2026-04-25), the gaps surfaced by the prior audit are closed at the *requirements* level — implementation lands in herold's phase plan.

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

## Phasing summary

- **Herold phase 1** (already on the roadmap): everything tabard-mail v1 needs that wasn't in herold's prior scope — Sieve JMAP datatype, Mailbox.color, Identity.signature, EmailSubmission.sendAt with queue gating, iMIP REPLY pass-through, image proxy. Plus everything that was already in herold.
- **Herold phase 2**: snooze (already there), JMAP for Calendars/Contacts, LLM categorisation, chat (DMs, Spaces, ephemeral WebSocket, presence), 1:1 video calls (signaling + TURN mint).
- **Herold phase 3+**: nothing tabard requires.

## Notes

- "Committed" here means *the requirements doc commits to it*. Implementation lands in herold's phase plan; tabard's pre-implementation prep should track herold's implementation phase progress to schedule its own work.
- The implementation work in herold for phase 2 is non-trivial — chat alone is a substantial new feature surface (new datatypes, WebSocket endpoint, presence machinery, TURN credential mint, fanout integration). Herold's phasing doc (`/Users/hans/herold/docs/implementation/02-phasing.md`) is the source of truth for sequencing.
- coturn is operator-deployed, not bundled. Herold's deploy/ docs include a reference configuration; production deployments require operator-supplied TLS certificates and shared-secret rotation policy.
