# Implementation plan — herold mobile client

Milestones for the native mobile client (`00-scope.md`). Each milestone closes on an
**acceptance check** — an instrumented run against a real herold, not a pushed
commit — per the project's convergence discipline (`CLAUDE.md` § Verification
and done-state).

## Document tree to author

Mirrors `docs/design/web/`:

- `00-scope.md` — done (rev 1).
- `implementation-plan.md` — this file.
- `requirements/` — thin files. Cite suite `REQ-*` where behaviour is
  platform-independent; author new `REQ-AND-*` only for divergences:
  - `01-auth-and-token.md` — token grant, secure storage, biometric unlock.
  - `02-offline-and-sync.md` — local store, `Foo/changes` reconciliation, outbox.
  - `03-notifications.md` — FCM channels, direct-reply, conversation shortcuts.
  - `04-system-integration.md` — share intents, widgets, tiles, SAF, deep links.
  - `05-navigation-shell.md` — native nav stack, back/predictive-back, lifecycle.
  - Remaining mail behaviour cites suite `requirements/02..27` by REQ ID.
- `architecture/` —
  - `01-system-overview.md` — KMP module layout (`shared` / `androidApp`).
  - `02-jmap-client.md` — typed JMAP-over-bearer-token client (shared core).
  - `03-sync-and-state.md` — offline store, change feed, outbox reconciliation.
  - `04-push.md` — FCM registration and the herold push-gateway FCM transport.
  - `05-ui-shell.md` — Compose navigation, panes, theming.
- `notes/parity-matrix.md` — the continuous-follow instrument.
- `notes/server-contract.md` — the mobile client's pinned capability set,
  referencing `docs/design/web/notes/server-contract.md`.
- `notes/server-prerequisites.md` — the server-side work the mobile client
  depends on (bearer auth, FCM transport), owned by server agents.

## Server-side prerequisites (not mobile work)

