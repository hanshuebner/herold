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

## Client logging and error reporting

*(Added 2026-05-01: pairs with server-side REQ-OPS-200..220. Both the Suite and the Admin SPA forward runtime errors, console output, and Web Vitals to herold so they re-surface in the operator's slog/OTLP/ring-buffer pipeline. The wrapper is small, batched, and emits to herold only — there is no third-party SDK and no other origin.)*

### Wrapper module

| ID | Requirement |
|----|-------------|
| REQ-CLOG-01 | The SPA installs a single client-logging wrapper at module load: `window.onerror`, `window.onunhandledrejection`, the framework's error boundary, `console.error` and `console.warn` (always), and `console.info`/`console.log`/`console.debug` (only when REQ-CLOG-05 live-tail is active or the user has opted in to verbose telemetry per REQ-CLOG-06). The wrapper lives in a shared package (e.g. `web/packages/clientlog`) consumed by both Suite and Admin; there is no per-app reimplementation. |
| REQ-CLOG-02 | Events are batched in memory and flushed when any of: 20 events queued, 5 s since first queued event, the page becomes hidden (`pagehide`), or the page is unloading. The unload flush uses `navigator.sendBeacon` with a `Blob` of `application/json`; in-flight `fetch` calls during normal operation use `keepalive: true` so a navigation racing the request does not silently drop the batch. |
| REQ-CLOG-03 | Every event carries the fields specified by REQ-OPS-202 in full schema or REQ-OPS-207 in narrow schema. `client_ts` is `new Date().toISOString()` at capture time; `seq` is a monotonic counter per page load; `page_id` is generated once per page load; `session_id` is read from / written to `sessionStorage` so it spans page reloads in the same tab; `build_sha` is read from a `<meta name="herold-build">` tag injected by `internal/webspa` at asset bundling. |
| REQ-CLOG-04 | The wrapper exposes a `logFatal(err, {synchronous: true})` API for boot-path errors that must not be lost to a later batch. A synchronous emission performs an immediate `fetch` with `keepalive: true` and awaits the result; the caller chooses whether to display anything to the user before resolving. Routine logs and errors NEVER use synchronous mode. |
| REQ-CLOG-05 | The wrapper observes a `livetail_until` field on the JMAP session descriptor (refreshed on every JMAP response). While that timestamp is in the future, the wrapper switches to a 100 ms flush interval and emits each event synchronously per REQ-CLOG-04. Live-tail mode is operator-driven via REQ-ADM-232; the SPA is a passive observer. |
| REQ-CLOG-06 | A per-user opt-in flag (`telemetry_enabled`, default from server config per REQ-OPS-208) suppresses all `kind=log` and `kind=vital` events from the SPA. Errors (`kind=error`) are always sent regardless of the flag — a user cannot opt out of crash reporting. The flag is exposed in `/settings` as a single checkbox with one-line copy ("Send anonymous diagnostic logs to my mail-server operator"). |
| REQ-CLOG-07 | Pre-authentication code paths (login form, OIDC redirect, public unsubscribe view) emit only via the anonymous endpoint per REQ-OPS-200, and only with the narrow schema per REQ-OPS-207. The wrapper detects "no session yet" at module init by absence of the session cookie surface and routes accordingly until the first authenticated request succeeds. |
| REQ-CLOG-08 | Web Vitals (LCP, INP, CLS, TTFB, FCP) are captured via the standard `web-vitals` library or an inline equivalent (whichever bundles smaller) and emitted as `kind=vital` events. One report per metric per page load; no continuous streaming. |
| REQ-CLOG-09 | When the in-memory queue is at its cap (default 200 events) the wrapper drops oldest entries first (errors retained in preference to logs) and increments a drop counter. The next batch sent includes a synthetic `{kind: "log", level: "warn", msg: "clientlog: dropped N earlier events"}` entry so the gap is visible to the operator. |
| REQ-CLOG-10 | The wrapper enforces a payload allowlist client-side (defence-in-depth with REQ-OPS-215). It MUST NOT include: message bodies, attachment names, contact data, chat content, draft contents, search queries, mailbox names, URL query strings, request bodies, response bodies, header values, DOM snapshots, input field values. Breadcrumbs carry only the fields permitted by REQ-OPS-202 (`ts`, `kind`, `route?`, `status?`, `method?`, `url_path?`, `msg?`). |

### Correlation, ordering, breadcrumbs

| ID | Requirement |
|----|-------------|
| REQ-CLOG-20 | Every outbound JMAP / admin / chat / SMTP-API fetch carries an `X-Request-Id` header (UUID v7 generated client-side); the wrapper records the in-flight request id in a thread-local-like context so events emitted while the request is open carry that `request_id`. The server echoes the same id in `X-Request-Id` on the response and in any associated server-side log lines, enabling REQ-OPS-213 cross-source correlation. |
| REQ-CLOG-21 | Breadcrumbs capture: route changes (`{kind:"route", route, ts}`), fetch start/end (`{kind:"fetch", method, url_path, status?, ts}` — `url_path` is the path-only, query stripped), and explicit `console.warn`/`console.error` calls (`{kind:"console", level, msg, ts}`). The breadcrumb buffer is a ring of at most 32 entries; new entries evict oldest. |
| REQ-CLOG-22 | The wrapper makes no attempt to time-align client and server clocks. It always records browser wall-clock `client_ts`; the server is responsible for computing skew and reordering (REQ-OPS-203, REQ-OPS-210). |

### Bootstrap, kill switch, dev mode

| ID | Requirement |
|----|-------------|
| REQ-CLOG-12 | The wrapper reads a small bootstrap descriptor from a `<meta name="herold-clientlog">` tag (or the JMAP session descriptor for authenticated callers) carrying `{enabled, batch_max_events, batch_max_age_ms, queue_cap, telemetry_enabled}`. When `enabled=false`, the wrapper installs no handlers and emits nothing — operators with `clientlog.enabled=false` (REQ-OPS-219) get truly silent SPAs. |
| REQ-CLOG-13 | In Vite dev mode the wrapper additionally `console.log`s every captured event to the developer's browser console under a `[clientlog]` prefix, so a developer can see exactly what would be forwarded without needing the server side. The dev-mode echo is gated on `import.meta.env.DEV` and stripped from production bundles. |
| REQ-CLOG-14 | Tests of the wrapper run with deterministic fakes for `Date.now`, `crypto.randomUUID`, and `navigator.sendBeacon`/`fetch`. No background timers in unit tests; flush is driven explicitly by the test harness. |
