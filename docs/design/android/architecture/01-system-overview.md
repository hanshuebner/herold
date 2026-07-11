# 01 — System overview

The KMP project shape and how the layers fit. The mobile client is a second
independent JMAP client over herold; the shared contract is the server surface,
not Suite code (`../00-scope.md`).

## Module layout

The project lives at the repo's `mobile/` (a Gradle KMP build):

```
mobile/
  shared/            Kotlin Multiplatform library — no UI
    commonMain/
      jmap/          typed JMAP-over-bearer client (02-jmap-client.md)
      sync/          sync engine + outbox reconciliation (03-sync-and-state.md)
      store/         SQLDelight schema + queries (local source of truth)
      domain/        domain models (Thread, Email, Mailbox, Identity, ...)
      auth/          token acquisition, refresh, secure-storage interface
    androidMain/     android actuals (Keystore, connectivity, platform bits)
    iosMain/         ios actuals (added when the iOS app starts)
  androidApp/        native Jetpack Compose UI (05-ui-shell.md)
  iosApp/            SwiftUI over the shared core (later)
```

- **`shared`** holds everything that is not platform UI: the JMAP client, sync
  engine, local store, domain models, and the auth/token logic. It is pure
  Kotlin `commonMain`, with thin `expect`/`actual` seams for platform services
  (secure storage, connectivity, clock). This is the half an eventual iOS client
  reuses wholesale.
- **`androidApp`** is native Jetpack Compose. It reads the local store and drives
  the shared core; it owns all Android platform integration
  (`../requirements/04-system-integration.md`) and gets first-day, unmediated
  access to every Android API (`../00-scope.md` G5).
- **`iosApp`** does not exist at v1. Its later addition adds `iosMain` actuals
  and a SwiftUI layer over the same `shared`.

## Layering

```
  Compose UI  ─reads─▶  local store (SQLDelight)  ◀─writes─  sync engine
       │                                                        ▲
       └── issues intents (open thread, archive, compose) ──▶ outbox
                                                                │
   JMAP client ◀── sync engine drains outbox / reconciles ─────┘
       │
   herold  (bearer token; JMAP + EventSource + blob + FCM registration)
```

The UI never calls JMAP directly. It reads the local store and expresses intent
(navigate, act, compose); the sync engine is the sole reconciler between the
store and the server, and the sole owner of the outbox. This keeps the offline
model (`../requirements/02-offline-and-sync.md`) coherent: the store is always
renderable, the network is always asynchronous to the UI.

## Bootstrap

1. Resolve the account's bearer token from secure storage; if absent or
   unrefreshable, enter sign-in (`../requirements/01-auth-and-token.md`).
2. Render the UI immediately from the local store (empty on first run).
3. Fetch the JMAP session descriptor; pin capabilities
   (`../notes/server-contract.md`).
4. Start the sync engine: reconcile via `Foo/changes` from persisted state
   strings, open EventSource, drain the outbox.
5. Register the FCM token as a push subscription
   (`../requirements/03-notifications.md`).

## Relationship to herold and the Suite

herold is unchanged except for the two server prerequisites
(`../notes/server-prerequisites.md`: bearer grant #199, FCM transport #200).
Every other capability the client uses already exists for the Suite. Parity with
the Suite is tracked in `../notes/parity-matrix.md`.
