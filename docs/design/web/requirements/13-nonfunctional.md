# 13 — Non-functional requirements

Performance, security, reliability, accessibility, browser support.

## Performance

| ID | Requirement |
|----|-------------|
| REQ-PERF-01 | Initial Inbox load (first 50-thread page rendered) completes in < 2 s on a 50 Mbit connection from a warm cache; < 4 s from cold. |
| REQ-PERF-02 | Switching between views (inbox, label, search) feels instant: < 200 ms to first content, using the in-memory cache plus a background refresh. |
| REQ-PERF-03 | Optimistic actions paint the new UI state in < 100 ms (one frame plus render). See `11-optimistic-ui.md`. |
| REQ-PERF-04 | The client uses EventSource push (RFC 8620 §7) for live updates. It does NOT poll while a push channel is open. |
| REQ-PERF-05 | Thread-list scroll is virtualised. Scrolling through 10,000 threads in the same view does not grow the DOM beyond ~200 rows. |

## Security

| ID | Requirement |
|----|-------------|
| REQ-SEC-01 | The suite authenticates via an HTTP-only session cookie set by herold's login surface. The cookie is scoped to the suite origin (the same origin that serves both the static assets and the JMAP API). The suite does not store, read, or transmit any auth token in JS-accessible storage. See `../architecture/01-system-overview.md` § Bootstrap. |
| REQ-SEC-02 | Logout clears the cookie via herold's logout endpoint. The suite does not maintain its own auth state. The user-facing logout is a single click; no "remember me" toggle (the cookie's lifetime is herold's policy). |
| REQ-SEC-03 | All JMAP requests are HTTPS. The suite refuses to operate against an `http://` JMAP URL. |
| REQ-SEC-04 | HTML message bodies render inside a sandboxed iframe with `sandbox="allow-same-origin"` and no `allow-scripts`. See `../architecture/04-rendering.md`. |
| REQ-SEC-05 | External images in messages are NOT loaded by default. The user clicks "Load images" per message, with optional per-sender opt-in. |
| REQ-SEC-06 | No third-party analytics, tracking, or ad scripts ship with the suite. |
| REQ-SEC-07 | Content-Security-Policy is set such that the message iframe can only load images from the configured proxy origin (image proxying is the server's job; the suite requires a proxy to be configured — see `../notes/server-contract.md`). |

## Reliability and sync

| ID | Requirement |
|----|-------------|
| REQ-REL-01 | The client uses JMAP `state` strings and `Foo/changes` methods for incremental sync. Full re-fetch is a fallback on `cannotCalculateChanges`. |
| REQ-REL-02 | If the EventSource push channel disconnects, the client falls back to polling at a 30 s interval with a subtle "connecting…" indicator. The push channel is retried with exponential back-off in parallel. |
| REQ-REL-03 | Failed JMAP requests retry with exponential back-off, max 3 retries, max 30 s delay. After exhaustion the action surfaces as failed (`11-optimistic-ui.md` REQ-OPT-02). |
| REQ-REL-04 | The suite reconciles its in-memory cache to the server's `state` on every push event and after every batch reply, ensuring optimistic writes don't drift from server truth. |

## Accessibility

| ID | Requirement |
|----|-------------|
| REQ-A11Y-01 | All interactive elements are keyboard-navigable with a visible focus indicator. |
| REQ-A11Y-02 | Screen reader support: thread list = `role="listbox"`, thread row = `role="option"`, toolbars = `role="toolbar"`. ARIA-live for toast announcements. |
| REQ-A11Y-03 | Colour is not the sole differentiator for any status (unread, label colour, snooze indicator). Each conveys its meaning via shape, position, or text in addition to colour. |
| REQ-A11Y-04 | The shortcut help overlay (`?`) is screen-reader navigable. |

## Browser support

| Browser | Minimum version | Platforms |
|---------|-----------------|-----------|
| Chrome / Chromium | 120+ | Desktop (Windows, Linux, macOS), Android |
| Firefox | 120+ | Desktop |
| Safari | 17+ | macOS, iOS, iPadOS |
| Samsung Internet | 23+ | Android (best-effort; Chrome-based) |

Phone and tablet use the same browser engines as desktop (no native shells). Mobile-specific behaviour lives in `24-mobile-and-touch.md`.

| ID | Requirement |
|----|-------------|
| REQ-BR-01 | Below the supported versions the suite shows an "unsupported browser" page with an upgrade link. It does not attempt graceful degradation. |
| REQ-BR-02 | The suite does not ship JavaScript polyfills for features available in all supported versions. |
