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

self.addEventListener('install', (event) => {
  // Skip waiting so a new SW activates immediately for the push-only use case.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Claim all clients so the new SW serves push events immediately.
  event.waitUntil(self.clients.claim());
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

  event.waitUntil(handleNotificationClick(data, action, event));
});

async function handleNotificationClick(data, action, event) {
  const kind = data.kind;

  switch (kind) {
    case 'mail':
      await handleMailAction(data, action, event);
      break;
    case 'chat':
      await handleChatAction(data, action, event);
      break;
    case 'calendar-invite':
      // Open app at the thread/email; Accept/Decline is handled in-app (v1).
      await openApp(`/mail/thread/${encodeURIComponent(data.emailId ?? '')}`);
      break;
    case 'call':
      // Open app for call signaling — SW cannot drive WebRTC (REQ-PUSH-67).
      await openApp(
        data.conversationId
          ? `/chat/${encodeURIComponent(data.conversationId)}`
          : '/',
      );
      break;
    default:
      await openApp('/');
      break;
  }
}

async function handleMailAction(data, action, event) {
  if (action === 'archive') {
    const ok = await jmapEmailSetArchive(data.emailId, data.inboxMailboxId);
    if (!ok) {
      // Re-show with failure suffix per REQ-PUSH-61.
      await self.registration.showNotification(
        event.notification.title + ' — failed to archive',
        {
          ...event.notification,
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

  if (action === 'reply' || action === 'retry_archive' || action === 'retry_read') {
    await openApp(
      `/mail/compose?inReplyTo=${encodeURIComponent(data.emailId ?? '')}&quick=1`,
    );
    return;
  }

  // Body click — open the thread.
  if (data.threadId) {
    await openApp(`/mail/thread/${encodeURIComponent(data.threadId)}`);
  } else {
    await openApp('/mail');
  }
}

async function handleChatAction(data, action, event) {
  if (action === 'mark_read') {
    // Mark read is best-effort from the SW; the app can re-sync on open.
    // We open the app at the conversation to let the user see the update.
  }
  await openApp(
    data.conversationId
      ? `/chat/${encodeURIComponent(data.conversationId)}`
      : '/chat',
  );
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
 * Focus an existing suite tab at the given path, or open a new one.
 */
async function openApp(path) {
  const clients = await self.clients.matchAll({ type: 'window' });
  for (const client of clients) {
    if ('focus' in client) {
      await client.focus();
      client.postMessage({ type: 'navigate', path });
      return;
    }
  }
  // No open window — open a new one.
  await self.clients.openWindow(path);
}

// ── Notification close ─────────────────────────────────────────────────────

self.addEventListener('notificationclose', () => {
  // Nothing to do — we do not track dismissals (REQ-PUSH-73: no remote telemetry).
});
