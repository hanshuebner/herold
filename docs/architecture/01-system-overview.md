# 01 — System overview

How tabard is shaped at the highest level.

## Shape

```
                              ┌──────────────────────┐
                              │       browser        │
                              │  ┌───────────────┐   │
                              │  │   tabard SPA  │   │
                              │  └───────┬───────┘   │
                              │          │           │
                              └──────────┼───────────┘
                                         │
                          HTTPS / JMAP   │   text/event-stream
                                         │   (push)
                                         ▼
                              ┌──────────────────────┐
                              │       herold         │
                              │  (JMAP + EventSource)│
                              └──────────────────────┘
```

Tabard is a single-page application served as static assets. After bootstrap it speaks JMAP to herold over HTTPS for everything: reads, writes, search, attachments, push.

## Layers (logical)

- **Transport.** One JMAP client object: batched method calls, token auth, HTTPS only. See `02-jmap-client.md`.
- **Sync.** State strings and `Foo/changes` per type, EventSource subscription for live updates, polling fallback when push drops. See `03-sync-and-state.md`.
- **Cache.** In-memory, normalised by JMAP type and ID. The single source of truth that views render from. Optimistic writes hit the cache first.
- **Views.** Three-pane layout (`../requirements/09-ui-layout.md`), keyboard-driven (`../requirements/10-keyboard.md`), HTML-mail rendered in a sandboxed iframe (`04-rendering.md`).
- **Keyboard engine.** Single global dispatcher, two-key sequence buffer, picker keymaps superseding the global map while open. See `05-keyboard-engine.md`.

## What lives client-side, what lives server-side

| Concern | Where |
|---------|-------|
| RFC 5322 / MIME parsing | Server (herold). Tabard reads parsed JMAP `Email` properties. |
| Search index | Server (herold's Bleve). Tabard issues `Email/query` and renders results. |
| Filter execution | Server (Sieve via RFC 9007). |
| Snooze wake-up | Server. (Reasoned in `../requirements/06-snooze.md`.) |
| Image proxying | Server. (`../notes/server-contract.md` § Image proxy.) |
| Optimistic UI state | Client. Reverts on server failure. |
| Keyboard handling | Client. |
| Drafts | Both. Tabard composes locally with autosave to JMAP `Email` in the Drafts mailbox; the server persists. |

The principle: anything that has to survive a closed laptop, a second device, or a logged-out session goes to the server. Anything that's purely about how the active tab feels stays in the client.

## Bootstrap

Tabard and herold are deployed at the same origin (resolved Q1). Herold serves both tabard's static assets and the JMAP API; the suite shares one auth surface.

1. Static assets load from the suite origin (served by herold).
2. Tabard issues `GET /.well-known/jmap` with `credentials: 'include'`. The browser attaches the suite-origin session cookie if one is present.
3. If herold returns 401 (no cookie or expired session), tabard redirects to `/login?return=<current-url>`. Herold's login surface authenticates the user (password+TOTP locally, or OIDC redirect through an external IdP — herold is not an IdP itself, only a relying party). On success herold sets a fresh session cookie and redirects back to tabard's URL.
4. With a valid session, `.well-known/jmap` returns the session descriptor.
5. Tabard checks the descriptor's `capabilities` and `accountCapabilities` against `../notes/server-contract.md`. Per resolved Q14/Q15, herold ships before tabard implements; capabilities are treated as available, not feature-detected with fallbacks. A missing capability is a deployment configuration error and surfaces in the About settings panel as such.
6. Tabard issues a single batched JMAP call for the initial view: mailboxes, identities, the inbox's first thread page, the inbox state strings, the user's filters list, the user's category configuration.
7. Tabard opens the EventSource push channel (also `credentials: 'include'`).
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

## Suite shape and cross-app handoff

Tabard is a suite (`../00-scope.md` § "Tabard is a suite"). All three apps — tabard-mail, tabard-calendar, tabard-contacts — are served from the same origin as herold, share the same session cookie, and use the same JMAP server. Cross-app navigation is plain `<a href>` links between same-origin URLs (resolved Q16):

- `/mail/...` — tabard-mail routes (default landing page).
- `/calendar/...` — tabard-calendar routes (when the app exists).
- `/contacts/...` — tabard-contacts routes (when the app exists).

There is no shared parent shell, no postMessage between iframes, no per-app sub-domain. An app linking to another opens it in the same tab; the destination app's bundle loads, picks up the existing JMAP session cookie, and hydrates from cache (each app maintains its own cache; no cross-app cache sharing in v1).

Until the second app exists, only `/mail/...` is in service. Routes for siblings 404 with a "this app isn't deployed yet" page.
