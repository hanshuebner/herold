# 29 — Apps and thread export (v1)

*(Added 2026-10-13.)*

An **app** is an external web application that a user can hand mail to from the suite. The operator registers the app; the app publishes a manifest declaring the actions it offers; the suite renders those actions natively; when the user picks one, the herold **server** exports the relevant messages, POSTs them to the app over a signed server-to-server request, and the suite opens the URL the app returns. The first extension point is a per-thread action (`thread.action`); the model is built to take further extension points and scopes without changing its shape.

The motivating case: a club board member receives donation offers by mail, exchanges a few messages with the donor, and then creates a donation offer in the club's forum. The forum publishes a "Create donation offer" thread action; herold sends it the thread's raw messages; the forum answers with a one-time link that opens its pre-filled draft.

This file also specifies **thread export** — downloading every message of a thread as `.eml` files in one zip — which is both a user-facing download and the building block apps use to receive mail.

Web-side counterpart: `../../web/requirements/28-app-actions.md` (REQ-APPS-*) for the action menu, the invocation UX and the admin Apps page; `../../web/requirements/02-mail-basics.md` REQ-MAIL-143 for the thread download.

## Scope and rationale

- **The suite stays content-blind** (`STANDARDS.md` §4.1): message bodies leave herold only from the server, to an origin the operator approved, as the direct result of a user's click. The browser never sends a body to the app.
- **Apps run nowhere inside herold.** No app code executes in the suite or the server. An app is reached over HTTPS only; it contributes declarative UI (a label and an icon) and an endpoint. This keeps the trust boundary where `11-plugins.md` draws it: plugins are operator-installed local executables with no UI and no state access; apps are remote services with a narrow, declared data grant.
- **The data grant is per invocation and per thread.** An app with the `thread.raw` scope receives the raw messages of the one thread the user invoked it on, at the moment of the click. It holds no token, cannot read the mailbox on its own, and gets no herold credentials (consistent with NG11 — herold issues no tokens to third-party apps).
- **Identity in the app is the app's business.** Herold asserts which principal invoked the action inside the signed request; the app decides whether and how to map that to its own accounts. The returned URL is opened in the user's browser, where the app's own login session applies.
- Precedents: Gmail HTTP add-ons (contextual triggers, server-side callbacks, `openLink` responses), Slack message shortcuts (signed POST, synchronous response), Outlook add-in manifests (typed extension points, versioned requirement sets), Atlassian Forge egress allowlists, WOPI/Drive "Open with" handoffs (opaque, short-lived links).

## Thread export

- **REQ-TEXP-01** A shared server-side exporter returns the raw RFC 5322 messages of one thread for one account, in ascending `receivedAt` order. It is the single implementation behind the thread download (REQ-TEXP-10) and app invocation (REQ-APP-40); neither re-implements thread resolution.
- **REQ-TEXP-02** The message set is the thread's messages that the requesting principal can read in the addressed account, after access-control filtering for shared accounts (`15-access-control` posture, the same filter `Thread/get` applies). Messages whose every mailbox has the `trash` or `junk` role are excluded unless the thread contains no other messages.
- **REQ-TEXP-03** Messages with an identical blob hash are exported once.
- **REQ-TEXP-04** The exporter streams blob bytes from the blob store; it never buffers a whole thread in memory. Callers that need a bound (REQ-APP-42) enforce it while streaming and abort with a typed error once exceeded.
- **REQ-TEXP-10** `GET /jmap/download-thread/{accountId}/{threadId}/{name}` on the public listener returns the export as `application/zip`. Authorization is identical to `/jmap/download/...` (session cookie or Bearer key carrying `mail.receive`). The session capability `https://netzhansa.com/jmap/thread-export` advertises it as `{"downloadUrl": "/jmap/download-thread/{accountId}/{threadId}/{name}"}`, using the same variable syntax as the core `downloadUrl`.
- **REQ-TEXP-11** Each zip entry is one message, named `NN-<yyyy-mm-dd>-<sanitised-subject>.eml` where `NN` is the 1-based position zero-padded to the width of the message count and the date is the message's `receivedAt` in UTC. Subject sanitising follows the `.eml` download (web REQ-MAIL-141): truncated to 80 characters, path separators and control characters removed, fallback `message`. Entries use Deflate.
- **REQ-TEXP-12** The response sets `Content-Disposition: attachment` with the `{name}` from the URL (RFC 5987 encoded) and `Cache-Control: private, no-store`. An unknown thread, or one with no exportable message, returns `404`.
- **REQ-TEXP-13** Thread export is not rate-limited separately; it counts against the account's existing download limits (REQ-STORE-20) by total bytes.

