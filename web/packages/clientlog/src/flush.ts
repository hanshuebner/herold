/**
 * Flush layer (REQ-CLOG-02, REQ-OPS-200, REQ-OPS-201, REQ-OPS-202, REQ-OPS-207).
 *
 * Responsible for:
 *   - Draining the queue and building the wire payload.
 *   - Selecting the endpoint (authenticated vs anonymous) based on
 *     isAuthenticated() at flush time.
 *   - Full schema (auth endpoint) vs narrow schema (anon endpoint).
 *   - fetch with keepalive: true for normal flushes.
 *   - navigator.sendBeacon with Blob for unload flushes; halve-and-retry
 *     up to 3 levels when sendBeacon returns false.
 *   - Synchronous (awaited) fetch for logFatal or livetail per-event.
 *   - Retry on 5xx / network failure for auth endpoint (3 retries,
 *     1 s / 5 s / 30 s backoff). Anon endpoint: drop on first failure.
 *   - Synthetic drop-counter warning event prepended to next batch.
 *
 * REQ-CLOG-03: session_id, page_id, build_sha, seq, route, ua attached here.
 * REQ-CLOG-07: pre-auth uses narrow schema; breadcrumbs/session_id stripped.
 * REQ-CLOG-10: no query strings, no bodies, no cookies in any field.
 */

import type { CapturedEvent, BatchBody, FullEvent, NarrowEvent, WireEvent } from './schema.js';
import type { Queue } from './queue.js';
import type { AppName } from './schema.js';
import type { Breadcrumb } from './schema.js';

export interface FlushContext {
  app: AppName;
  buildSha: string;
  pageId: string;
  sessionId: string;
  getRoute: () => string;
  getUa: () => string;
  isAuthenticated: () => boolean;
  endpoints: { authenticated: string; anonymous: string };
  queue: Queue;
  originalConsole: Pick<Console, 'error'>;
}

const AUTH_RETRY_DELAYS = [1000, 5000, 30000];
const BEACON_MAX_LEVELS = 3;

/** Build the wire payload for a single captured event in full schema. */
function toFullEvent(ev: CapturedEvent, ctx: FlushContext): FullEvent {
  const base: FullEvent = {
    v: 1,
    kind: ev.kind,
    level: ev.level,
    msg: ev.msg,
    client_ts: ev.client_ts,
    seq: ev.seq,
    page_id: ctx.pageId,
    session_id: ctx.sessionId,
    app: ctx.app,
    build_sha: ctx.buildSha,
    route: ctx.getRoute(),
    ua: ctx.getUa().slice(0, 256),
  };
  if (ev.stack !== undefined) base.stack = ev.stack;
  if (ev.request_id !== undefined) base.request_id = ev.request_id;
  if (ev.vital !== undefined) base.vital = ev.vital;
  if (ev.synchronous) base.synchronous = true;
  // Breadcrumbs only on kind=error on auth endpoint (REQ-OPS-202).
  if (ev.kind === 'error' && ev.breadcrumbs_snapshot !== undefined) {
    base.breadcrumbs = ev.breadcrumbs_snapshot;
  }
  return base;
}

/** Build the wire payload for a single captured event in narrow schema. */
function toNarrowEvent(ev: CapturedEvent, ctx: FlushContext): NarrowEvent {
  const base: NarrowEvent = {
    v: 1,
    kind: ev.kind,
    level: ev.level,
    msg: ev.msg,
    client_ts: ev.client_ts,
    seq: ev.seq,
    page_id: ctx.pageId,
    app: ctx.app,
    build_sha: ctx.buildSha,
    route: ctx.getRoute(),
    ua: ctx.getUa().slice(0, 256),
  };
  if (ev.stack !== undefined) base.stack = ev.stack;
  return base;
}

function buildBody(events: WireEvent[]): string {
  const body: BatchBody = { events };
  return JSON.stringify(body);
}

/**
 * Attempt a sendBeacon flush. If the browser returns false (payload too
 * large), split in half and retry recursively up to BEACON_MAX_LEVELS.
 * REQ-CLOG-02.
 */
function beaconFlush(
  endpoint: string,
  events: WireEvent[],
  level: number,
  sendBeacon: (url: string, data: Blob) => boolean,
): void {
  if (events.length === 0) return;
  const body = buildBody(events);
  const blob = new Blob([body], { type: 'application/json' });
  const ok = sendBeacon(endpoint, blob);
  if (!ok && level < BEACON_MAX_LEVELS && events.length > 1) {
    const mid = Math.ceil(events.length / 2);
    beaconFlush(endpoint, events.slice(0, mid), level + 1, sendBeacon);
    beaconFlush(endpoint, events.slice(mid), level + 1, sendBeacon);
  }
}

/**
 * Normal flush via fetch with keepalive. Fire-and-forget for non-sync
 * paths. Retries on 5xx/network failure for auth endpoint only.
 */
