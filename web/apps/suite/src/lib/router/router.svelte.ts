/**
 * Hand-rolled hash-based router.
 *
 * Decision: hash routing (per docs/implementation/01-tech-stack.md). Route
 * tree is small; hash routing keeps the static-SPA hosting story simple
 * (herold serves index.html for `/` and that's all it needs to know about
 * the SPA's routes).
 *
 * URL shape: `https://herold.example.com/#/mail/thread/abc-123`
 *
 * Match by inspecting `router.parts` (the path split on `/`). Views
 * compare the first segment(s) with the app they implement.
 */

const DEFAULT_PATH = '/mail';

function parse(hash: string): string {
  const trimmed = hash.replace(/^#\/?/, '');
  return '/' + trimmed;
}

/** True when `path` is a thread-reader route (`/mail/thread/<id>`). */
function isThreadPath(path: string): boolean {
  const segs = (path.split('?')[0] ?? path).split('/').filter(Boolean);
  return segs[0] === 'mail' && segs[1] === 'thread';
}

/**
 * Extract a query-parameter value from the hash fragment.
 * The hash may contain a `?key=value` portion after the path segments:
 * e.g. `#/mail?tab=promotions` → `parseHashParam('tab') === 'promotions'`.
 */
function parseHashParam(hash: string, key: string): string | null {
  const qIdx = hash.indexOf('?');
  if (qIdx < 0) return null;
  const qs = hash.slice(qIdx + 1);
  for (const pair of qs.split('&')) {
    const eq = pair.indexOf('=');
    if (eq < 0) continue;
    const k = decodeURIComponent(pair.slice(0, eq));
    if (k === key) return decodeURIComponent(pair.slice(eq + 1));
  }
  return null;
}

/**
 * Build a hash string that preserves current path segments but sets (or
 * removes) a single query parameter. Other params are preserved.
 */
function withHashParam(current: string, key: string, value: string | null): string {
  // Split path from query.
  const qIdx = current.indexOf('?');
  const path = qIdx >= 0 ? current.slice(0, qIdx) : current;
  const oldQs = qIdx >= 0 ? current.slice(qIdx + 1) : '';
  const params = new Map<string, string>();
  for (const pair of oldQs.split('&')) {
    if (!pair) continue;
    const eq = pair.indexOf('=');
    if (eq < 0) {
      params.set(decodeURIComponent(pair), '');
    } else {
      params.set(decodeURIComponent(pair.slice(0, eq)), decodeURIComponent(pair.slice(eq + 1)));
    }
  }
  if (value === null) {
    params.delete(key);
  } else {
    params.set(key, value);
  }
  const qs = Array.from(params.entries())
    .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`)
    .join('&');
  return qs ? `${path}?${qs}` : path;
}

class Router {
  /** Current path (including query string); always starts with '/'. */
  current = $state(parse(window.location.hash));

  /**
   * The most recent non-thread route visited in this tab, e.g.
   * `/mail`, `/mail/folder/sent`, `/mail/search?q=...`. `null` until the
   * first non-thread route is recorded (e.g. a session that opens straight
   * on a thread URL from a saved link or notification).
   *
   * `navigateBackFromThread()` (navigate-back.ts) pushes this path when
   * leaving a thread rather than calling `window.history.back()`, so the
   * thread's own history entry is never reused/consumed (re #294): the
   * thread stays reachable by a subsequent Back press instead of being
   * skipped over.
   */
  lastListPath: string | null = null;

  constructor() {
    window.addEventListener('hashchange', (event) => {
      // Read the hash from the event's own `newURL`, not a fresh
      // `window.location.hash` lookup. Two history transitions fired in
      // quick succession (e.g. an archive action's own push immediately
      // followed by the user's browser Back) each queue a genuine,
      // distinct hashchange event, but if this handler is slow to run for
      // the first one, `window.location.hash` has by then already moved
      // on to the second transition's target -- re-reading it collapses
      // both transitions into one, so `current` never actually passes
      // through the first transition's value. That skipped intermediate
      // state is exactly what MailView's auto-navigate-away effect (re
      // #29 / re #294) relies on seeing to reset its `confirmedFolderKey`
      // guard between thread visits. `newURL` is captured at dispatch
      // time and is immune to this race.
      this.setCurrent(parse(new URL(event.newURL).hash));
    });

    // Default route on first load.
    if (this.current === '/') {
      this.replace(DEFAULT_PATH);
    }
  }

  /**
   * Update `current`, recording the outgoing path as `lastListPath` when it
   * was a genuine route (not the pre-redirect empty-hash placeholder '/')
   * and not itself a thread route.
   */
  private setCurrent(next: string): void {
    if (this.current !== '/' && !isThreadPath(this.current)) {
      this.lastListPath = this.current;
    }
    this.current = next;
  }

  /** Path segments (query string stripped), e.g. /mail/thread/abc → ['mail', 'thread', 'abc']. */
  get parts(): readonly string[] {
    const path = this.current.split('?')[0] ?? this.current;
    return path.split('/').filter(Boolean);
  }

  /**
   * Read a query parameter from the current hash URL.
   * e.g. current = '/mail?tab=promotions' → getParam('tab') === 'promotions'.
   */
  getParam(key: string): string | null {
    return parseHashParam(this.current, key);
  }

  /**
   * Navigate to the current path with a query parameter set or removed.
   * Preserves all other params. Removes the param when value is null.
   */
  setParam(key: string, value: string | null): void {
    const next = withHashParam(this.current, key, value);
    this.navigate(next);
  }

  /** Push a new path; the browser back button returns the user. */
  navigate(path: string): void {
    if (!path.startsWith('/')) path = '/' + path;
    window.location.hash = '#' + path;
  }

  /** Replace the current path; back button does NOT return to this state. */
  replace(path: string): void {
    if (!path.startsWith('/')) path = '/' + path;
    history.replaceState(null, '', '#' + path);
    this.setCurrent(path);
  }

  /** True when the current path starts with the given prefix segments. */
  matches(...prefix: string[]): boolean {
    const p = this.parts;
    if (prefix.length > p.length) return false;
    return prefix.every((seg, i) => p[i] === seg);
  }
}

export const router = new Router();
