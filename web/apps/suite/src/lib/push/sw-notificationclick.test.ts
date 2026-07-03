/**
 * Unit tests for the service worker notificationclick path resolver and
 * the openApp open-window/fallback function.
 *
 * sw.js lives in public/ and is not a module, so we cannot import it
 * directly.  We evaluate its source in a controlled context with the SW
 * global mocked to silence event registrations, then extract and call the
 * pure resolveNotificationPath function, which has no dependency on SW APIs,
 * and the async openApp function, which uses self.clients.
 *
 * REQ-PUSH-70..73, REQ-MOB-74.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it, vi } from 'vitest';

// Resolve the path to sw.js relative to this test file.
// test: src/lib/push/  →  ../../../public/sw.js
const __dir = dirname(fileURLToPath(import.meta.url));
const swSource = readFileSync(resolve(__dir, '../../../public/sw.js'), 'utf-8');

// ── resolveNotificationPath + buildNotificationOptions fixture ───────────────
//
// Both functions make no SW API calls, so a minimal self mock is sufficient.

// Minimal SW global mock.  Neither resolveNotificationPath nor
// buildNotificationOptions makes SW API calls, but the module-level
// self.addEventListener() calls at the top of sw.js need something callable so
// evaluation does not throw.
const selfMock = {
  addEventListener: () => {},
  clients: {},
  registration: { scope: 'http://localhost/' },
  skipWaiting: () => {},
};

type NotificationOptions = {
  title: string;
  body: string;
  tag: string | undefined;
  badge?: string;
  data: Record<string, unknown>;
  actions?: { action: string; title: string }[];
};

// Evaluate sw.js and return its internal functions under test.
// eslint-disable-next-line no-new-func
const factory = new Function(
  'self',
  `${swSource}\nreturn { resolveNotificationPath, buildNotificationOptions };`,
);
const { resolveNotificationPath, buildNotificationOptions } = factory(selfMock) as {
  resolveNotificationPath: (
    data: Record<string, unknown>,
    action: string,
  ) => string | null;
  buildNotificationOptions: (
    payload: Record<string, unknown>,
  ) => NotificationOptions | null;
};

// ── openApp fixture ──────────────────────────────────────────────────────────
//
// openApp calls self.clients.openWindow and self.clients.matchAll, so each
// test creates a fresh SW evaluation with the specific client mock it needs.

type ClientMock = {
  focus: () => Promise<void>;
  postMessage: (msg: unknown) => void;
};

function makeOpenApp(clientsMock: {
  openWindow: (path: string) => Promise<ClientMock | null>;
  matchAll?: () => Promise<ClientMock[]>;
}): (path: string) => Promise<void> {
  const mock = {
    addEventListener: () => {},
    clients: {
      matchAll: async (): Promise<ClientMock[]> => [],
      ...clientsMock,
    },
    registration: { scope: 'http://localhost/' },
    skipWaiting: () => {},
  };
  // eslint-disable-next-line no-new-func
  const f = new Function('self', `${swSource}\nreturn { openApp };`);
  return (f(mock) as { openApp: (path: string) => Promise<void> }).openApp;
}

// ── Background JMAP actions (must return null — no window to open) ──────────

describe('resolveNotificationPath — background actions', () => {
  it('returns null for archive action on mail', () => {
    expect(
      resolveNotificationPath(
        { kind: 'mail', emailId: 'e1', inboxMailboxId: 'm1' },
        'archive',
      ),
    ).toBeNull();
  });

  it('returns null for mark_read action on mail', () => {
    expect(
      resolveNotificationPath({ kind: 'mail', emailId: 'e1' }, 'mark_read'),
    ).toBeNull();
  });

  it('returns null only for mail archive, not for other kinds', () => {
    // archive is a JMAP-mail-only background action; other kinds fall through
    expect(resolveNotificationPath({ kind: 'mail' }, 'archive')).toBeNull();
    // chat has no server-side archive — its click opens the conversation
    expect(resolveNotificationPath({ kind: 'chat', conversationId: 'c1' }, 'archive')).toBe(
      '/#/mail?openChat=c1',
    );
  });
});

// ── Mail body click ──────────────────────────────────────────────────────────

describe('resolveNotificationPath — mail body click', () => {
  it('returns /#/mail/thread/<threadId> when threadId is present', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', threadId: 'T1234', emailId: 'e1' },
      '',
    );
    expect(path).toBe('/#/mail/thread/T1234');
  });

  it('encodes special characters in threadId', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', threadId: 'T abc/+=' },
      '',
    );
    expect(path).toBe('/#/mail/thread/T%20abc%2F%2B%3D');
  });

  it('falls back to /#/mail when threadId is absent', () => {
    expect(resolveNotificationPath({ kind: 'mail', emailId: 'e1' }, '')).toBe(
      '/#/mail',
    );
  });
});

// ── Mail action buttons that open the compose window ────────────────────────

describe('resolveNotificationPath — mail open-window actions', () => {
  it('returns compose path for reply action', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e42' },
      'reply',
    );
    expect(path).toBe('/#/mail/compose?inReplyTo=e42&quick=1');
  });

  it('returns compose path for retry_archive action', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e42' },
      'retry_archive',
    );
    expect(path).toBe('/#/mail/compose?inReplyTo=e42&quick=1');
  });

  it('returns compose path for retry_read action', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e42' },
      'retry_read',
    );
    expect(path).toBe('/#/mail/compose?inReplyTo=e42&quick=1');
  });

  it('encodes emailId in compose path', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e a+b' },
      'reply',
    );
    expect(path).toContain('inReplyTo=e%20a%2Bb');
  });

  it('uses empty string for missing emailId in compose path', () => {
    const path = resolveNotificationPath({ kind: 'mail' }, 'reply');
    expect(path).toBe('/#/mail/compose?inReplyTo=&quick=1');
  });
});

// ── Chat ─────────────────────────────────────────────────────────────────────

describe('resolveNotificationPath — chat', () => {
  it('returns openChat deep-link when conversationId is present', () => {
    const path = resolveNotificationPath(
      { kind: 'chat', conversationId: 'conv-1' },
      '',
    );
    expect(path).toBe('/#/mail?openChat=conv-1');
  });

  it('encodes special characters in conversationId', () => {
    const path = resolveNotificationPath(
      { kind: 'chat', conversationId: 'conv a+b' },
      '',
    );
    expect(path).toBe('/#/mail?openChat=conv%20a%2Bb');
  });

  it('falls back to /#/mail when conversationId is absent', () => {
    expect(resolveNotificationPath({ kind: 'chat' }, '')).toBe('/#/mail');
  });

  it('returns openChat deep-link for mark_read action on chat', () => {
    // mark_read on chat is not a background action — chat has no JMAP mark_read
    // handler, so the click opens the conversation overlay.
    const path = resolveNotificationPath(
      { kind: 'chat', conversationId: 'conv-1' },
      'mark_read',
    );
    expect(path).toBe('/#/mail?openChat=conv-1');
  });
});

// ── Calendar invite ──────────────────────────────────────────────────────────

describe('resolveNotificationPath — calendar-invite', () => {
  it('returns /#/mail thread path for emailId', () => {
    const path = resolveNotificationPath(
      { kind: 'calendar-invite', emailId: 'e99' },
      '',
    );
    expect(path).toBe('/#/mail/thread/e99');
  });

  it('falls back to /#/mail when emailId is absent', () => {
    // When the server cannot link a calendar event to an invite email, emailId
    // is absent.  The route should fall back to the inbox rather than generating
    // an invalid /#/mail/thread/ path with an empty ID.
    const path = resolveNotificationPath({ kind: 'calendar-invite' }, '');
    expect(path).toBe('/#/mail');
  });

  it('returns /#/mail thread path for accept action (handled in-app)', () => {
    const path = resolveNotificationPath(
      { kind: 'calendar-invite', emailId: 'e99' },
      'accept',
    );
    expect(path).toBe('/#/mail/thread/e99');
  });
});

// ── Call ─────────────────────────────────────────────────────────────────────

describe('resolveNotificationPath — call', () => {
  it('returns /#/chat/<conversationId> when conversationId is present', () => {
    const path = resolveNotificationPath(
      { kind: 'call', conversationId: 'room-7' },
      '',
    );
    expect(path).toBe('/#/chat/room-7');
  });

  it('falls back to / when conversationId is absent', () => {
    expect(resolveNotificationPath({ kind: 'call' }, '')).toBe('/');
  });
});

// ── Unknown / reaction ───────────────────────────────────────────────────────

describe('resolveNotificationPath — unknown kind', () => {
  it('returns / for unrecognised kind', () => {
    expect(resolveNotificationPath({ kind: 'reaction' }, '')).toBe('/');
  });

  it('returns / when data is empty', () => {
    expect(resolveNotificationPath({}, '')).toBe('/');
  });

  it('returns / for archive action on unknown kind (not a mail background action)', () => {
    // The background-action gate is mail-specific; unknown kind falls to default.
    expect(resolveNotificationPath({ kind: 'unknown' }, 'archive')).toBe('/');
  });
});

// ── buildNotificationOptions ─────────────────────────────────────────────────
//
// These tests verify that buildNotificationOptions correctly processes the
// server's push payload format (after the server-side fix that adds kind,
// threadId, emailId, inboxMailboxId, body fields).  They also document the
// contract between the server's BuildPayload output and the SW's notification
// rendering.

describe('buildNotificationOptions — email (server payload format)', () => {
  // A payload shaped like the server's emailPayload struct.
  // threadId uses the JMAP wire form "t<n>" (lowercase "t") matching
  // renderThreadID() in internal/protojmap/mail/thread/methods.go.
  const emailPayload = {
    '@type': 'StateChange',
    changed: { a1: { Email: '100' } },
    kind: 'mail',
    type: 'email',
    from: 'Bob Smith <bob@example.test>',
    body: 'Re: Project discussion',
    subject: 'Re: Project discussion',
    mailbox: 'INBOX',
    emailId: '42',
    msgid: '42',
    threadId: 't123',
    inboxMailboxId: '5',
  };

  it('returns non-null options for kind=mail payload', () => {
    expect(buildNotificationOptions(emailPayload)).not.toBeNull();
  });

  it('uses from as the notification title', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.title).toBe('Bob Smith <bob@example.test>');
  });

  it('uses body as the notification body', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.body).toBe('Re: Project discussion');
  });

  it('stores kind=mail in notification data', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.data['kind']).toBe('mail');
  });

  it('stores threadId from payload into notification data', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.data['threadId']).toBe('t123');
  });

  it('stores emailId from payload into notification data', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.data['emailId']).toBe('42');
  });

  it('stores inboxMailboxId from payload into notification data', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.data['inboxMailboxId']).toBe('5');
  });

  it('sets tag to threadId when threadId is present', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    expect(opts.tag).toBe('t123');
  });

  it('sets tag to emailId when threadId is absent', () => {
    const p = { ...emailPayload, threadId: undefined };
    const opts = buildNotificationOptions(p)!;
    expect(opts.tag).toBe('42');
  });

  it('data.threadId flows through resolveNotificationPath to thread route', () => {
    const opts = buildNotificationOptions(emailPayload)!;
    const path = resolveNotificationPath(
      opts.data as Record<string, unknown>,
      '',
    );
    expect(path).toBe('/#/mail/thread/t123');
  });

  it('falls back to /#/mail route when threadId absent in notification data', () => {
    const p = { ...emailPayload, threadId: undefined };
    const opts = buildNotificationOptions(p)!;
    const path = resolveNotificationPath(
      opts.data as Record<string, unknown>,
      '',
    );
    expect(path).toBe('/#/mail');
  });

  it('returns null for payload without kind (old server format, type=email only)', () => {
    const oldPayload = {
      '@type': 'StateChange',
      type: 'email',
      from: 'Bob',
      subject: 'Hello',
      msgid: '42',
    };
    expect(buildNotificationOptions(oldPayload)).toBeNull();
  });

  it('returns null for unrecognised kind', () => {
    expect(buildNotificationOptions({ kind: 'unknown' })).toBeNull();
  });

  it('returns null for empty payload', () => {
    expect(buildNotificationOptions({})).toBeNull();
  });
});

describe('buildNotificationOptions — chat (server payload format)', () => {
  const chatPayload = {
    '@type': 'StateChange',
    changed: { a1: { Message: '200' } },
    kind: 'chat',
    type: 'chat',
    from: 'Carol',
    body: 'Hello team!',
    conversationId: 'C99',
    text: 'Hello team!',
  };

  it('returns non-null options for kind=chat payload', () => {
    expect(buildNotificationOptions(chatPayload)).not.toBeNull();
  });

  it('stores conversationId in notification data', () => {
    const opts = buildNotificationOptions(chatPayload)!;
    expect(opts.data['conversationId']).toBe('C99');
  });

  it('uses body as the notification body', () => {
    const opts = buildNotificationOptions(chatPayload)!;
    expect(opts.body).toBe('Hello team!');
  });

  it('data.conversationId flows to chat deep-link route', () => {
    const opts = buildNotificationOptions(chatPayload)!;
    const path = resolveNotificationPath(
      opts.data as Record<string, unknown>,
      '',
    );
    expect(path).toBe('/#/mail?openChat=C99');
  });
});

describe('buildNotificationOptions — calendar-invite (server payload format)', () => {
  const calPayload = {
    '@type': 'StateChange',
    changed: { a1: { CalendarEvent: '300' } },
    kind: 'calendar-invite',
    type: 'calendar',
    from: 'Dana',
    eventSummary: 'Team standup',
    body: 'Team standup',
    eventUID: 'uid-abc',
    uid: 'uid-abc',
  };

  it('returns non-null options for kind=calendar-invite payload', () => {
    expect(buildNotificationOptions(calPayload)).not.toBeNull();
  });

  it('uses eventSummary in title when from is present', () => {
    const opts = buildNotificationOptions(calPayload)!;
    expect(opts.title).toContain('Team standup');
  });

  it('sets tag to eventUID', () => {
    const opts = buildNotificationOptions(calPayload)!;
    expect(opts.tag).toBe('uid-abc');
  });

  it('routes to /#/mail when emailId is absent in notification data', () => {
    const opts = buildNotificationOptions(calPayload)!;
    const path = resolveNotificationPath(
      opts.data as Record<string, unknown>,
      '',
    );
    // emailId is not sent by the server for calendar events; sw.js should
    // fall back to the inbox rather than generating an empty-ID thread route.
    expect(path).toBe('/#/mail');
  });
});

// ── openApp ──────────────────────────────────────────────────────────────────
//
// openApp is the definitive test the maintainer asked for (issue #83 comment
// 2026-06-29): verify that clients.openWindow() is called first and that the
// focus+navigate fallback fires only when openWindow() returns null (macOS
// focus-stealing prevention).

describe('openApp — openWindow succeeds', () => {
  it('calls clients.openWindow with the given path', async () => {
    const mockWindow = { url: 'http://localhost/', postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue(mockWindow);
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(openWindow).toHaveBeenCalledWith('/#/mail/thread/T1');
  });

  it('does not call matchAll when openWindow returns a window', async () => {
    const mockWindow = { url: 'http://localhost/' };
    const openWindow = vi.fn().mockResolvedValue(mockWindow);
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(matchAll).not.toHaveBeenCalled();
  });

  it('does not call focus or postMessage when openWindow returns a window', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(mockClient.focus).not.toHaveBeenCalled();
    expect(mockClient.postMessage).not.toHaveBeenCalled();
  });
});

describe('openApp — openWindow returns null (macOS fallback)', () => {
  it('calls matchAll to find existing window clients', async () => {
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(matchAll).toHaveBeenCalled();
  });

  it('calls focus and postMessage on the first window client', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(mockClient.focus).toHaveBeenCalled();
    expect(mockClient.postMessage).toHaveBeenCalledWith({
      type: 'navigate',
      path: '/#/mail/thread/T1',
    });
  });

  it('posts the exact path string to postMessage', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail?openChat=conv-abc');

    expect(mockClient.postMessage).toHaveBeenCalledWith({
      type: 'navigate',
      path: '/#/mail?openChat=conv-abc',
    });
  });

  it('completes without error when no window clients exist', async () => {
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
  });

  it('uses only the first focusable client and does not call others', async () => {
    const client1 = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const client2 = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([client1, client2]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(client1.focus).toHaveBeenCalled();
    expect(client1.postMessage).toHaveBeenCalled();
    expect(client2.focus).not.toHaveBeenCalled();
    expect(client2.postMessage).not.toHaveBeenCalled();
  });
});

// ── threadId format regression guard (re #83) ────────────────────────────────
//
// Root cause of the "notification opens index page" bug (issue #83):
//
//   buildEmailPayload() in internal/webpush/payload.go had a guard:
//     if msg.ThreadID != 0 { out.ThreadID = fmt.Sprintf(...) }
//
//   For standalone (unthreaded) emails, the store sets ThreadID==0, so the
//   push payload contained NO threadId field.
//
//   SW resolveNotificationPath with notification.data.threadId === undefined
//   returns '/#/mail' (the inbox fallback). The user clicks the notification
//   and sees the inbox — "the index page" the maintainer reported.
//
//   The fix emits threadId unconditionally: "t<threadID>" for threaded
//   messages and "t<emailID>" for unthreaded ones, matching
//   threadIDForMessage()'s ThreadID==0 branch in the JMAP handler.
//
//   Secondary fix: threaded messages were emitting "42" (bare numeric) instead
//   of "t42" (JMAP format). Thread/get echoes back the requested ID format, so
//   bare-numeric queries would still resolve — but the "t" prefix is the
//   correct wire format and is now asserted here.
//
// These tests pin the SW-side path derivation. The companion Go tests
// TestBuildPayload_Email and TestBuildPayload_EmailWithThread assert the
// server-side emission.

describe('threadId format — regression guard (re #83)', () => {
  it('JMAP t-prefix threadId routes openApp to correct thread URL', async () => {
    // Server now sends threadId: "t42". The SW opens /#/mail/thread/t42.
    // SPA loadThread("t42") → Thread/get {ids: ["t42"]} → server responds
    // {id: "t42"} → find((t) => t.id === "t42") succeeds.
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const openApp = makeOpenApp({ openWindow, matchAll: vi.fn().mockResolvedValue([]) });

    await openApp('/#/mail/thread/t42');

    expect(openWindow).toHaveBeenCalledWith('/#/mail/thread/t42');
  });

  it('bare-numeric threadId (old server format) produces a different path than t-prefix format', () => {
    // Old server code for threaded messages emitted threadId: "42" (no "t").
    // Thread/get echoes back the requested ID format, so loadThread("42") would
    // actually resolve via Thread/get {ids: ["42"]} → {id: "42"}, not cause a
    // "not found" error. However, bare-numeric is not the correct JMAP wire
    // format; the server now emits "t42". These assertions document that the
    // SW routes whatever format it receives and that the paths differ.
    // The regression is caught at the Go level by TestBuildPayload_EmailWithThread
    // asserting threadId == "t42".
    const pathWithOldFormat = resolveNotificationPath({ kind: 'mail', threadId: '42' }, '');
    const pathWithNewFormat = resolveNotificationPath({ kind: 'mail', threadId: 't42' }, '');

    // Old format produces a non-JMAP path (still functional via echo-back, but incorrect).
    expect(pathWithOldFormat).toBe('/#/mail/thread/42');
    // New format produces the correct JMAP thread path.
    expect(pathWithNewFormat).toBe('/#/mail/thread/t42');
    // The paths differ — the "t" prefix matters for format correctness.
    expect(pathWithOldFormat).not.toBe(pathWithNewFormat);
  });

  it('full pipeline: server-format payload → notification data → correct thread URL', () => {
    // Simulates the complete push→click flow with the corrected server payload.
    // 1. Server pushes payload with threadId: "t42" (buildEmailPayload fix).
    const serverPayload = {
      kind: 'mail',
      from: 'Sender',
      body: 'Subject line',
      emailId: '42',
      threadId: 't42',
      inboxMailboxId: '5',
    };
    // 2. SW's buildNotificationOptions stores the thread ID in notification.data.
    const opts = buildNotificationOptions(serverPayload)!;
    expect(opts).not.toBeNull();
    expect(opts.data['threadId']).toBe('t42');
    // 3. On click, resolveNotificationPath reads notification.data.
    const path = resolveNotificationPath(opts.data as Record<string, unknown>, '');
    // 4. The resolved path carries the JMAP-format thread ID.
    expect(path).toBe('/#/mail/thread/t42');
    // 5. openApp(path) calls clients.openWindow('/#/mail/thread/t42'); the
    //    new tab loads the SPA at that hash, which navigates to /mail/thread/t42,
    //    loadThread("t42") runs Thread/get {ids: ["t42"]}, the server returns
    //    {id: "t42"}, and the thread renders. (SPA-side load is not exercisable
    //    in this vitest harness; the Go test covers the payload emission and a
    //    real-device test is required for the notification-click → thread-load
    //    path on macOS.)
  });

  it('full pipeline: unthreaded email uses emailId-derived thread URL', () => {
    // For unthreaded messages (ThreadID==0 in store), the server now emits
    // threadId: "t<emailId>" (buildEmailPayload fix). This matches
    // threadIDForMessage's ThreadID==0 branch in the JMAP handler.
    const serverPayload = {
      kind: 'mail',
      from: 'Sender',
      body: 'Subject',
      emailId: '99',
      threadId: 't99',
      inboxMailboxId: '3',
    };
    const opts = buildNotificationOptions(serverPayload)!;
    expect(opts.data['threadId']).toBe('t99');
    const path = resolveNotificationPath(opts.data as Record<string, unknown>, '');
    expect(path).toBe('/#/mail/thread/t99');
  });
});