async function fetchFlush(
  endpoint: string,
  body: string,
  isAuth: boolean,
  originalConsole: Pick<Console, 'error'>,
  fetchFn: typeof globalThis.fetch,
  setTimeoutFn: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>,
  attempt: number,
): Promise<void> {
  try {
    const res = await fetchFn(endpoint, {
      method: 'POST',
      body,
      keepalive: true,
      headers: { 'Content-Type': 'application/json' },
    });
    if (res.status >= 500 && isAuth && attempt < AUTH_RETRY_DELAYS.length) {
      const delay = AUTH_RETRY_DELAYS[attempt] ?? 30000;
      setTimeoutFn(
        () =>
          void fetchFlush(
            endpoint,
            body,
            isAuth,
            originalConsole,
            fetchFn,
            setTimeoutFn,
            attempt + 1,
          ),
        delay,
      );
    }
  } catch {
    if (isAuth && attempt < AUTH_RETRY_DELAYS.length) {
      const delay = AUTH_RETRY_DELAYS[attempt] ?? 30000;
      setTimeoutFn(
        () =>
          void fetchFlush(
            endpoint,
            body,
            isAuth,
            originalConsole,
            fetchFn,
            setTimeoutFn,
            attempt + 1,
          ),
        delay,
      );
    }
    // anon endpoint: drop on first failure (REQ architecture §flush)
  }
}

export interface FlusherOptions {
  ctx: FlushContext;
  fetchFn: typeof globalThis.fetch;
  sendBeaconFn: (url: string, data: Blob) => boolean;
  setTimeoutFn: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>;
}

export interface Flusher {
  /** Fire-and-forget batch flush. */
  flush(): void;
  /** Awaitable flush used by logFatal and livetail per-event. */
  flushSync(events?: CapturedEvent[]): Promise<void>;
  /** Unload flush via sendBeacon. */
  flushBeacon(): void;
}

export function createFlusher(opts: FlusherOptions): Flusher {
  const { ctx, fetchFn, sendBeaconFn, setTimeoutFn } = opts;

  // Pending drop counter that was accumulated before the last drain.
  // Prepended as a synthetic warning on the next flush.
  let pendingDropWarning = 0;

  function collectEvents(): { events: WireEvent[]; endpoint: string; isAuth: boolean } {
    const pending = ctx.queue.drain();
    const drops = ctx.queue.dropCount();

    // At drain time dropCount() has been reset. Capture the accumulated
    // drops from before this drain (tracked in pendingDropWarning).
    const totalDrops = pendingDropWarning;
    pendingDropWarning = drops; // save for next flush

    const auth = ctx.isAuthenticated();
    const endpoint = auth ? ctx.endpoints.authenticated : ctx.endpoints.anonymous;

    const wireEvents: WireEvent[] = [];

    // Prepend synthetic drop warning if there was one.
    if (totalDrops > 0) {
      const dropWarning: CapturedEvent = {
        kind: 'log',
        level: 'warn',
        msg: `clientlog: dropped ${totalDrops} earlier events`,
        client_ts: new Date().toISOString(),
        seq: -1, // sentinel; the server can handle out-of-order seq
      };
      wireEvents.push(
        auth ? toFullEvent(dropWarning, ctx) : toNarrowEvent(dropWarning, ctx),
      );
    }

    for (const ev of pending) {
      wireEvents.push(auth ? toFullEvent(ev, ctx) : toNarrowEvent(ev, ctx));
    }

    return { events: wireEvents, endpoint, isAuth: auth };
  }

  function flush(): void {
    const { events, endpoint, isAuth } = collectEvents();
    if (events.length === 0) return;
    const body = buildBody(events);
    void fetchFlush(endpoint, body, isAuth, ctx.originalConsole, fetchFn, setTimeoutFn, 0);
  }

  async function flushSync(overrideEvents?: CapturedEvent[]): Promise<void> {
    const auth = ctx.isAuthenticated();
    const endpoint = auth ? ctx.endpoints.authenticated : ctx.endpoints.anonymous;
    let wireEvents: WireEvent[];

    if (overrideEvents !== undefined) {
      wireEvents = overrideEvents.map((ev) =>
        auth ? toFullEvent(ev, ctx) : toNarrowEvent(ev, ctx),
      );
    } else {
      const collected = collectEvents();
      wireEvents = collected.events;
      // endpoint/auth already determined above
    }

    if (wireEvents.length === 0) return;
    const body = buildBody(wireEvents);
    try {
      await fetchFn(endpoint, {
        method: 'POST',
        body,
        keepalive: true,
        headers: { 'Content-Type': 'application/json' },
      });
    } catch {
      // logFatal errors must not throw back to caller
    }
  }

  function flushBeacon(): void {
    const { events, endpoint } = collectEvents();
    if (events.length === 0) return;
    beaconFlush(endpoint, events, 0, sendBeaconFn);
  }

  return { flush, flushSync, flushBeacon };
}

/**
 * Helper exported for breadcrumb fetch recording: extracts the pathname
 * from a URL string, stripping query and fragment. REQ-CLOG-10.
 */
export function urlPathOnly(urlOrString: string | URL): string {
  try {
    const u = typeof urlOrString === 'string' ? new URL(urlOrString, globalThis.location?.href ?? 'http://localhost') : urlOrString;
    return u.pathname;
  } catch {
    // Not a valid URL; return the raw string stripped of query parts
    const s = String(urlOrString);
    const q = s.indexOf('?');
    const h = s.indexOf('#');
    const end = [q, h].filter((x) => x !== -1);
    return end.length > 0 ? s.slice(0, Math.min(...end)) : s;
  }
}

// Re-export types for convenience
export type { Breadcrumb };
