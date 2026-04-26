/**
 * Hand-rolled hash-based router.
 *
 * Decision: hash routing (per docs/implementation/01-tech-stack.md). Route
 * tree is small; hash routing keeps the static-SPA hosting story simple
 * (herold serves index.html for `/` and that's all it needs to know about
 * the SPA's routes).
 *
 * URL shape: `https://tabard.example.com/#/mail/thread/abc-123`
 *
 * Match by inspecting `router.parts` (the path split on `/`). Views
 * compare the first segment(s) with the app they implement.
 */

const DEFAULT_PATH = '/mail';

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

  /** Path segments, e.g. /mail/thread/abc → ['mail', 'thread', 'abc']. */
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
