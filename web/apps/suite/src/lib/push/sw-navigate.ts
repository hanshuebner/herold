/**
 * Helper for handling SW → SPA navigate messages.
 *
 * The service worker's `openApp()` falls back to
 * `client.postMessage({ type: 'navigate', path })` when
 * `clients.openWindow()` returns null (macOS focus-stealing prevention
 * blocks the new window).  This module processes those messages so the
 * already-open tab navigates to the correct thread.
 *
 * Paths sent by the SW for `clients.openWindow()` use the `/#/` prefix
 * (e.g. `/#/mail/thread/T1234`) so the browser hash-routes correctly when
 * a new tab is opened.  Strip the leading `/#` here so the hash router
 * receives a plain path starting with `/`.
 *
 * REQ-PUSH-70..73.
 */

/**
 * Strip the `/#` prefix that SW paths carry for openWindow hash routing.
 * Plain paths (no `/#`) are returned unchanged.
 */
export function normalizeSwPath(path: string): string {
  return path.startsWith('/#') ? path.slice(2) : path;
}

/**
 * Handle a message posted from the service worker to a window client.
 * Calls `navigate` when the message is a `{ type: 'navigate', path }` message;
 * silently ignores all other message shapes.
 *
 * @param data   The `MessageEvent.data` value from the SW message.
 * @param navigate  Called with the normalised router path when the message matches.
 */
export function handleSwNavigateMessage(
  data: unknown,
  navigate: (path: string) => void,
): void {
  if (!data || typeof data !== 'object') return;
  const msg = data as Record<string, unknown>;
  if (msg['type'] !== 'navigate') return;
  const path = typeof msg['path'] === 'string' ? msg['path'] : '';
  if (!path) return;
  navigate(normalizeSwPath(path) || '/');
}
