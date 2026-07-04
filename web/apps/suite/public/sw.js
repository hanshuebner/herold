/**
 * Herold suite service worker — REQ-PUSH-70..73, REQ-MOB-74.
 *
 * Responsibilities:
 *   - Receive Web Push notifications and display them via the Notifications API.
 *   - Handle notificationclick: dispatch action buttons or open the app.
 *   - Handle notificationclose: log locally only; no remote telemetry (REQ-PUSH-73).
 *   - Relay SW update lifecycle so the in-app "A new version is available" prompt
 *     can be shown (REQ-MOB-75 / REQ-PUSH-72).
 *
 * This SW does NOT:
 *   - Cache anything — NG2 (no offline mode).
 *   - Intercept navigation or fetch requests.
 *   - Do background sync.
 *
 * The JMAP endpoint path (/jmap) is hard-coded here for action handlers
 * (Archive, Mark Read, etc.) because the SW cannot access the SPA's
 * module graph. The path matches the production deployment contract
 * (same-origin, /jmap as the JMAP API URL).
 */

'use strict';

// ── SW observability state ──────────────────────────────────────────────────
//
// swLog() fires-and-forgets a narrow event to /api/v1/clientlog/public so
// service-worker events appear in the server-side ring buffer.  Conforms to
// the narrow-event wire schema (REQ-OPS-207): no session_id, no breadcrumbs.
// Payload content is JSON-encoded and appended to the msg field (the narrow
// schema has no separate payload field, and the server rejects unknown fields
// with DisallowUnknownFields).
//
// _swPageId is a stable "page" identifier for the lifetime of this SW
// instance (reset on SW restart / update).  _swSeq is a monotonic counter.

const _swPageId = (() => {
  try { return crypto.randomUUID(); } catch { return 'sw-' + Date.now(); }
})();
let _swSeq = 0;

/**
 * Fire-and-forget log helper for the service worker.
 *
 * Encodes msg + payload into a narrow NarrowEvent and POSTs it to the
 * anonymous clientlog endpoint with keepalive:true so the request survives
 * the notificationclick handler teardown.  Never throws or rejects.
 *
 * Prefix SW events with "sw." (e.g. "sw.notificationclick") so they are
 * greppable in the admin clientlog ring buffer.
 */
function swLog(msg, payload) {
  let full = msg;
  if (payload) {
    try {
      full = msg + ' ' + JSON.stringify(payload);
    } catch { /* ignore serialisation errors */ }
  }
  if (full.length > 1024) full = full.slice(0, 1021) + '...';
  const event = {
    v: 1,
    kind: 'log',
    level: 'info',
    msg: full,
    client_ts: new Date().toISOString(),
    seq: _swSeq++,
    page_id: _swPageId,
    app: 'suite',
    build_sha: '',
    route: '/sw',
    ua: '',
  };
  try {
    fetch('/api/v1/clientlog/public', {
      method: 'POST',
      keepalive: true,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ events: [event] }),
    }).catch(() => { /* fire-and-forget: swallow network errors */ });
  } catch { /* never throw */ }
}

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
  // Claim all clients so the new SW serves push events immediately after
  // the user has confirmed the reload.
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

// ── Push event ─────────────────────────────────────────────────────────────

self.addEventListener('push', (event) => {
  if (!event.data) return;

  let payload;
  try {
    payload = event.data.json();
  } catch {
    // Non-JSON push: ignore.
    return;
  }

  swLog('sw.push', { kind: payload.kind, hasThreadId: payload.threadId !== undefined });

  const options = buildNotificationOptions(payload);
  if (!options) return;

  event.waitUntil(
    self.registration.showNotification(options.title, options),
  );
});

/**
 * Build the Notification options from a push payload.
 * Returns null if the payload kind is unrecognised.
 *
 * Payload shapes per REQ-PUSH-41..45.
 */
