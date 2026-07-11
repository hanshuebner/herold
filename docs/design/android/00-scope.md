# 00 — Scope (Android / mobile client)

**2026-07-11** (rev 1): The herold mobile client is commissioned as a
native application tracking the web suite's mail feature set. This
**amends `docs/design/web/00-scope.md` NG1 and `requirements/24-mobile-and-touch.md`
REQ-MOB-02**, which declared native iOS/Android applications out of scope
for the suite. That non-goal was a decision about the *suite* (the web
SPA), taken when the PWA was the whole mobile answer. The mobile client is
a *separate deliverable* over the same herold JMAP server; the suite's PWA
and this native client coexist. The suite non-goals are not silently
contradicted — they are superseded for the mobile deliverable by this file.

## What the mobile client is

A native mobile application whose feature set converges on the web suite's
mail experience, and which continues to follow feature additions the suite
makes. Android ships first; an iOS client is plausible later and reuses the
shared core.

The mobile client is a **second independent client over the same server
surface** as the suite — RFC 8620 (Core), RFC 8621 (Mail), JMAP for
Calendars (RFC 8984) and Contacts (RFC 9553), EventSource/push, Sieve
(RFC 9007), plus herold's extensions (`SeenAddress`, `ShortcutCoachStat`,
`Email.reactions`, the chat WS protocol, the LLM-transparency contract).
The pinned capability set is `docs/design/web/notes/server-contract.md`;
the mobile client's own pinned set lives in `notes/server-contract.md` and
references it rather than re-deriving it.

**The shared contract between the two clients is the server surface, not
client code.** Nothing of the suite's TypeScript is ported. Parity is
maintained at the JMAP contract and tracked in `notes/parity-matrix.md`.

## Goals

- **G1. Feature-parity with the suite's mail experience**, calibrated the
  same way the suite is (the Gmail subset the user actually uses). Presentation
  is native and idiomatic to the platform; behaviour matches the suite's
  `REQ-*` where the requirement is platform-independent.
- **G2. Be a competent JMAP citizen.** RFC 8620/8621 conformant; incremental
  sync via `Foo/changes` + state strings; push via the platform push channel.
- **G3. Full offline.** The local store is the UI's source of truth. The app
  is usable with no connectivity: read synced mail, compose, and queue actions;
  reconcile on reconnect. This is the deliberate divergence from suite NG2
  (the suite is online-first with no offline) — a native mobile mail client
  that shows nothing on a dead connection is not acceptable.
- **G4. Optimistic UI.** Archive / label / snooze / star / delete update the
  screen before the server confirms; on failure they revert with a clear error
  and a Retry affordance. Same semantics as suite `11-optimistic-ui.md`,
  extended to survive an offline outbox.
- **G5. Perfect platform feature support.** First-day access to every relevant
  Android capability: FCM (data messages, direct-reply, conversation shortcuts,
  Bubbles), share intents, home-screen widgets, Quick-Settings tiles,
  Credential Manager / passkeys, BiometricPrompt, Storage Access Framework,
  per-app language, Material You dynamic colour, predictive back. This goal
  drove the toolkit choice (see Defaults).
- **G6. Single-user, single-account.** Matches suite NG3. One JMAP account;
  account switching is not a feature.
- **G7. Continuous follow.** When the suite adds a user-facing feature, the
  mobile client tracks it. Protocol-level additions (a new JMAP type, keyword,
  filter capability) are available by construction; presentation-level additions
  are the backlog, tracked per feature in `notes/parity-matrix.md`.

## Non-goals

- **NG1.** Multi-account UI. One JMAP account per install (suite NG3).
- **NG2.** Delegation, shared mailboxes, admin / multi-user views (suite NG4).
- **NG3.** The operator admin surface. The mobile client tracks the *suite*
  (consumer mail), never the admin SPA.
- **NG4.** S/MIME and PGP (suite NG5).
- **NG5.** AI compose / smart-reply / summarisation (suite NG7). The user does
  not want these.
- **NG6.** Third-party tracking, analytics, ad SDKs. Ever (suite NG9).
- **NG7.** Being a general IMAP/POP client for arbitrary servers. v1 targets
  herold, the same as the suite (suite Defaults).

## Defaults in force

- **Toolkit:** Kotlin Multiplatform. The **shared core** (JMAP client, sync
  engine, local store, domain models) is pure Kotlin; the **Android UI** is
  native Jetpack Compose. An iOS UI added later reuses the shared core. This
  shape gives identical, first-day Android platform access to a pure-native
  build while banking iOS optionality; it was chosen because "perfect platform
  feature support" ruled out cross-platform UI frameworks (which mediate
  platform access through plugins) but did not distinguish native from KMP.
- **Local store:** SQLDelight in the shared core. Local DB is the UI's source
  of truth; JMAP `Foo/changes` reconciles it on reconnect; an outbox persists
  offline sends and actions.
- **Push:** Firebase Cloud Messaging on Android. herold's push gateway
  (currently RFC 8030/8291/8292 Web Push for the suite SW) grows an FCM
  transport — see `notes/server-prerequisites.md`.
- **Auth:** bearer token over HTTPS. The suite authenticates with a
  same-origin session cookie (`credentials: 'include'`), which a native app
  cannot use; the mobile client obtains a token via herold's token grant,
  tied to G8 (OIDC federation). Server-side prerequisite — see
  `notes/server-prerequisites.md`.
- **Location:** the herold monorepo, top-level `mobile/` (KMP project:
  `shared`, `androidApp`, later `iosApp`). Design docs live here under
  `docs/design/android/`.
- **Platform support:** Android 10 (API 29) minimum, targeting the current
  Android SDK. Revisited per release.
- **Localisation:** English (US/GB), German (DE/AT/CH), French (FR/BE/CA/CH)
  at v1, matching suite `22-internationalization.md`.
- **Visual style:** Material 3 with dynamic colour; light and dark are equal
  peers following the system setting, with a fixed-theme override in settings
  (suite `20-settings.md` REQ-SET-01).

## Relationship to the suite (parity model)

The suite's requirements already separate *protocol/server* concerns from
*presentation* concerns. The mobile requirements exploit that split:

- Where a requirement is platform-independent (sync semantics, optimistic-UI
  rules, snooze/filter/categorisation models, push payload shape, LLM
  transparency contract), the mobile requirement **cites the suite `REQ-*`**
  and does not restate it.
- Where the platform genuinely diverges (native navigation, offline local
  store, FCM notifications, Android system integration, background sync,
  token auth), the mobile requirement is **new (`REQ-AND-*`)**.

`notes/parity-matrix.md` is the living instrument that keeps the two clients
converging: one row per user-facing suite feature, classified
protocol-vs-presentation, with the Android status and tracking ticket.
