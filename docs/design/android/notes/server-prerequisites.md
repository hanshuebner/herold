# Server-side prerequisites

Server work the mobile client depends on. These are herold (Go) changes owned
by server specialists, not mobile work, and both gate mobile Phase 0 — until
they land, the app cannot authenticate against or receive push from a real
herold. Tracked as Forgejo issues in herold/herold.

## Bearer-token auth grant (#199)

https://code.netzhansa.com/herold/herold/issues/199

The Suite authenticates with a same-origin session cookie
(`credentials: 'include'`, `docs/design/web/architecture/02-jmap-client.md`,
`docs/design/web/notes/server-contract.md` § Authentication flow). A native app
has no same-origin cookie jar and uses `Authorization: Bearer <token>` instead —
the bearer path herold already keeps for non-browser clients (server-contract
§ Auth and session: "Bearer-token auth on JMAP endpoints stays available for
non-browser clients").

The prerequisite is the **grant** that mints the token: an OAuth2
authorization-code flow (system browser / Custom Tab -> herold login, password
+ TOTP or OIDC federation per G8 -> redirect back with a code -> exchange for a
bearer + refresh token), token refresh and revocation, and the same auth-scope
enforcement (`REQ-AUTH-SCOPE-01..04`), idle-expiry (`REQ-AUTH-72..76`), and TOTP
step-up (`REQ-AUTH-74`) rules that cookies get, applied to bearer tokens.
Bearer-authed sessions must also appear in and be revocable from the session
management surface (Suite `REQ-AS-30..34`).

Owners: `directory-auth-implementor` + `http-api-implementor`.

## FCM transport in the push gateway (#200)

https://code.netzhansa.com/herold/herold/issues/200

herold's push gateway currently delivers via Web Push (RFC 8030 transport,
RFC 8291 encryption, RFC 8292 VAPID) for the Suite service worker
(`docs/design/web/notes/server-contract.md` § Web Push). A native Android app
receives via Firebase Cloud Messaging, which differs in addressing (a device
registration token in a Firebase project, not an endpoint URL), provider auth (a
Google service-account credential, not VAPID), and payload visibility (Google
can read the payload; a Web Push provider cannot).

The prerequisite adds a second delivery backend under the existing dispatcher.
The dispatcher's shared work is unchanged — per-subscription rule evaluation
(`notificationRules`, quiet hours), enriched-vs-minimal payload construction,
and per-(subscription, thread) coalescing (server-contract § Web Push, Suite
`REQ-PUSH-40..47`). New: an FCM device token registered as a push-subscription
kind alongside Web Push subscriptions; an FCM sender that holds the Firebase
service-account credential in herold's secret handling, maps the computed
payload into an FCM message via the FCM HTTP v1 API, and destroys the
subscription on an FCM unregister response (mirroring the Web Push 410 path,
`REQ-PUSH-34`). Payloads to FCM stay minimal to preserve the "bodies are never
pushed" property (`REQ-PUSH-92`) given FCM's payload visibility.

Owners: `queue-delivery-implementor` (push gateway) + `http-api-implementor`.
