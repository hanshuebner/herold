/**
 * Breadcrumb ring buffer (REQ-CLOG-21, REQ-OPS-202).
 *
 * Captures three kinds of events:
 *   route  -- SPA route changes
 *   fetch  -- outbound HTTP requests (path only, no query string)
 *   console -- console.warn / console.error calls
 *
 * The buffer holds at most MAX_BREADCRUMBS entries; new entries evict oldest.
 * A snapshot is taken at capture time for kind=error events so subsequent
 * breadcrumbs do not retroactively appear in earlier errors.
 *
 * REQ-CLOG-10: url_path is URL.pathname only -- no query, no fragment.
 * Breadcrumbs carry only the documented field set; nothing else.
 */

import type { Breadcrumb } from './schema.js';

const MAX_BREADCRUMBS = 32;

const ring: Breadcrumb[] = [];
let head = 0; // next write position (wraps around)
let count = 0; // total entries ever (capped at MAX for snapshot)

function push(crumb: Breadcrumb): void {
  ring[head] = crumb;
  head = (head + 1) % MAX_BREADCRUMBS;
  if (count < MAX_BREADCRUMBS) count++;
}

/**
 * Returns an ordered snapshot of current breadcrumbs (oldest first).
 * The returned array is a fresh copy safe to attach to an event.
 */
export function snapshot(): Breadcrumb[] {
  const len = Math.min(count, MAX_BREADCRUMBS);
  if (len === 0) return [];
  const result: Breadcrumb[] = new Array(len);
  const start = count >= MAX_BREADCRUMBS ? head : 0;
  for (let i = 0; i < len; i++) {
    result[i] = ring[(start + i) % MAX_BREADCRUMBS]!;
  }
  return result;
}

/** Record a route change (called by router integration). */
export function recordRoute(route: string): void {
  push({ kind: 'route', ts: new Date().toISOString(), route });
}

/**
 * Record a fetch start or completion.
 * url_path is URL.pathname only -- caller must strip query and fragment.
 * REQ-CLOG-10, REQ-CLOG-21.
 */
export function recordFetch(
  method: string,
  url_path: string,
  status?: number,
): void {
  push({ kind: 'fetch', ts: new Date().toISOString(), method, url_path, status });
}

/** Record a console.warn or console.error call (REQ-CLOG-21). */
export function recordConsole(level: 'warn' | 'error', msg: string): void {
  push({ kind: 'console', ts: new Date().toISOString(), level, msg });
}

/** Exposed for testing only -- resets the ring buffer to empty. */
export function _resetForTest(): void {
  ring.length = 0;
  head = 0;
  count = 0;
}
