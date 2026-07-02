/**
 * Herold admin service worker — REQ-MOB-75 / REQ-PUSH-72.
 *
 * Build: __SW_BUILD__
 *
 * This SW exists solely to relay the update lifecycle so the admin SPA can
 * show the "A new version is available" prompt (REQ-MOB-75).  It does NOT
 * handle Web Push (the admin UI does not use push notifications), cache any
 * assets, or intercept network requests.
 */

'use strict';

// ── Install / activate ─────────────────────────────────────────────────────

// eslint-disable-next-line no-unused-vars
self.addEventListener('install', (_event) => {
  // Do not call skipWaiting() here.  Letting the SW enter the WAITING state
  // gives the SPA the chance to show the "A new version is available" prompt
  // while the user is still on the old version (REQ-PUSH-72 / REQ-MOB-75).
  // The SPA posts {type:'SKIP_WAITING'} when the user clicks Reload, which
  // triggers the message handler below.
});

self.addEventListener('activate', (event) => {
  // Claim all clients so the new SW takes control immediately after the
  // user has confirmed the reload.
  event.waitUntil(self.clients.claim());
});

// ── Message: SKIP_WAITING ──────────────────────────────────────────────────

// The SPA sends this after the user clicks Reload on the "new version
// available" banner.  Only then do we skip the waiting state and activate,
// triggering a controllerchange event on every controlled client.
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});
