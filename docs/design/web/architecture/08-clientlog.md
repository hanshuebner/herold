# 08 — Client-log wrapper

The browser side of the client-log pipeline. Pairs with the server architecture in `../../server/architecture/10-client-log-pipeline.md` and the requirements in `../requirements/13-nonfunctional.md` (REQ-CLOG-*).

## Module layout

```
web/packages/clientlog/
  src/
    index.ts            // public API: install(), logFatal(), shutdown()
    queue.ts            // in-memory bounded queue + drop counter
    flush.ts            // batch + sendBeacon + keepalive fetch
    capture.ts          // window.onerror, unhandledrejection, console wrap
    breadcrumbs.ts      // route + fetch + console ring buffer (≤32)
    request_id.ts       // X-Request-Id minting + in-flight context
    livetail.ts         // observes session-descriptor field
    schema.ts           // event types; runtime validators for tests
    bootstrap.ts        // reads <meta name="herold-clientlog"> + session desc
    vitals.ts           // web-vitals adapter
    test/
      fakes.ts          // deterministic Date / crypto / sendBeacon / fetch
```

The package is consumed by both `web/apps/suite` and `web/apps/admin`. There is no SPA-specific wrapper code; differences (auth-vs-public path selection, Suite-vs-Admin `app` tag) are configuration passed to `install()`.

## Public API

```ts
type ClientlogConfig = {
  app: 'suite' | 'admin';
  buildSha: string;                                  // from <meta name="herold-build">
  endpoints: {
    authenticated: string;                           // e.g. '/api/v1/clientlog'
    anonymous:     string;                           // e.g. '/api/v1/clientlog/public'
  };
  isAuthenticated: () => boolean;                    // checked at flush time
  livetailUntil:   () => number | null;              // ms epoch; observed each tick
  telemetryEnabled: () => boolean;                   // observed each event
  bootstrap: BootstrapDescriptor;                    // see below
};

function install(cfg: ClientlogConfig): Clientlog;

interface Clientlog {
  logFatal(err: unknown, opts?: { synchronous?: boolean }): Promise<void>;
  shutdown(): void;                                  // drains queue, removes handlers
  // capture surfaces are installed automatically by install()
}
```

`install()` is called exactly once, very early in the SPA bootstrap — before the JMAP client is constructed, so a crash during JMAP setup is captured. The wrapper's own internal exceptions are caught and silently dropped (it must not be the source of the crash it is supposed to report).

## Bootstrap descriptor

Two sources, in order:

1. **`<meta name="herold-clientlog" content='{"...json...}'>`** — injected by `internal/webspa` at asset bundling, present from the first paint. Carries:
   ```
   { enabled, batch_max_events, batch_max_age_ms, queue_cap,
     telemetry_enabled_default }
   ```
   This is enough for pre-auth code paths.
2. **JMAP session descriptor** (`urn:netzhansa:params:jmap:clientlog`) — once authenticated, a richer descriptor overrides the meta values:
   ```
   { telemetry_enabled, livetail_until }
   ```

If the meta tag is absent (operator stripped it) or `enabled=false`, `install()` returns a no-op stub. The wrapper installs no handlers, captures nothing, and `logFatal()` resolves immediately. This is the kill switch for `clientlog.enabled=false` (REQ-OPS-219, REQ-CLOG-12).

## Capture surfaces

| Source | How | Always-on |
|---|---|---|
| Uncaught errors | `window.addEventListener('error', …)` | yes |
| Unhandled promise rejections | `window.addEventListener('unhandledrejection', …)` | yes |
| Framework error boundary | Svelte error boundary forwards to `logFatal()` | yes |
| `console.error`, `console.warn` | wrapped at `install()` time, original retained | yes |
| `console.info` / `log` / `debug` | wrapped, but emit only when livetail is active OR `telemetryEnabled()` is true | conditional |
| Web Vitals | `web-vitals` library callbacks | one report per metric per page load |
| Fetch breadcrumbs | global `fetch` wrapper installed by JMAP/admin client | yes |
| Route breadcrumbs | router emits a route-change hook | yes |

Wrapping `console.*` is done by replacing `globalThis.console` properties with proxies that call the original AND enqueue an event. The original is still the visible console output for the developer; the wrapper only adds a side channel.

