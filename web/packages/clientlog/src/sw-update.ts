/**
 * Service worker update detection helpers — REQ-PUSH-72 / REQ-MOB-75.
 *
 * The SW lifecycle is install -> WAITING -> activating.  The in-app prompt
 * fires while the user is still on the old version, while a new SW sits in
 * the WAITING state.  The user clicks Reload; the SPA posts SKIP_WAITING to
 * the waiting worker, the worker activates, and the controllerchange event
 * causes exactly one page reload.
 *
 * These helpers are shared between the suite and admin SPAs via
 * @herold/clientlog/sw-update so the update-prompt lifecycle is not
 * duplicated.
 */

/**
 * Returns true when a waiting service worker represents an update, not a
 * first install.  A first install has no existing controller, so `controller`
 * is null and we must not prompt.
 */
export function shouldShowBanner(
  waiting: ServiceWorker | null,
  controller: ServiceWorker | null,
): boolean {
  return waiting !== null && controller !== null;
}

/**
 * Attaches update-detection listeners to a service worker registration.
 *
 * Calls onShow() when a new version is ready (waiting) and the page is
 * controlled by an older version.  Two cases are covered:
 *
 *   1. The page was (re)loaded while a SW update was already waiting.
 *      reg.waiting is non-null at call time; controller is non-null too.
 *
 *   2. A new SW finishes installing while the page is open.
 *      reg.updatefound -> installing.statechange -> state === 'installed',
 *      and getController() is non-null (old SW still controls the page).
 *
 * No banner is shown on first install: getController() returns null in
 * that case, and both conditions guard on it.
 */
export function watchRegistration(
  reg: ServiceWorkerRegistration,
  getController: () => ServiceWorker | null,
  onShow: () => void,
  options?: { suppressInitialWaiting?: boolean },
): void {
  // Case 1: already waiting when the page loaded.
  //
  // Skipped for one load right after a user-accepted update reload
  // (suppressInitialWaiting). When the user clicks Reload, the SPA posts
  // SKIP_WAITING and reloads on controllerchange; on the reloaded page the
  // just-activated worker can still surface transiently as `reg.waiting`, which
  // would immediately re-announce the version the user just applied. Case 2
  // still fires for a genuinely new worker that installs after this load.
  if (!options?.suppressInitialWaiting && shouldShowBanner(reg.waiting, getController())) {
    onShow();
  }

  // Case 2: new SW installs while the page is open.
  //
  // When suppressInitialWaiting is true the page was reloaded immediately
  // after the user accepted an update.  The browser fires an update check for
  // sw.js as a side-effect of the register() call on the reloaded page.  That
  // check can race with the SW activation event: if the browser's internal
  // "installed SW" record has not yet been committed when the check runs, the
  // browser compares the fetched sw.js against the previous (now-superseded)
  // version and fires updatefound for the same bytes we just activated.
  //
  // We suppress the first updatefound in that case.  Any subsequent updatefound
  // (from startPeriodicUpdateCheck's hourly reg.update() calls or a navigation)
  // is a genuine new version and is announced normally.
  let skipNextUpdateFound = options?.suppressInitialWaiting === true;

  reg.addEventListener('updatefound', () => {
    if (skipNextUpdateFound) {
      skipNextUpdateFound = false;
      return;
    }
    const installing = reg.installing;
    if (!installing) return;
    installing.addEventListener('statechange', () => {
      if (installing.state === 'installed' && getController() !== null) {
        onShow();
      }
    });
  });
}

// Module-level reload guard.  Set to true before calling location.reload()
// so that a subsequent controllerchange event (which can fire more than once
// in some edge-cases) does not trigger a second reload.
let _reloading = false;

/**
 * Tells the waiting worker to skip waiting and arranges a single page reload
 * once the new worker has taken control.
 *
 * @param waiting - The waiting ServiceWorker, or null (no-op if null).
 * @param addControllerChangeListener - Abstraction over
 *   navigator.serviceWorker.addEventListener('controllerchange', ...) so the
 *   function can be unit-tested without a live navigator.
 * @param onReload - Called at most once when the new worker takes control.
 *   In production this is () => location.reload().
 */
export function activateWaiting(
  waiting: ServiceWorker | null,
  addControllerChangeListener: (cb: () => void) => void,
  onReload: () => void,
): void {
  if (!waiting) return;
  addControllerChangeListener(() => {
    if (!_reloading) {
      _reloading = true;
      onReload();
    }
  });
  waiting.postMessage({ type: 'SKIP_WAITING' });
}

/**
 * Starts a periodic service worker update check.
 *
 * The browser checks for an updated sw.js on navigation events and at most
 * once per 24 hours by its own heuristic.  A SPA with hash-based routing
 * (e.g. /#/mail) generates no real navigations after the initial page load,
 * so the browser may not detect a new SW for up to 24 hours.  Calling
 * reg.update() on an interval ensures the browser byte-compares the
 * registered sw.js against the server copy and fires updatefound within
 * intervalMs of a new deploy.  The watchRegistration() listener handles
 * the rest (banner, Reload).
 *
 * @returns A cleanup function that cancels the interval.
 */
export function startPeriodicUpdateCheck(
  reg: ServiceWorkerRegistration,
  intervalMs: number,
): () => void {
  const id = setInterval(() => {
    reg.update().catch(() => {
      // update() rejects when the network is unavailable or the server
      // returns a non-OK status.  This is expected in offline / degraded
      // conditions; the next tick fires another attempt.
    });
  }, intervalMs);
  return () => clearInterval(id);
}

export const _internals_forTest = {
  shouldShowBanner,
  watchRegistration,
  activateWaiting,
  startPeriodicUpdateCheck,
  get reloading(): boolean {
    return _reloading;
  },
  resetReloading(): void {
    _reloading = false;
  },
};
