# Implementation plan — herold mobile client

Phasing for the native mobile client (`00-scope.md`). Each phase closes on an
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

These block real-device testing and must land first. They belong to server
specialists, not the mobile agent.

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
  triggered only on `mobile/**` and `docs/design/android/**`. It builds the
  KMP project and runs unit + instrumented tests on an emulator.
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
- A phase closes when its acceptance flow passes on the emulator against
  dev-instance, with a captured screenshot in the tracking ticket — not when
  a commit lands.
- Offline behaviour is tested by toggling the emulator's connectivity
  mid-flow and asserting the outbox drains correctly on reconnect.

## Phases

- **Phase 0 — foundations.** These docs; the two server prerequisites; the KMP
  project skeleton (`shared`, `androidApp`); the mobile CI workflow; the
  dev-instance-driven emulator harness. Acceptance: the app authenticates
  against dev-instance and renders the account's mailbox list.
- **Phase 1 — read path.** Session/bootstrap, the sync engine + local store,
  thread list, reading pane (HTML render + inline images), push delivery and
  tap-through. Acceptance: new mail arrives via push, opens to the thread,
  and is readable offline after sync.
- **Phase 2 — write path.** Optimistic actions (archive/label/snooze/star/
  delete) with the offline outbox; compose (including the inline-vs-attach
  distinction, suite G8); drafts; `EmailSubmission`. Acceptance: compose and
  send offline, outbox drains on reconnect, recipient receives.
- **Phase 3 — organise + find.** Filters (Sieve), categorisation display and
  LLM transparency (suite G7), snooze, search. Acceptance: category chips and
  the per-message "the LLM was asked ..." inspect view match the suite.
- **Phase 4+ — sibling apps.** Contacts, calendar, chat + 1:1 video calls,
  tracking each suite sibling app as it ships. Not started until the suite's
  own sibling apps exist.

## Roster and process change

- Add a `mobile` specialist agent (`AGENTS.md`) owning `mobile/` and
  `docs/design/android/` and the parity matrix, analogous to
  `web-frontend-implementor` owning `web/`.
- **Parity discipline.** A suite change that touches user-facing behaviour
  also updates `notes/parity-matrix.md` and, if presentation-level, files an
  `android-parity` ticket (create the label). This applies the existing
  convergence discipline ("the diff and the passing test are the record")
  across the two clients; the matrix is the audit surface for drift.