function buildNotificationOptions(payload) {
  const kind = payload.kind;

  switch (kind) {
    case 'mail': {
      const actions = [
        { action: 'archive', title: 'Archive' },
        { action: 'mark_read', title: 'Mark Read' },
        { action: 'reply', title: 'Reply' },
      ];
      return {
        title: payload.from ?? 'New message',
        body: payload.body ?? '',
        tag: payload.threadId ?? payload.emailId,
        badge: '/icons/badge-72.png',
        data: {
          kind: 'mail',
          threadId: payload.threadId,
          emailId: payload.emailId,
          accountId: payload.accountId,
          inboxMailboxId: payload.inboxMailboxId,
        },
        actions,
      };
    }

    case 'chat': {
      const actions = [
        { action: 'mark_read', title: 'Mark Read' },
        { action: 'reply', title: 'Reply' },
      ];
      return {
        title: payload.from ?? 'New message',
        body: payload.body ?? '',
        tag: payload.conversationId,
        data: {
          kind: 'chat',
          conversationId: payload.conversationId,
          messageId: payload.messageId,
        },
        actions,
      };
    }

    case 'calendar-invite': {
      const actions = [
        { action: 'accept', title: 'Accept' },
        { action: 'decline', title: 'Decline' },
      ];
      return {
        title: payload.from
          ? `${payload.from} invited you to ${payload.eventSummary ?? 'an event'}`
          : 'Calendar invitation',
        body: payload.body ?? '',
        tag: payload.eventUID ?? payload.emailId,
        data: {
          kind: 'calendar-invite',
          emailId: payload.emailId,
          eventUID: payload.eventUID,
        },
        actions,
      };
    }

    case 'call': {
      return {
        title: payload.from
          ? `Incoming video call from ${payload.from}`
          : 'Incoming video call',
        tag: `call-${payload.callId}`,
        requireInteraction: true,
        data: {
          kind: 'call',
          callId: payload.callId,
          conversationId: payload.conversationId,
        },
        actions: [
          { action: 'accept', title: 'Accept' },
          { action: 'decline', title: 'Decline' },
        ],
      };
    }

    case 'reaction': {
      return {
        title: payload.from
          ? `${payload.from} reacted with ${payload.emoji ?? ''}`
          : 'New reaction',
        body: payload.subject ? `"${payload.subject}"` : '',
        tag: payload.emailId ?? payload.messageId,
        data: {
          kind: 'reaction',
          emailId: payload.emailId,
          messageId: payload.messageId,
        },
        actions: [{ action: 'view', title: 'View' }],
      };
    }

    default:
      return null;
  }
}

// ── Notification click ─────────────────────────────────────────────────────

self.addEventListener('notificationclick', (event) => {
  event.notification.close();

  const data = event.notification.data ?? {};
  const action = event.action;

  swLog('sw.notificationclick', {
    action,
    kind: data.kind,
    hasThreadId: data.threadId !== undefined,
  });

  // Resolve the navigation path SYNCHRONOUSLY before the first await.
  // Pure JMAP background actions (archive, mark_read) do not open a window and
  // are dispatched through a separate waitUntil path that does not call openApp.
  const path = resolveNotificationPath(data, action);

  if (path !== null) {
    event.waitUntil(openApp(path));
  } else {
    event.waitUntil(handleBackgroundAction(data, action, event));
  }
});

/**
 * Return the app path to open for this notification click, or null when the
 * action is a background JMAP operation that does not open a window.
 *
 * This function is intentionally synchronous so the result can be passed
 * directly to event.waitUntil(openApp(path)) without an intermediate await.
 */
function resolveNotificationPath(data, action) {
  // Archive and mark_read are background JMAP calls specific to mail notifications.
  // Chat mark_read has no server-side SW handler; the app re-syncs on open,
  // so those clicks fall through and open the conversation.
  if (data.kind === 'mail' && (action === 'archive' || action === 'mark_read')) return null;

  const kind = data.kind;

  switch (kind) {
    case 'mail': {
      if (action === 'reply' || action === 'retry_archive' || action === 'retry_read') {
        return `/#/mail/compose?inReplyTo=${encodeURIComponent(data.emailId ?? '')}&quick=1`;
      }
      // Body click — open the thread.
      return data.threadId
        ? `/#/mail/thread/${encodeURIComponent(data.threadId)}`
        : '/#/mail';
    }
    case 'chat': {
      // Open the mail view with the openChat deep-link overlay so the
      // conversation appears without navigating away from whatever the user
      // was doing.  The full-screen /chat/* route is reachable from the rail.
      return data.conversationId
        ? `/#/mail?openChat=${encodeURIComponent(data.conversationId)}`
        : '/#/mail';
    }
    case 'calendar-invite': {
      // Accept/Decline is handled in-app (v1). Fall back to the inbox when the
      // email ID is unknown (the server cannot always link a calendar event back
      // to its invite email).
      return data.emailId
        ? `/#/mail/thread/${encodeURIComponent(data.emailId)}`
        : '/#/mail';
    }
    case 'call': {
      // SW cannot drive WebRTC (REQ-PUSH-67) — open the app for call signaling.
      return data.conversationId
        ? `/#/chat/${encodeURIComponent(data.conversationId)}`
        : '/';
    }
    default:
      return '/';
  }
}

