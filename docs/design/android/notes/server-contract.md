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
and the EventSource connection, and obtains that token through herold's OAuth2
authorization-code grant with PKCE (`server-prerequisites.md` #199,
`internal/protoadmin/oauth2_native.go`). All other session semantics — scope
enforcement (`REQ-AUTH-SCOPE-*`), idle-expiry, session management
(`REQ-AS-30..34`) — apply to the bearer-authed session identically.

**Redirect URI.** `com.netzhansa.herold:/oauth2/callback`, an RFC 8252 §7.1
private-use scheme claimed by an exported activity. The scheme is the app's
reversed domain, the form with a single slash and no authority, which Go's
`net/url` parses as a hierarchical URI so the authorization endpoint appends
`code` and `state` to it cleanly. The server matches a private-use redirect
byte-for-byte (`directory.ValidateRedirectURI`), so the registered string and
the one the client sends are identical. An App Link was not needed: the client
must work against any herold host the user types, and an App Link binds to one.

**Client registration.** The client is public — no secret, PKCE is what
secures it. An operator registers it once per instance, with an admin session:

    curl -X POST https://<host>/api/v1/oauth2/clients \
      -H "Authorization: Bearer <admin api key>" \
      -H "Content-Type: application/json" \
      -d '{
            "client_id": "herold-android",
            "name": "herold Android",
            "redirect_uris": ["com.netzhansa.herold:/oauth2/callback"]
          }'

Omitting `scopes` grants the default end-user set (`auth.AllEndUserScopes`);
the grant never issues an admin-scoped token. `scripts/dev-instance.sh` runs
exactly this call after seeding and prints the id as `OAUTH2_CLIENT_ID`.

**Token lifecycle.** The access token is the `hk_` bearer credential with a
one-hour expiry (`directory.AccessTokenTTL`); the refresh token rotates within
a 30-day family and its reuse revokes the family. Revoking a family deletes the
paired access token immediately, so a remote revoke takes effect on the client's
very next request rather than at the next expiry.

**Sessions.** `GET /api/v1/auth/credentials` and
`DELETE /api/v1/auth/credentials/{kind}/{id}` — the endpoints the Suite's
session management uses (issue #224). The list marks the credential the
request authenticated with as `is_current` (issue #356), which is how the
client knows which entry is this device.

**Step-up.** A self-service operation the server elevates (REQ-AUTH-78:
create an API key, create an app password, change the password, disable
TOTP) answers a bearer caller with `403 step_up_required` /
`elevation_scope: "self-service"`. `POST /api/v1/auth/step-up` with
`{totp_code}` elevates the calling credential - a device token or an
OAuth2 access token - for the same window a cookie session gets, and
answers `{elevation_expires_at}` (REQ-AUTH-79, issue #357). A wrong code
is a `401`, a locked-out attempt a `429`, and a principal with no TOTP
enrolled a `400` carrying `enroll_required`. Revoking a credential is
deliberately NOT elevated.

### Push (delta from § Web Push)

The Suite registers a Web Push subscription (`PushSubscription/set` with
`endpoint` + `p256dh`/`auth`). The mobile client registers an **FCM device
token** as a push subscription and receives via FCM
(`server-prerequisites.md` #200). The subscription's rule/quiet-hours/payload
contract (`notificationRules`, enriched-vs-minimal, coalescing by thread) is
unchanged; only the transport and the encryption/visibility properties differ.

The mail payload's sender arrives as `from`, the decoded display name, plus
`fromAddress`, the bare address (server issue #347). The client reads
`fromAddress` for the avatar lookup and falls back to splitting `from` as a
raw `From` header - decoding its RFC 2047 encoded words - when the field is
absent, so a payload from a server without that change still renders a name.

### Categorisation (no divergence from § Mailbox disposition and priority)

The mobile client reads `Mailbox.disposition` and `Mailbox.priority` from the
server (base contract § Mailbox disposition and priority) to decide inbox
lanes — pinned tabs (max 5), bundled rows, daily/weekly digests, filed
labels — and their order, identically to the Suite (#399).

A category is a label, and the two are joined by name: herold case-folds
keywords, so the messages of the label `Promotions` are the ones carrying
`$category-promotions`. A category the classifier derives for which the
principal holds no label therefore has no disposition and gets no lane; it
sits in the stream like any uncategorised message. The client writes both
properties with `Mailbox/set` through its outbox and surfaces the
`tooManyPinned` SetError as "at most 5 categories can be pinned".

### Identity alias addresses (no divergence from § Identity alias addresses)

The mobile client reads `Identity.aliases` (base contract § Identity alias
addresses, issue #387) identically to the Suite: a non-nullable array of
addr-spec strings, defaulting to empty, that select the owning identity as the
reply sender without ever becoming a From address. No mobile-specific handling.

### EventSource

Used identically to the Suite (server-contract § EventSource push) while the app
is in the foreground and connected: `GET /jmap/eventsource` carries `StateChange`
events; reconnect via `Last-Event-ID`. When the app is backgrounded or killed,
the OS closes the connection and FCM is the wake channel; on foreground the
client reopens EventSource and reconciles via `Foo/changes`
(`../architecture/03-sync-and-state.md`).

### Digital Asset Links (App Links)

Android verifies an App Link by fetching `https://<origin>/.well-known/assetlinks.json`
over HTTPS with no redirect, as `application/json`, unauthenticated. herold does
not serve that path today - the public mux registers `/.well-known/jmap` and
nothing else under `/.well-known/` (`internal/admin/server.go`) - so the
client's `autoVerify` filter for `mail.netzhansa.com` cannot pass until the
server serves this file verbatim:

```json
[
  {
    "relation": [
      "delegate_permission/common.handle_all_urls"
    ],
    "target": {
      "namespace": "android_app",
      "package_name": "com.netzhansa.herold.android",
      "sha256_cert_fingerprints": [
        "DF:1D:E8:CA:0B:62:34:FB:34:2F:49:32:F5:60:56:23:38:B7:A9:EC:A8:9D:CC:AE:A4:8F:3A:D1:B9:26:E3:72"
      ]
    }
  }
]
```

The fingerprint is the release signing certificate the mobile CI lane signs
with. A debug-signed build carries a different certificate and is driven with
an explicit package instead of verification.

What the client matches: the Suite routes a conversation on the fragment
(`https://<origin>/#/mail/thread/<threadId>`,
`web/apps/suite/src/lib/router/router.svelte.ts`), and an Android intent filter
matches on the path, which for such a URL is `/`. The client therefore
registers the origin's root and reads the fragment itself
(`requirements/04-system-integration.md` REQ-AND-SYS-11). Serving the file
under the root of the deployment origin covers every Suite link.

Owner: server (`http-api-implementor`); this is not mobile work.

## Cross-reference

Base contract and herold-side coverage: `docs/design/web/notes/server-contract.md`
and `docs/design/web/notes/herold-coverage.md`. When the base contract changes,
re-check this delta file for rows that are affected.
