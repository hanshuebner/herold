# Server contract (mobile)

What the mobile client expects herold to deliver. The **base contract is the
Suite's** `docs/design/web/notes/server-contract.md` — the same JMAP
capabilities, the same behaviours (snooze, categorisation, reactions, snippets,
delayed send, image proxy, chat), the same target scale. This file records only
where the mobile client's expectation **differs** from the Suite's, so the base
contract is read once and not duplicated.

## Capabilities required

Identical to the Suite's capability table (server-contract § JMAP capabilities
required): `urn:ietf:params:jmap:core`, `:mail`, `:submission`, `:sieve`,
`:contacts`, `:calendars`, and the `https://netzhansa.com/jmap/*` extensions
(snooze, categorise, chat, email-reactions, shortcut-coach, push). The mobile
client pins the same set. As with the Suite, capabilities are read once from the
session descriptor and pinned per session; a feature whose capability is not
advertised is removed from the UI.

Divergences from the Suite's pinned set:

- `https://netzhansa.com/jmap/shortcut-coach` is **not used on phone** — the
  coach is keyboard-shortcut coaching and phones have no keyboard (Suite
  `REQ-MOB-39`, `REQ-MOB-121`). The capability may be advertised; the phone UI
  ignores it. Tablet with a hardware keyboard is out of scope for v1 phone-first
  delivery and is revisited with the tablet layout.

## Divergences from the Suite contract

### Authentication (delta from § Auth and session)

The Suite uses a same-origin session cookie. The mobile client uses
`Authorization: Bearer <token>` on every JMAP method call, blob upload/download,
and the EventSource connection, and obtains that token via herold's token grant
(`server-prerequisites.md` #199). All other session semantics — scope
enforcement (`REQ-AUTH-SCOPE-*`), idle-expiry, TOTP step-up (Suite
`REQ-AS-10..27`), session management (`REQ-AS-30..34`) — apply to the
bearer-authed session identically; the mobile UI reacts to the same
`session_expired` / `session_revoked` / `step_up_required` responses.

### Push (delta from § Web Push)

The Suite registers a Web Push subscription (`PushSubscription/set` with
`endpoint` + `p256dh`/`auth`). The mobile client registers an **FCM device
token** as a push subscription and receives via FCM
(`server-prerequisites.md` #200). The subscription's rule/quiet-hours/payload
contract (`notificationRules`, enriched-vs-minimal, coalescing by thread) is
unchanged; only the transport and the encryption/visibility properties differ.

### EventSource

Used identically to the Suite (server-contract § EventSource push) while the app
is in the foreground and connected: `GET /jmap/eventsource` carries `StateChange`
events; reconnect via `Last-Event-ID`. When the app is backgrounded or killed,
the OS closes the connection and FCM is the wake channel; on foreground the
client reopens EventSource and reconciles via `Foo/changes`
(`../architecture/03-sync-and-state.md`).

## Cross-reference

Base contract and herold-side coverage: `docs/design/web/notes/server-contract.md`
and `docs/design/web/notes/herold-coverage.md`. When the base contract changes,
re-check this delta file for rows that are affected.
