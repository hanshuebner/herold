/**
 * Hand-rolled hash-based router for the admin SPA.
 *
 * Mirrors the pattern from web/apps/suite/src/lib/router/router.svelte.ts.
 * URL shape: `https://herold.example.com/admin/#/dashboard`
 *
 * Match by inspecting `router.parts` (the path split on '/').
 * Views compare the first segment(s) against the view they implement.
 */

const DEFAULT_PATH = '/dashboard';

function parse(hash: string): string {
  const trimmed = hash.replace(/^#\/?/, '');
  return '/' + trimmed;
}

class Router {
  /** Current path; always starts with '/'. */
  current = $state(parse(window.location.hash));

  constructor() {
    window.addEventListener('hashchange', () => {
      this.current = parse(window.location.hash);
    });

    // Default route on first load.
    if (this.current === '/') {
      this.replace(DEFAULT_PATH);
    }
  }

  /** Path segments, e.g. /principals/abc-123 -> ['principals', 'abc-123']. */
  get parts(): readonly string[] {
    return this.current.split('/').filter(Boolean);
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
    this.current = path;
  }

  /** True when the current path starts with the given prefix segments. */
  matches(...prefix: string[]): boolean {
    const p = this.parts;
    if (prefix.length > p.length) return false;
    return prefix.every((seg, i) => p[i] === seg);
  }
}

export const router = new Router();
