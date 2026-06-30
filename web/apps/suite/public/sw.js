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

  // Resolve the navigation path SYNCHRONOUSLY before the first await.
  //
  // clients.openWindow() is gated on the transient user activation granted by
  // the notificationclick event.  On macOS (Chrome and Safari), that activation
  // can expire after the first microtask yield even when event.waitUntil() is
  // in use, so any intermediate async function boundary before the openWindow()
  // call can silently prevent the window from opening.
  //
  // The fix: determine the path here (synchronous), then pass it directly to
  // event.waitUntil(openApp(path)) so that clients.openWindow() executes as
  // the very first async step in the waitUntil promise chain.
  //
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
      // Accept/Decline is handled in-app (v1).
      return `/#/mail/thread/${encodeURIComponent(data.emailId ?? '')}`;
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
 * clients.openWindow() is called first.  On platforms where it succeeds (the
 * common case), the browser opens a new tab/window and brings it to the
 * foreground.  On macOS, some browser versions return null from openWindow()
 * when the browser is not the active application (macOS focus-stealing
 * prevention blocks the window from coming to the front, and the browser
 * silently declines to open it).  In that case we fall back to focusing the
 * nearest existing window client and posting a navigate message so the user
 * can find the message when they switch to the browser.
 *
 * IMPORTANT: this function must be called as the first async step within
 * event.waitUntil() — with no intermediate await before the call — so that
 * clients.openWindow() executes while Chrome's transient user activation is
 * still in effect (see the notificationclick handler comment above).
 */
async function openApp(path) {
  const win = await self.clients.openWindow(path);
  if (win !== null) return;

  // Fallback: focus an existing window and navigate it to the target path.
  const clients = await self.clients.matchAll({ type: 'window' });
  for (const client of clients) {
    if ('focus' in client) {
      await client.focus();
      client.postMessage({ type: 'navigate', path });
      return;
    }
  }
}

// ── Notification close ─────────────────────────────────────────────────────

self.addEventListener('notificationclose', () => {
  // Nothing to do — we do not track dismissals (REQ-PUSH-73: no remote telemetry).
});