## The app object

- **REQ-APP-01** An app carries: `id` (server-assigned), `app_key` (the manifest's stable reverse-DNS identifier, REQ-APP-21), `origin` (scheme + host + port; every URL the app is reached at lies under it), `manifest` (the approved manifest snapshot) and its `manifest_digest` (SHA-256 of the canonical JSON), `pending_manifest` / `pending_manifest_digest` (a fetched manifest awaiting approval, REQ-APP-12), `secret` (the signing secret, REQ-APP-30), `state` (REQ-APP-11), `all_principals` (boolean) plus an explicit set of enabled principals (REQ-APP-13), `created_at`, `updated_at`, `last_invoked_at`.
- **REQ-APP-02** Schema additions (forward-only, SQLite + Postgres parity):
  - `apps(id, app_key UNIQUE, origin, manifest_json, manifest_digest, pending_manifest_json, pending_manifest_digest, secret_sealed, state, all_principals, created_at_us, updated_at_us, last_invoked_at_us)`.
  - `app_principals(app_id FK → apps.id ON DELETE CASCADE, principal_id FK → principals.id ON DELETE CASCADE, PRIMARY KEY (app_id, principal_id))`.
- **REQ-APP-03** Apps are application config (`00-scope.md` § Config split): they live in the DB and change live through the admin REST API, CLI and admin SPA, never through `system.toml`.

## Registration and approval

- **REQ-APP-10** Admin REST (admin listener, admin scope + elevation, REQ-AUTH-74): `GET /api/v1/apps`, `POST /api/v1/apps {origin}`, `GET /api/v1/apps/{id}`, `PATCH /api/v1/apps/{id} {all_principals?, principals?}`, `POST /api/v1/apps/{id}/approve {manifest_digest}`, `POST /api/v1/apps/{id}/refresh`, `POST /api/v1/apps/{id}/rotate-secret`, `POST /api/v1/apps/{id}/disable`, `POST /api/v1/apps/{id}/enable`, `DELETE /api/v1/apps/{id}`. The CLI mirrors them as `herold app list|add|show|set|approve|refresh|rotate-secret|disable|enable|remove`. Every mutation is audit-logged (REQ-ADM-300) with `subject="app:<app_key>"`.
- **REQ-APP-11** States: `pending_approval` → `active` (approve) → `disabled` (disable) → `active` (enable). `POST /api/v1/apps` fetches the manifest (REQ-APP-20), creates the app in `pending_approval` with the fetched manifest as `pending_manifest`, generates the secret, and returns the app including the plaintext secret **once**. Approval MUST quote the `manifest_digest` the admin reviewed; a mismatch with the current `pending_manifest_digest` is rejected with `409`, so a manifest that changed between review and approval is never approved blindly.
- **REQ-APP-12** `refresh` re-fetches the manifest. An identical digest is a no-op. A different digest is stored as `pending_manifest`; the app keeps serving its approved `manifest` until the admin approves the new digest. The admin REST response and the admin SPA show the difference (added/removed extensions, added/removed scopes, changed endpoints).
- **REQ-APP-13** An app's actions are offered to a principal when the app is `active` and either `all_principals` is true or the principal is in `app_principals`. New apps start with `all_principals = false` and no principals.
- **REQ-APP-14** App management requires the data key (`[server.secrets].data_key_ref`); without it the apps REST surface returns `503` with a problem type naming the missing key, and the capability (REQ-APP-50) is not advertised — the same degradation as IMAP import.

## Manifest

- **REQ-APP-20** The manifest is fetched with `GET {origin}/.well-known/herold-app.json` through the outbound guard of REQ-APP-60. The response MUST be `application/json`, at most 64 KiB, and parse against the v1 schema; anything else fails registration or refresh with a problem response carrying the validation errors.
- **REQ-APP-21** Manifest v1:
  ```json
  {
    "herold_app": 1,
    "app_key": "de.classic-computing.forum.donations",
    "name": {"en": "VzEkC donation offers", "de": "VzEkC-Spendenangebote"},
    "extensions": [
      {
        "point": "thread.action",
        "id": "donation-offer",
        "label": {"en": "Create donation offer", "de": "Spendenangebot erstellen"},
        "icon": "gift",
        "scopes": ["thread.raw"],
        "endpoint": "/vzekc-verlosung/herold/actions/donation-offer"
      }
    ]
  }
  ```
  - `herold_app` is the manifest API version. A manifest with a version the server does not implement is rejected.
  - `app_key` is a reverse-DNS identifier, `[a-z0-9.-]{3,100}`, and MUST equal the stored `app_key` on refresh.
  - `name` and `label` are localized maps keyed by BCP 47 language tag; `en` is required. Labels are at most 40 characters, names at most 60.
  - `icon` is one of the suite's app-action icon set (web REQ-APPS-04); an unknown icon falls back to the generic app icon.
  - `endpoint` is an absolute path resolved against `origin`; it MUST NOT carry a scheme, host, query or fragment.
  - `id` is unique within the manifest, `[a-z0-9-]{1,40}`.
- **REQ-APP-22** Extension points in v1: `thread.action`. Scopes in v1: `thread.raw`. An extension with an unknown `point` is dropped with a validation warning shown at review (forward compatibility: a newer app keeps working on an older server). An unknown scope rejects the whole manifest — a grant the server cannot explain is never approved.
- **REQ-APP-23** Future extension points (`message.action`, `compose.action`) and scopes (`thread.metadata`, `message.raw`) are added by spec change with a new REQ entry each; adding one does not bump `herold_app`. A breaking change to an existing point or to the request/response contract bumps `herold_app`, and the server keeps accepting the previous version for one major release.

## Signing secret

- **REQ-APP-30** The secret is 32 bytes from a CSPRNG, presented once as base64url, and stored sealed (`secrets.Seal`, `internal/secrets`). It is never logged, never returned after create/rotate, and never sent to the suite.
- **REQ-APP-31** `rotate-secret` generates a new secret, returns it once, and discards the previous one; every invocation from that point on is signed with the new secret. An app that needs a changeover window accepts both secrets on its side until it has stored the new one.

## Invocation

- **REQ-APP-40** JMAP method `AppAction/invoke` (capability REQ-APP-50), arguments `{accountId, appId, extensionId, threadId}`. It is accepted only on interactive sessions (session cookie with `end-user` scope); Bearer API keys are rejected with `forbidden`, because an invocation releases mail on the strength of a user's click. Steps:
  1. Resolve the app and extension; reject unless the app is `active`, enabled for the principal (REQ-APP-13), and the extension's point is `thread.action`.
  2. Resolve the thread in `accountId` through the thread-export access rules (REQ-TEXP-02); reject with `notFound` otherwise.
  3. Build the request body (REQ-APP-41) by streaming the export (REQ-TEXP-01), enforcing `max_payload_bytes` (REQ-APP-42).
  4. Sign (REQ-APP-43) and POST to `origin + endpoint` through the outbound guard (REQ-APP-60).
  5. Validate the response (REQ-APP-44) and return it.
- **REQ-APP-41** The request body is `multipart/form-data` with:
  - a first part named `context`, `application/json`:
    ```json
    {
      "herold_app": 1,
      "invocation_id": "0f7a…",
      "issued_at": "2026-10-13T08:12:00Z",
      "app_key": "de.classic-computing.forum.donations",
      "extension_id": "donation-offer",
      "user": {"email": "alice@example.local", "display_name": "Alice Example"},
      "account": {"email": "alice@example.local"},
      "thread": {"id": "t4383", "subject": "Old computers to give away", "message_count": 2},
      "messages": [{"index": 1, "received_at": "2026-10-12T16:29:57Z", "size": 3612345}]
    }
    ```
  - one part per exported message, named `message[]`, `Content-Type: message/rfc822`, `filename="NN.eml"`, in export order. The `[]` suffix makes form parsers that collapse repeated field names (Rack, PHP) collect every part into a list.
- **REQ-APP-42** System config `[server.apps]`: `enabled` (default `true`), `max_payload_bytes` (default 50 MiB), `invoke_timeout` (default 15 s), `allowed_insecure_origins` (default empty; REQ-APP-61). A thread whose export exceeds `max_payload_bytes` fails the invocation with `tooLarge` before any byte is sent to the app.
- **REQ-APP-43** Request headers: `Herold-Signature: t=<unix-seconds>,v1=<hex HMAC-SHA256(secret, "v1:" + t + ":" + body)>`, `Herold-Invocation-Id`, `Herold-App-Api: 1`, `User-Agent: herold/<version>`. The HMAC key is the secret's base64url text exactly as presented to the admin (REQ-APP-30), so the app operator pastes it into the app unchanged. The signature covers the exact body bytes. Apps MUST verify it in constant time and reject requests whose `t` is more than 300 s from their clock; apps SHOULD reject a repeated `Herold-Invocation-Id`. Herold does not retry an invocation.
- **REQ-APP-44** The app answers `2xx` with `application/json`, at most 64 KiB:
  - `{"type": "open_url", "url": "<https URL>"}` — the URL MUST lie under the app's `origin`; anything else is treated as an invalid response. The suite opens it (web REQ-APPS-11).
  - `{"type": "notice", "text": "<plain text, ≤ 500 chars>"}` — shown to the user as a toast; no URL is opened.
  - A non-`2xx` response MAY carry `{"error": "<plain text, ≤ 500 chars>"}`; the method fails with `appRejected` and passes the text through. Timeouts, transport errors, invalid responses and `5xx` without a body fail with `appUnavailable`.
- **REQ-APP-45** Every invocation is audit-logged with `action="app.invoke"`, `actor=principal/<id>`, `subject="app:<app_key>/<extension_id>"`, the thread id, message count, request bytes, the app's status code, duration and outcome. Subjects, addresses and bodies are not logged. `last_invoked_at` is updated on success.

## Wire (JMAP)

- **REQ-APP-50** Capability `https://netzhansa.com/jmap/apps`, account-level, advertised only when apps are enabled (REQ-APP-42), the data key is present (REQ-APP-14), and the principal has at least one offered action (REQ-APP-13). Value: `{"actions": [{"appId", "extensionId", "point", "appName": {...}, "label": {...}, "icon"}]}`. It carries no origin, endpoint or secret. Any change to the principal's offered actions changes the session `state`, so the suite refetches the session (RFC 8620 §2).
- **REQ-APP-51** `AppAction/invoke` responses: `{"type": "openUrl", "url"}` or `{"type": "notice", "text"}`. Method-level errors: `notFound`, `forbidden`, `tooLarge`, `appRejected` (with `description` = the app's error text), `appUnavailable`.

## Outbound guard

- **REQ-APP-60** Manifest fetches and invocations go through `internal/netguard` with `AllowedHosts` = the app's origin host, `AllowedPorts` = its port, `RequireHTTPS = true`, private and loopback destinations refused, no redirects followed, and the dial and total timeouts of REQ-APP-42.
- **REQ-APP-61** `allowed_insecure_origins` lists origins (e.g. `http://127.0.0.1:18080`) exempt from `RequireHTTPS` and the private-address refusal. It exists for the dev instance and CI fakes and is empty in production configs.

## Test fake and end-to-end coverage

- **REQ-APP-70** An in-tree fake app (`internal/testfakes/fakeapp`, binary `cmd/herold-fakeapp`) serves a manifest, verifies `Herold-Signature`, records each invocation (context JSON, part count, part sizes, signature validity) for inspection over `GET/DELETE /requests`, and answers per extension: `open_url` to its own `/opened/<invocation_id>` page, `notice`, `error 4xx`, `5xx`, and a slow response past the timeout.
- **REQ-APP-71** `scripts/dev-instance.sh` starts the fake app, lists its origin in `allowed_insecure_origins`, registers and approves it, and enables it for `alice@example.local`, seeding a two-message thread for her.
- **REQ-APP-72** One CI end-to-end test drives the whole flow against the fake: thread download zip contents, `AppAction/invoke` → fake receives a correctly signed request with the thread's messages in order → `openUrl` under the fake's origin; plus the `notice`, `appRejected`, `appUnavailable` (timeout) and `tooLarge` paths. Store-touching tests run on SQLite and Postgres.

## Security review notes

- Untrusted inputs: the manifest and the invocation response (both parsed as bounded JSON against a schema), the app-supplied `url` (origin-checked), and the app's error/notice text (rendered as plain text only).
- SSRF: all app traffic is pinned to the registered origin through `netguard`; redirects are not followed.
- Exfiltration is bounded by four conditions that must all hold: an admin registered and approved the app, the admin enabled it for the principal, the approved manifest grants `thread.raw`, and the user clicked the action on that thread from an interactive session.
- Manifest swap: approval is bound to a digest; a changed manifest never takes effect without a new approval.
- Replay: the signature covers a timestamp checked against a 300 s window plus a unique invocation id.
- Open redirect: `open_url` must be under the app's origin.

## Open questions

- Whether `thread.action` should also be offered on multi-select in the thread list (several threads in one invocation). v1 is single-thread.
- Whether principals may later register their own apps (self-service) with a per-principal approval, in addition to operator-registered ones.
