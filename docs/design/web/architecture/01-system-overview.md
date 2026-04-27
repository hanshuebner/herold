# 01 — System overview

How the suite is shaped at the highest level.

## Shape

```
                              ┌────────────────────────────────────┐
                              │              browser               │
                              │  ┌──────────────────────────────┐  │
                              │  │       the suite shell     │  │
                              │  │  ┌────────┐ ┌─────────────┐  │  │
                              │  │  │  app   │ │ chat panel  │  │  │
                              │  │  │ route  │ │(persistent) │  │  │
                              │  │  └────────┘ └─────────────┘  │  │
                              │  └──────────────┬───────────────┘  │
                              └─────────────────┼──────────────────┘
                                                │
                  HTTPS / JMAP    EventSource    │    WebSocket
                                  (mail+chat     │    (chat ephemeral
                                   state)        │     + WebRTC signal)
                                                ▼
                              ┌────────────────────────────────────┐
                              │              herold                │
                              │  JMAP   |  EventSource  |  WS+TURN │
                              └────────────────────────────────────┘
```

The suite is **one** single-page application — the suite shell. It client-side routes between mail / calendar / contacts (each is a lazy-loaded module within the shell). The chat panel mounts once in the shell and persists across route changes. Three concurrent transports talk to herold:

- **HTTPS (JMAP)** for batched reads and writes.
- **EventSource** for durable state-change push (mail, chat conversations, chat messages, etc.).
- **WebSocket at `/chat/ws`** for chat ephemeral signals (typing, presence) and WebRTC call signaling.

## Layers (logical)

- **Transport.** One JMAP client object: batched method calls, token auth, HTTPS only. See `02-jmap-client.md`.
- **Sync.** State strings and `Foo/changes` per type, EventSource subscription for live updates, polling fallback when push drops. See `03-sync-and-state.md`.
- **Cache.** In-memory, normalised by JMAP type and ID. The single source of truth that views render from. Optimistic writes hit the cache first.
- **Views.** Three-pane layout (`../requirements/09-ui-layout.md`), keyboard-driven (`../requirements/10-keyboard.md`), HTML-mail rendered in a sandboxed iframe (`04-rendering.md`).
- **Keyboard engine.** Single global dispatcher, two-key sequence buffer, picker keymaps superseding the global map while open. See `05-keyboard-engine.md`.

## What lives client-side, what lives server-side

| Concern | Where |
|---------|-------|
| RFC 5322 / MIME parsing | Server (herold). The suite reads parsed JMAP `Email` properties. |
| Search index | Server (herold's Bleve). The suite issues `Email/query` and renders results. |
| Filter execution | Server (Sieve via RFC 9007). |
| Snooze wake-up | Server. (Reasoned in `../requirements/06-snooze.md`.) |
| Image proxying | Server. (`../notes/server-contract.md` § Image proxy.) |
| Optimistic UI state | Client. Reverts on server failure. |
| Keyboard handling | Client. |
| Drafts | Both. The suite composes locally with autosave to JMAP `Email` in the Drafts mailbox; the server persists. |

The principle: anything that has to survive a closed laptop, a second device, or a logged-out session goes to the server. Anything that's purely about how the active tab feels stays in the client.

## Bootstrap

The suite and herold are deployed at the same origin (resolved Q1). Herold serves both the suite's static assets and the JMAP API; the suite shares one auth surface.

1. Static assets load from the suite origin (served by herold).
2. The suite issues `GET /.well-known/jmap` with `credentials: 'include'`. The browser attaches suite-origin session cookie if one is present.
3. If herold returns 401 (no cookie or expired session), the suite redirects to `/login?return=<current-url>`. Herold's login surface authenticates the user (password+TOTP locally, or OIDC redirect through an external IdP — herold is not an IdP itself, only a relying party). On success herold sets a fresh session cookie and redirects back to the suite's URL.
4. With a valid session, `.well-known/jmap` returns the session descriptor.
5. The suite checks the descriptor's `capabilities` and `accountCapabilities` against `../notes/server-contract.md`. Herold is treated as a fully-provisioned peer; capabilities are assumed present, not feature-detected with fallbacks. A missing capability is a deployment configuration error and surfaces in the About settings panel as such.
6. The suite issues a single batched JMAP call for the initial view: mailboxes, identities, the inbox's first thread page, the inbox state strings, the user's filters list, the user's category configuration.
7. The suite opens the EventSource push channel (also `credentials: 'include'`).
8. The first paint shows the inbox.

## Concurrency

Single-threaded JS event loop. Long work (search snippet rendering, large HTML mail iframe load) yields cooperatively. No service worker (NG2). No worker thread in v1; revisit only if profiling shows main-thread stalls.

## State persistence

| What | Where | Lifetime |
|------|-------|----------|
| Auth | HTTP-only session cookie (set by herold) | Cookie's own expiry; cleared by herold logout endpoint |
| Sidebar collapsed/expanded | `localStorage` per account | Persistent |
| Pane split ratio | `localStorage` per account | Persistent |
| Recent search queries | `localStorage` per account | Bounded ring buffer (default 10) |
| Cache (mailboxes, threads, emails) | Memory only | Tab lifetime |
| Composed-but-unsaved compose state | `sessionStorage` | Tab close (drafts that auto-saved are server-side) |

Nothing else persists client-side. No IndexedDB, no service worker storage.

## Suite shape

The suite is **one SPA shell** with client-side routing. The shell hosts:

- `/mail/*` — the suite routes (default landing page).
- `/calendar/*` — the calendar app routes (future).
- `/contacts/*` — the contacts app routes (future).
- `/chat/*` — full-screen chat routes (a route used when the user wants chat to take the whole window — distinct from the persistent panel which is always present).
- The persistent **chat panel** is mounted once in the shell, persists across route changes.
- One JMAP client, one EventSource, one chat WebSocket — all owned by the shell, lifecycles span routes.

Cross-app navigation within the suite is **client-side routing**, not full-page reload (resolved R16-amended). The chat panel's connection state, JMAP session, and in-memory cache survive route changes.

Code organisation (eventual monorepo): `apps/suite` builds the shell. Per-app code lives in `apps/{mail,calendar,contacts,chat}` as packages the shell imports and lazy-loads. The package boundary is for code organisation, not for separate deployment — the suite ships as one bundle (with route-based code-splitting for the lazy-loaded app modules).

Per-app caches are isolated within the shell — mail's cache and chat's cache are separate JMAP-typed normalised stores, but both live in the same memory space. No cross-app cache sharing semantics; just don't trip over each other's namespaces.

Until the calendar app and the contacts app exist, their routes 404 with a "this app isn't deployed yet" page. The chat panel and `/chat/*` routes work as long as the chat capability is advertised by herold.
