# 05 — UI shell

The native Jetpack Compose UI in `androidApp`. It realises the Suite's phone
layout (`docs/design/web/requirements/24-mobile-and-touch.md` § Layout - phone)
natively and reads from the local store; it holds no server logic.

## Composition and navigation

- A single-activity Compose app with a navigation graph (Navigation Compose).
  Destinations: thread list, reading pane, compose, search, settings, and the
  drawer/bottom-sheet surfaces (`../requirements/05-navigation-shell.md`).
- The navigation stack is the Suite's phone stack model (`REQ-MOB-30`): push to
  drill in, pop to back out, with the system-back order of
  `REQ-AND-NAV-10`. Predictive back is wired through the platform back handler.
- The variant (phone now; tablet two/three-pane later) is provided via a Compose
  composition-local, mirroring the Suite's context-plumbed variant
  (`REQ-MOB-12`); components do not read window size directly.

## State and the store

- Screen state is exposed by view models that observe the local store
  (SQLDelight `Flow`s) and map rows to UI models. The UI is a function of the
  store; a reconciliation or outbox change updates the store and the affected
  screens recompose.
- User intents (open thread, archive, snooze, compose, send) call into the
  shared core: optimistic actions apply to the store and enqueue outbox entries
  (`03-sync-and-state.md`); the UI reflects the optimistic state at once.
- There is no UI-held server cache. Process death and restore reconstruct state
  from saved instance state plus the store (`../requirements/05-navigation-shell.md`
  REQ-AND-NAV-20).

## Message rendering

- The reading pane renders the thread accordion (Suite `09-ui-layout.md`
  REQ-UI-20..25) with mobile density. HTML bodies render in a sandboxed
  WebView-equivalent surface; inline images resolve through herold's image proxy
  (`docs/design/web/notes/server-contract.md` § Image proxy), with the same
  `img-src`-restricted, proxied fetch the Suite uses, and single-action save per
  inline image (`../requirements/04-system-integration.md` REQ-AND-SYS-31; Suite
  G16).
- Compose provides the inline-vs-attach distinction (Suite G15): pasted images
  inline, picker attaches, with reversible moves between body and attachment.

## Theming and insets

- Material 3 with dynamic colour; light/dark follow the system with a settings
  override (`../requirements/04-system-integration.md` REQ-AND-SYS-40, Suite
  `REQ-SET-01`).
- IME and safe-area insets are handled with Compose window-insets so the focused
  input stays visible and content clears cutouts and the navigation bar
  (`../requirements/05-navigation-shell.md` REQ-AND-NAV-22/23).
- Type scale is in scalable units honouring system font scaling
  (`REQ-AND-SYS-41`).

## Touch interaction

Gestures realise the Suite's touch model natively
(`../requirements/05-navigation-shell.md` REQ-AND-NAV-13): swipe quick-actions on
thread rows, long-press context menu / selection mode with haptics, pull-to-
refresh driving reconciliation, two-finger / edge navigation between threads.
Each gesture has a reachable button/menu equivalent (Suite `REQ-MOB-102`), and
TalkBack announces all of them (`REQ-AND-SYS-43`).

## Testing hook

The UI is exercised by Compose UI tests on an emulator against an ephemeral
herold from `scripts/dev-instance.sh` (`../implementation-plan.md` § Verification
model). Screens are testable in isolation by seeding the local store directly, so
UI tests do not require the network for deterministic layout/interaction
coverage.
