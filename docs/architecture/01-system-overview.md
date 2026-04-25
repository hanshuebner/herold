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

1. Static assets load (HTML / JS / CSS / fonts).
2. Tabard reads token from `sessionStorage` (or `localStorage` if the user opted in).
3. Tabard issues `GET /.well-known/jmap` to fetch the session descriptor.
4. Tabard checks the descriptor's `capabilities` and `accountCapabilities` against `../notes/server-contract.md`. Missing capabilities feature-degrade the UI (see that file's "What happens when the contract is unmet" section).
5. Tabard issues a single batched JMAP call for the initial view: mailboxes, identities, the inbox's first thread page, the inbox state strings, the user's filters list (if `:sieve` is advertised).
6. Tabard opens the EventSource push channel.
7. The first paint shows the inbox.

## Concurrency

Single-threaded JS event loop. Long work (search snippet rendering, large HTML mail iframe load) yields cooperatively. No service worker (NG2). No worker thread in v1; revisit only if profiling shows main-thread stalls.

## State persistence

| What | Where | Lifetime |
|------|-------|----------|
| Auth token | `sessionStorage` (default) or `localStorage` (opt-in) | Tab close, or explicit logout |
| Sidebar collapsed/expanded | `localStorage` per account | Persistent |
| Pane split ratio | `localStorage` per account | Persistent |
| Recent search queries | `localStorage` per account | Bounded ring buffer (default 10) |
| Cache (mailboxes, threads, emails) | Memory only | Tab lifetime |
| Composed-but-unsaved compose state | `sessionStorage` | Tab close (drafts that auto-saved are server-side) |

Nothing else persists client-side. No IndexedDB, no service worker storage.