## Queue, batch, flush

The queue is a bounded array. Defaults from REQ-CLOG-09:

- `queue_cap` = 200
- Drop policy when full: drop oldest, but `kind=error` events are retained in preference to `log` and `vital`. Implementation: queue is split into a small "errors" sub-queue (cap 50) and a "rest" sub-queue (cap 150). When dropping, the rest sub-queue evicts first.
- Drop counter is global; on next flush, a synthetic `{kind: 'log', level: 'warn', msg: 'clientlog: dropped N earlier events', client_ts: <now>}` event is prepended.

Flush triggers (REQ-CLOG-02):

- 20 events enqueued
- 5 s elapsed since the first event was enqueued
- `pagehide` event (use `sendBeacon` with a `Blob` — it is the only API that survives discard)
- `beforeunload` (rare; `pagehide` covers most paths)
- Live-tail mode: timer fires every 100 ms, plus per-event synchronous flush per REQ-CLOG-05

The flush function:

1. Captures the current queue snapshot, resets the live queue.
2. Builds the request body `{events: [...]}`.
3. Picks the endpoint (auth if `isAuthenticated()` returned true at module init, otherwise anonymous).
4. Sends:
   - Normal flush: `fetch(endpoint, { method: 'POST', body, keepalive: true, headers: ... })`. No `await` for fire-and-forget; errors logged to original console only.
   - Unload flush: `navigator.sendBeacon(endpoint, new Blob([body], {type: 'application/json'}))`. If sendBeacon returns false, body too large — the wrapper splits the batch in halves and retries up to 3 levels.
   - Synchronous flush (`logFatal({synchronous:true})` or live-tail per-event): single-event `fetch` with `keepalive: true`, awaited by the caller.
5. On `5xx` or network failure, re-enqueues up to a small limit (3 retries with 1 s, 5 s, 30 s backoff) for the auth endpoint; for the anonymous endpoint, drops on first failure (the server is silently dropping by policy anyway).

The wrapper uses the **session-cookie** auth path (REQ-SEC-01); no token plumbing in the request. The browser attaches the cookie on the same-origin POST.

## Pre-auth path

`install()` is called before the user has authenticated. The wrapper checks `isAuthenticated()` on every flush:

- If false: events are sent to the anonymous endpoint with the narrow schema (REQ-OPS-207). Breadcrumbs, `request_id`, `session_id` are stripped at flush time, not at capture time, so an event captured pre-auth and flushed post-auth gets the richer enrichment if it had it.
- If true: events go to the auth endpoint with the full schema.

The "is authenticated" predicate is supplied by the host SPA. For the Suite, it returns true once the JMAP session descriptor has been received successfully. For the Admin SPA, true once the admin login has completed. Before either, the wrapper assumes anonymous.

A common case: the user crashes the login page, gets redirected, and a fresh page load on the post-login route also crashes. The two events arrive on different endpoints (anonymous, then authenticated) but are joinable on `session_id` because the wrapper writes a `session_id` to `sessionStorage` on first install and reuses it.

## Schema construction

Built at flush time, not capture time, so enrichment that depends on late-bound state (route changed, request id resolved) reflects the latest values up to capture-instant. Each captured event stores the minimum needed to reconstruct (`client_ts`, `seq`, `page_id`, `kind`, `level`, `msg`, `stack?`, `breadcrumbs_snapshot?`).

`build_sha` is read once from `<meta name="herold-build">` at module init.

`session_id` is read from `sessionStorage`; if absent, generated via `crypto.randomUUID()` and written.

`page_id` is generated at module init via `crypto.randomUUID()` and never changes for the page load.

`seq` is a module-level counter starting at 0.

`route` is read from the router's current path at capture time.

`request_id` is read from the in-flight-request context (a small async-context shim built around `Zone.js`-style state machines, or simpler: the JMAP client sets a thread-local-ish variable for the duration of the fetch). Events captured outside any fetch carry no `request_id`.

`ua` is `navigator.userAgent`, capped at 256 chars.

## Breadcrumbs

Single ring buffer of 32 entries, three kinds:

```ts
type Breadcrumb =
  | { kind: 'route';    ts: string; route: string }
  | { kind: 'fetch';    ts: string; method: string; url_path: string; status?: number }
  | { kind: 'console';  ts: string; level: 'warn' | 'error'; msg: string };
```

`url_path` is `URL.pathname` only — no query string, no fragment. Strict (REQ-CLOG-10).

Snapshot of the breadcrumb buffer is attached only to `kind=error` events on the auth endpoint (the public endpoint never carries breadcrumbs). The snapshot is a copy taken at capture instant so subsequent breadcrumbs do not retroactively appear in an earlier error.

## X-Request-Id

A small primitive in `request_id.ts`:

```ts
export const RequestIdContext = {
  current(): string | undefined { … },
  run<T>(id: string, fn: () => Promise<T> | T): Promise<T> { … },
};
```

The JMAP client and admin REST client wrap each fetch in `RequestIdContext.run(uuidv7(), async () => fetch(…))`, attaching the id as `X-Request-Id` on the request. Any event captured during the fetch reads `RequestIdContext.current()` and stores it in the event.

`crypto.randomUUID()` produces UUID v4; v7 is generated inline (`Date.now()` + `crypto.getRandomValues`). Inlined to avoid a dependency.

## Live-tail observation

```ts
function startLivetailWatcher(cfg: ClientlogConfig) {
  setInterval(() => {
    const until = cfg.livetailUntil();
    if (until && until > Date.now()) {
      flushPolicy.set('aggressive');   // 100 ms timer + synchronous-per-event
    } else {
      flushPolicy.set('normal');
    }
  }, 1000);
}
```

`livetailUntil()` is supplied by the host. The Suite reads it from the JMAP session descriptor, refreshed on every JMAP response. The Admin SPA reads it from the admin session response. The 1 s observer poll is cheap; the JMAP push channel does not need to forward it because the next API response will carry the change anyway, with at most 1 s + JMAP-roundtrip latency.

## Web Vitals

`vitals.ts` calls into the `web-vitals` library:

```ts
import { onLCP, onINP, onCLS, onFCP, onTTFB } from 'web-vitals';
onLCP(metric => emit({kind: 'vital', vital: {name: 'LCP', value: metric.value, id: metric.id}, …}));
// likewise for the others
```

One report per metric per page load. `web-vitals` handles the timing of when each metric's final value is settled (e.g. LCP fires on the first interaction or page-hide).

The library is bundled inline; no CDN.

## Concurrency

Single-threaded JS event loop (REQ web concurrency). The queue is a plain array, mutated on the main thread. Flushes are kicked off via `setTimeout(0, …)` so they never block the capture call site. `sendBeacon` is synchronous from JS but the network work is queued by the browser; it returns immediately.

The wrapper does NOT spawn a Web Worker. The capture cost is dominated by `Error.stack` materialisation, which already runs on the main thread; pushing it to a worker would not help.

## Testing

`test/fakes.ts` exports:

- `installFakeClock()` — replaces `Date.now`, `performance.now`, `setTimeout`, `setInterval` with a deterministic harness; tests advance time explicitly.
- `installFakeFetch()` — replaces `globalThis.fetch` with an in-memory recorder; tests assert what was sent.
- `installFakeBeacon()` — replaces `navigator.sendBeacon`.
- `installFakeUuid()` — replaces `crypto.randomUUID` with a counter-based fake.

Every wrapper unit test installs all four fakes. No real timers, no real network. Flush is driven by the test calling `clock.advance(5_000)` or `instance.flushNow()`.

End-to-end tests (Playwright) drive the actual SPA against a herold dev server and assert that events appear in `GET /api/v1/admin/clientlog` after a forced error in the page.

## What this is NOT

- Not a general-purpose logger. The host SPA's regular `console.log` is unaffected when telemetry is off; the wrapper is purely a side channel.
- Not an OpenTelemetry browser SDK. We do not import `@opentelemetry/*` in the SPA. The server is the OTLP source.
- Not source-map aware. Stacks ship raw. The admin viewer symbolicates client-side on demand (REQ-OPS-212).
- Not user-visible. The wrapper has no UI surface; the only user-facing control is the `/settings` checkbox that flips `telemetry_enabled` (REQ-CLOG-06).