/**
 * Handle a background JMAP action (archive or mark_read) that does not open
 * a window.  Called only when resolveNotificationPath() returns null.
 */
async function handleBackgroundAction(data, action, event) {
  if (data.kind !== 'mail') return;

  if (action === 'archive') {
    const ok = await jmapEmailSetArchive(data.emailId, data.inboxMailboxId);
    if (!ok) {
      // Re-show with failure suffix per REQ-PUSH-61.
      await self.registration.showNotification(
        event.notification.title + ' — failed to archive',
        {
          body: event.notification.body,
          tag: event.notification.tag,
          data: event.notification.data,
          actions: [{ action: 'retry_archive', title: 'Retry' }],
        },
      );
    }
    return;
  }

  if (action === 'mark_read') {
    const ok = await jmapEmailSetSeen(data.emailId);
    if (!ok) {
      await self.registration.showNotification(
        event.notification.title + ' — failed to mark read',
        {
          body: event.notification.body,
          tag: event.notification.tag,
          data: event.notification.data,
          actions: [{ action: 'retry_read', title: 'Retry' }],
        },
      );
    }
    return;
  }
}

// ── JMAP action helpers ─────────────────────────────────────────────────────

/**
 * Archive an email by removing the inbox from mailboxIds.
 * Uses a simple Email/set call with credentials: 'include' per REQ-PUSH-61.
 */
async function jmapEmailSetArchive(emailId, inboxMailboxId) {
  if (!emailId || !inboxMailboxId) return false;
  try {
    const res = await fetch('/jmap', {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
      },
      body: JSON.stringify({
        using: ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
        methodCalls: [
          [
            'Email/set',
            {
              update: {
                [emailId]: {
                  [`mailboxIds/${inboxMailboxId}`]: null,
                },
              },
            },
            'c0',
          ],
        ],
      }),
    });
    if (!res.ok) return false;
    const body = await res.json();
    const result = body.methodResponses?.[0]?.[1];
    return !result?.notUpdated?.[emailId];
  } catch {
    return false;
  }
}

/**
 * Mark an email as read by setting $seen: true.
 */
async function jmapEmailSetSeen(emailId) {
  if (!emailId) return false;
  try {
    const res = await fetch('/jmap', {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
      },
      body: JSON.stringify({
        using: ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
        methodCalls: [
          [
            'Email/set',
            {
              update: {
                [emailId]: { 'keywords/$seen': true },
              },
            },
            'c0',
          ],
        ],
      }),
    });
    if (!res.ok) return false;
    const body = await res.json();
    const result = body.methodResponses?.[0]?.[1];
    return !result?.notUpdated?.[emailId];
  } catch {
    return false;
  }
}

// ── App open helper ────────────────────────────────────────────────────────

/**
 * Open the suite at the given path.
 *
 * Uses the "Gmail pattern": focus an existing window client first and post a
 * navigate message so the already-open tab drives the route.  This sidesteps
 * clients.openWindow()'s two failure modes on macOS Chrome (issue #83):
 *
 *   1. Hash-fragment blindness: openWindow('/#/mail/thread/tX') opens or
 *      focuses a window but ignores the new hash when a tab is already open
 *      at the same origin, so no route change fires.
 *
 *   2. InvalidAccessError: openWindow throws when transient activation has
 *      expired (which can happen after any microtask yield on macOS).
 *
 * Focus-existing-first avoids both: the postMessage path reliably drives the
 * router in the already-open tab (App.svelte's SW message listener calls
 * handleSwNavigateMessage → router.navigate), and the openWindow branch is
 * only reached when no window exists at all.
 *
 * The openWindow call is wrapped in try/catch so an InvalidAccessError in
 * the no-existing-window case is swallowed rather than rejecting the
 * event.waitUntil chain.
 */
async function openApp(path) {
  const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  if (wins.length > 0) {
    swLog('sw.openApp.focus', { windowCount: wins.length, path });
    await wins[0].focus();
    wins[0].postMessage({ type: 'navigate', path });
    return;
  }

  // No existing window: open a new one.
  swLog('sw.openApp.openWindow', { path });
  try {
    await self.clients.openWindow(path);
    swLog('sw.openApp.openWindow.opened', { path });
  } catch (err) {
    swLog('sw.openApp.openWindow.threw', {
      name: (err && err.name) ? err.name : 'unknown',
      path,
    });
  }
}

// ── Notification close ─────────────────────────────────────────────────────

self.addEventListener('notificationclose', () => {
  // Nothing to do — we do not track dismissals (REQ-PUSH-73: no remote telemetry).
});