Both are on `main` (#199, #200). They belong to server specialists, not the
mobile agent.

1. **Bearer-token auth grant.** The suite uses a same-origin session cookie;
   a native app cannot. herold exposes a token grant (OAuth2 auth-code /
   device flow, tied to G8 OIDC federation, or a scoped personal-access
   token). Owners: `directory-auth-implementor` + `http-api-implementor`.
2. **FCM transport in the push gateway.** herold's gateway currently emits
   RFC 8030/8291/8292 Web Push for the suite service worker; a native Android
   app receives via FCM. The gateway grows an FCM sender path reusing the
   existing per-subscription rule evaluation and coalescing. Owner:
   `queue-delivery-implementor` (push gateway) + `http-api-implementor`.

## Monorepo integration (contain the mobile toolchain)

The mobile toolchain must not leak into the Go pipeline:

- A path-filtered Forgejo Actions workflow (`.forgejo/workflows/mobile.yml`)
  triggered only on `mobile/**` and `docs/design/android/**`, on `main` and
  on `android-*` milestone branches. It builds the KMP project and runs the
  host-JVM unit tests; the emulator lane is #256.
- The mobile job is **not** in the `deploy` job's `needs:` chain; a mobile
  build failure never blocks the server auto-deploy.
- pre-commit hooks scoped by path: gofmt/goimports/staticcheck stay on Go
  paths; ktlint/detekt scope to `mobile/**`. Neither runs on the other's files.
- The Go binary embeds `web/dist` only. The mobile tree is a separately
  distributed APK/AAB and is not embedded; `go build` stays independent of
  `mobile/`.

## Verification model (the Android analog of the puppeteer rule)

The project requires UI changes be exercised in a live environment
(`CLAUDE.md` — puppeteer for the web SPA). The mobile analog:

- Compose UI tests + instrumented (Espresso/`createAndroidComposeRule`) runs
  on an emulator, driven against an ephemeral herold from
  `scripts/dev-instance.sh` (seeded principals
  `alice@example.local` etc., password `testpass123...`). The dev-instance
  contract is reused; no new fake server is built for the happy path.
- A milestone closes when its acceptance flow passes on the emulator against
  dev-instance, with a captured screenshot in the tracking ticket — not when
  a commit lands. Until a KVM-capable CI runner exists (#256), the emulator
  is the local AVD on the maintainer's development machine.
- Offline behaviour is tested by toggling the emulator's connectivity
  mid-flow and asserting the outbox drains correctly on reconnect.

## Milestones

The client is delivered in milestones. Each milestone is a short-lived
branch (`android-<milestone>`) off `main`, fast-forwarded into `main` when
its acceptance check is green on the emulator, then deleted. Server-side
changes a milestone needs land on `main` directly. Maintainer real-world
verification is batched: one session on the maintainer's phone per
milestone group, never one guessed fix at a time.

### Milestone 0 — foundations (done)

The design tree, the two server prerequisites (#199, #200), the KMP project
skeleton, the build + unit-test CI lane. Acceptance met (#251): the app
authenticates against dev-instance and renders the mailbox list.

### Milestone 1 — replace the Gmail-over-IMAP setup

Goal: one signed APK on the maintainer's Play-enabled phone that receives
mail by push within seconds, reads threaded conversations with category
tabs and labels, sends from any identity with attachments and rich text,
and searches the whole mailbox. Cache-first (G3): the app opens instantly
from the local store; compose, actions and search need connectivity.

- **1a — foundation and read path.** Sign-in with email, password and TOTP
  through the device-token grant; token in Keystore-backed storage; base URL
  defaulting to the production host; cleartext HTTP only in debug builds.
  SQLDelight schema keyed by JMAP account id: mailbox, thread, email,
  identity, per-type state strings, blob cache under a size budget. Sync
  engine walking every account in the session descriptor with `Foo/changes`
  per type and EventSource in the foreground. Threaded inbox with pinned
  category tabs and bundled category rows, combined across accounts, with the
  account scope switcher. Thread view rendering sanitised HTML with inline
  images via the image proxy. Star, archive with undo, mark read, label
  apply, snooze picker, all online-optimistic with revert on failure.
  Acceptance: seeded mail, category tabs, an opened thread and
  swipe-archive-with-undo on the emulator against dev-instance.
- **1b — push and release pipeline.** Firebase Messaging with the FCM token
  registered as a `PushSubscription` of kind `fcm`, carrying the suite's
  rules and quiet hours; per-kind notification channels; thread-grouped
  notifications with Archive and Mark Read actions; tap-through to the
  thread. FCM service-account key deployed to production. Release signing
  with a keystore held as CI secrets; the mobile workflow signs a release
  APK and attaches it to a Forgejo release on an `android-v*` tag.
  Acceptance: a push emitted by dev-instance reaches the emulator; a tagged
  CI run produces an installable signed APK.
- **1c — compose and search.** New, reply, reply-all and forward with
  quoting and threading headers; identity picker across accounts;
  attachments via the system picker uploaded to the JMAP blob endpoint;
  rich-text body editing; server-side drafts; `EmailSubmission`. Search
  running `Email/query` with the suite's text filter and Trash/Junk
  exclusion plus `SearchSnippet/get` online, and filtering the local cache
  with a visible "cached results only" scope offline. Acceptance: a message
  composed with an attachment on the emulator arrives in the dev-instance
  SMTP sink; a search for a seeded subject returns its thread.

Milestone 1 closes with one real-world session on the maintainer's phone.

### Milestone 2 — offline, second push transport, hardened auth

- **2a — durable outbox (#351, #354).** On branch `android-m2a`: a SQLDelight
  `outbox` table holding actions, drafts and sends; the sync engine as the
  sole drainer, in order per account, with backoff on transient failures and
  revert-and-retain on a refusal; attachments spooled into app-private
  storage; a WorkManager drain under a network constraint; the outbox screen
  and the connectivity chip; the undo window after Send
  (`requirements/02-offline-and-sync.md` REQ-AND-SYNC-20..26, 30, 31).
  Acceptance: three things queued with the radios off, found intact after
  the process is killed, drained on reconnect and confirmed through an
  independent JMAP client; a refused send retained with its reason; an
  undone send that never reaches the server.
- **2b — hardened auth (#352).** On branch `android-m2b`: sign-in through
  herold's OAuth2 authorization-code grant with PKCE in a Custom Tab, on the
  private-use redirect `com.netzhansa.herold:/oauth2/callback`; an expiring
  access token with a rotating refresh token, refreshed single-flight ahead of
  expiry and once on a `401`; opt-in biometric / device-credential unlock
  gating the token on launch and after an idle period; the account's active
  sessions with remote revoke; sign-out revoking the grant server-side
  (`requirements/01-auth-and-token.md` REQ-AND-AUTH-01/02/04/11/20/21/22).
  The device-token form stays as a debug-build fallback for the instrumented
  harness. Acceptance: the whole Custom Tab round trip including the TOTP step
  on the emulator's browser; a deleted access token refreshed silently; a
  revoked grant landing back on sign-in; the sessions screen marking this
  device and a second session's revoke signing the app out; the app locked on
  a fresh process and released by the device credential.
- **2c — UnifiedPush (#229).** On branch `android-m2c`: the second push
  transport, so a phone without Google Play Services is served. The
  UnifiedPush connector discovers the installed distributors and takes the
  user's pick; the client mints its own RFC 8291 key pair and auth secret,
  registers the distributor's endpoint as `kind: "unifiedpush"` (#236), and
  decrypts the aes128gcm envelope in the shared module, which routes the
  `StateChange` into the same notification seam FCM feeds. Settings carries
  "Push transport: Automatic / FCM / UnifiedPush"; a switch destroys the
  subscription held over the old transport before registering the new one
  (`requirements/03-notifications.md` REQ-AND-PUSH-04/05). Acceptance: on
  the emulator, against the in-tree distributor of `mobile/fakeDistributor`,
  the registration read back from the server as kind unifiedpush, a mail
  delivered over SMTP arriving as a decrypted notification, a 410 costing
  the subscription, and a transport switch leaving no stale subscription.

### Milestone 3 — organise and system integration

**3a — filters, LLM transparency, List-Unsubscribe (#361).** `ManagedRule`
joins the synced types with its own state string, and rule writes go out
through the durable outbox as their own entry kind; on success the drain
reads the account's rule set back, which is what gives a create its
server-assigned id and lets the server keep ownership of the shapes it
composes for `Thread/mute` and `BlockedSender/set`. Filters is a drawer
destination over that rule set, with a structured editor over the server's
closed condition and action vocabularies and the hand-written Sieve script
shown read-only. The thread overflow carries mute, block, "Create filter
from this message" and "Why is this here?"; Settings carries "How herold
sorts your mail". The Unsubscribe affordance follows the suite's mechanism
priority, and the one-click POST goes out on the plain HTTP client so it
carries no bearer token and no cookie. Acceptance: on the emulator against
`scripts/dev-instance.sh`, a filter created on the phone read back through
`ManagedRule/get` and filing a delivered message under its label and out of
the inbox, a reorder and a disable round-tripping, a mute writing the
server's thread-id rule, the transparency page rendering the instance's
prompts, and the one-click POST recorded by an in-process TLS sink with no
`Cookie`, `Referer` or `Authorization` header.

**3b — system integration.** Share intents, widgets, tiles, deep links
(`requirements/04-system-integration.md`).

### Milestone 4+ — sibling apps

Contacts, calendar, chat + 1:1 video calls, tracking each suite sibling app
as it ships. Not started until the suite's own sibling apps exist.

## Roster and process change

- Add a `mobile` specialist agent (`AGENTS.md`) owning `mobile/` and
  `docs/design/android/` and the parity matrix, analogous to
  `web-frontend-implementor` owning `web/`.
- **Parity discipline.** A suite change that touches user-facing behaviour
  also updates `notes/parity-matrix.md` and, if presentation-level, files an
  `android-parity` ticket (create the label). This applies the existing
  convergence discipline ("the diff and the passing test are the record")
  across the two clients; the matrix is the audit surface for drift.
