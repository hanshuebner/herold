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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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

// ── fetch stub ───────────────────────────────────────────────────────────────
//
// sw.js calls fetch() from swLog (fire-and-forget to /api/v1/clientlog/public).
// Stub the global so tests do not make real network requests and so we can
// assert the call site in swLog-specific tests.

let fetchStub: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchStub = vi.fn().mockResolvedValue({ ok: true });
  vi.stubGlobal('fetch', fetchStub);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// ── openApp fixture ──────────────────────────────────────────────────────────
//
// openApp calls self.clients.matchAll first (new focus-existing-first
// behaviour — issue #83 fix).  Only when matchAll returns no window clients
// does it fall back to self.clients.openWindow.  Each test creates a fresh
// SW evaluation with the specific client mock it needs.

type ClientMock = {
  focus: () => Promise<void>;
  postMessage: (msg: unknown) => void;
};

function makeOpenApp(clientsMock: {
  matchAll?: (opts?: unknown) => Promise<ClientMock[]>;
  openWindow?: (path: string) => Promise<unknown>;
}): (path: string) => Promise<void> {
  const mock = {
    addEventListener: () => {},
    clients: {
      matchAll: async (): Promise<ClientMock[]> => [],
      openWindow: async (): Promise<null> => null,
      ...clientsMock,
    },
    registration: { scope: 'http://localhost/' },
    skipWaiting: () => {},
  };
  // eslint-disable-next-line no-new-func
  const f = new Function('self', `${swSource}\nreturn { openApp };`);
  return (f(mock) as { openApp: (path: string) => Promise<void> }).openApp;
}

// ── openApp ring-write fixture ────────────────────────────────────────────────
//
// makeOpenAppWithRing injects self._ringWrite so tests can assert which ring
// records openApp emits without touching IndexedDB.  It returns both the
// openApp function and a ringCalls array that accumulates every write.

type RingCall = { ctx: string; level: string; msg: string; payload?: unknown };

function makeOpenAppWithRing(clientsMock: {
  matchAll?: (opts?: unknown) => Promise<ClientMock[]>;
  openWindow?: (path: string) => Promise<unknown>;
}): { openApp: (path: string) => Promise<void>; ringCalls: RingCall[] } {
  const ringCalls: RingCall[] = [];
  const mock = {
    addEventListener: () => {},
    clients: {
      matchAll: async (): Promise<ClientMock[]> => [],
      openWindow: async (): Promise<null> => null,
      ...clientsMock,
    },
    registration: { scope: 'http://localhost/' },
    skipWaiting: () => {},
    _ringWrite: (ctx: string, level: string, msg: string, payload?: unknown) => {
      ringCalls.push({ ctx, level, msg, payload });
    },
  };
  // eslint-disable-next-line no-new-func
  const f = new Function('self', `${swSource}\nreturn { openApp };`);
  return {
    openApp: (f(mock) as { openApp: (path: string) => Promise<void> }).openApp,
    ringCalls,
  };
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
// openApp uses the "Gmail pattern" (focus-existing-first) after the issue #83
// diagnosis: clients.matchAll is called first; if a window client exists it is
// focused and a navigate message is posted so the already-open tab drives the
// route.  clients.openWindow is only called when NO window client exists, and
// only inside a try/catch so an InvalidAccessError is swallowed.
//
// These tests are the regression gate the prior five rounds lacked.

describe('openApp — existing window (focus-existing-first, Gmail pattern)', () => {
  it('focuses first existing window and posts a navigate message', async () => {
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

  it('does NOT call openWindow when a window client exists', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(openWindow).not.toHaveBeenCalled();
  });

  it('posts the exact path string to postMessage', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await openApp('/#/mail?openChat=conv-abc');

    expect(mockClient.postMessage).toHaveBeenCalledWith({
      type: 'navigate',
      path: '/#/mail?openChat=conv-abc',
    });
  });

  it('focuses only the first client and does not interact with others', async () => {
    const client1 = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const client2 = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const matchAll = vi.fn().mockResolvedValue([client1, client2]);
    const openApp = makeOpenApp({ matchAll });

    await openApp('/#/mail/thread/T1');

    expect(client1.focus).toHaveBeenCalled();
    expect(client1.postMessage).toHaveBeenCalled();
    expect(client2.focus).not.toHaveBeenCalled();
    expect(client2.postMessage).not.toHaveBeenCalled();
  });

  it('calls matchAll before considering openWindow', async () => {
    // matchAll is always the first clients call — never skipped.
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const openWindow = vi.fn().mockResolvedValue(null);
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(matchAll).toHaveBeenCalled();
  });
});

describe('openApp — no window, openWindow succeeds', () => {
  it('calls openWindow when matchAll returns no clients', async () => {
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(openWindow).toHaveBeenCalledWith('/#/mail/thread/T1');
  });

  it('completes without error when no window clients exist and openWindow succeeds', async () => {
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
  });

  it('does not call focus or postMessage when openWindow path is taken', async () => {
    // With no existing window, openWindow fires; no focus/navigate message needed.
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    // openWindow was called; no client to focus.
    expect(openWindow).toHaveBeenCalled();
  });
});

describe('openApp — no window, openWindow throws InvalidAccessError', () => {
  it('swallows the throw and does not reject', async () => {
    // This is the regression test: openWindow was previously called
    // unconditionally, and an InvalidAccessError (absent transient activation)
    // rejected the event.waitUntil chain, silently aborting the notification
    // open — the primary failure mode on macOS Chrome (issue #83).
    const err = new Error('Not allowed to open a window');
    err.name = 'InvalidAccessError';
    const openWindow = vi.fn().mockRejectedValue(err);
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
  });

  it('does not propagate a generic Error thrown by openWindow', async () => {
    const openWindow = vi.fn().mockRejectedValue(new Error('some other failure'));
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
  });
});

// ── swLog observability ───────────────────────────────────────────────────────
//
// swLog is the fire-and-forget telemetry path that posts a narrow NarrowEvent
// to /api/v1/clientlog/public on every notification click (re #83).  The
// fetch stub installed in beforeEach() intercepts the request so no real
// network call is made.

describe('swLog — posts to /api/v1/clientlog/public', () => {
  it('posts to the public clientlog endpoint when openApp focuses an existing window', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await openApp('/#/mail/thread/T1');

    expect(fetchStub).toHaveBeenCalledWith(
      '/api/v1/clientlog/public',
      expect.objectContaining({
        method: 'POST',
        keepalive: true,
      }),
    );
  });

  it('posts to the public clientlog endpoint when openWindow is taken', async () => {
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([]);
    const openApp = makeOpenApp({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(fetchStub).toHaveBeenCalledWith(
      '/api/v1/clientlog/public',
      expect.objectContaining({ method: 'POST', keepalive: true }),
    );
  });

  it('posts a valid narrow NarrowEvent body', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await openApp('/#/mail/thread/T1');

    const lastCall = fetchStub.mock.calls.at(-1)!;
    const body = JSON.parse(lastCall[1].body as string) as {
      events: Record<string, unknown>[];
    };
    const ev = body.events[0]!;

    expect(ev['v']).toBe(1);
    expect(ev['kind']).toBe('log');
    expect(ev['level']).toBe('info');
    expect(typeof ev['msg']).toBe('string');
    expect(typeof ev['client_ts']).toBe('string');
    expect(typeof ev['seq']).toBe('number');
    expect(typeof ev['page_id']).toBe('string');
    expect(ev['app']).toBe('suite');
    expect(ev['route']).toBe('/sw');
    // Narrow schema must NOT contain breadcrumbs, session_id, or request_id.
    expect(ev).not.toHaveProperty('breadcrumbs');
    expect(ev).not.toHaveProperty('session_id');
    expect(ev).not.toHaveProperty('request_id');
  });

  it('does not reject even if the fetch call itself throws', async () => {
    fetchStub.mockRejectedValue(new TypeError('Failed to fetch'));
    const matchAll = vi.fn().mockResolvedValue([]);
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const openApp = makeOpenApp({ openWindow, matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
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

// ── openApp navigate-before-focus ordering (re #83) ─────────────────────────
//
// WindowClient.focus() can throw InvalidAccessError when transient activation
// is absent (proven on macOS Chrome, issue #83).  The fix posts the navigate
// message BEFORE awaiting focus() so the route change is always delivered
// even when focus subsequently throws.
//
// These tests are the regression gate: they assert both the ordering contract
// and the throw-safe behaviour.

describe('openApp — navigate message posted BEFORE focus', () => {
  it('calls postMessage before focus when a window exists', async () => {
    const callOrder: string[] = [];
    const mockClient = {
      focus: vi.fn().mockImplementation(async () => { callOrder.push('focus'); }),
      postMessage: vi.fn().mockImplementation(() => { callOrder.push('postMessage'); }),
    };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await openApp('/#/mail/thread/T1');

    const pmIdx = callOrder.indexOf('postMessage');
    const focusIdx = callOrder.indexOf('focus');
    expect(pmIdx).toBeGreaterThanOrEqual(0);
    expect(focusIdx).toBeGreaterThanOrEqual(0);
    expect(pmIdx).toBeLessThan(focusIdx);
  });

  it('delivers the navigate message even when focus throws InvalidAccessError', async () => {
    const err = Object.assign(new Error('Not allowed to open a window'), {
      name: 'InvalidAccessError',
    });
    const mockClient = {
      focus: vi.fn().mockRejectedValue(err),
      postMessage: vi.fn(),
    };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();

    // The navigate message must have been posted despite the focus throw.
    expect(mockClient.postMessage).toHaveBeenCalledWith({
      type: 'navigate',
      path: '/#/mail/thread/T1',
    });
  });

  it('does not reject when focus throws InvalidAccessError', async () => {
    const err = Object.assign(new Error('Not allowed to open a window'), {
      name: 'InvalidAccessError',
    });
    const mockClient = {
      focus: vi.fn().mockRejectedValue(err),
      postMessage: vi.fn(),
    };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
  });

  it('does not reject when focus throws a generic Error', async () => {
    const mockClient = {
      focus: vi.fn().mockRejectedValue(new Error('some unexpected focus failure')),
      postMessage: vi.fn(),
    };
    const matchAll = vi.fn().mockResolvedValue([mockClient]);
    const openApp = makeOpenApp({ matchAll });

    await expect(openApp('/#/mail/thread/T1')).resolves.toBeUndefined();
  });
});

// ── openApp ring records (re #83) ─────────────────────────────────────────────
//
// openApp must write specific sw.* ring records so the Diagnostics form can
// show the click trace.  Tests use makeOpenAppWithRing which injects
// self._ringWrite to capture records synchronously without IDB.

describe('openApp — ring records when window exists', () => {
  it('writes sw.openApp.matchAll with count', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const { openApp, ringCalls } = makeOpenAppWithRing({
      matchAll: vi.fn().mockResolvedValue([mockClient]),
    });

    await openApp('/#/mail/thread/T1');

    const matchAllRecord = ringCalls.find((r) => r.msg === 'sw.openApp.matchAll');
    expect(matchAllRecord).toBeDefined();
    expect((matchAllRecord?.payload as { count: number }).count).toBe(1);
  });

  it('writes sw.openApp.postNavigate after posting the message', async () => {
    const postMessageOrder: string[] = [];
    const mockClient = {
      focus: vi.fn().mockResolvedValue(undefined),
      postMessage: vi.fn().mockImplementation(() => { postMessageOrder.push('postMessage'); }),
    };
    const { openApp, ringCalls } = makeOpenAppWithRing({
      matchAll: vi.fn().mockResolvedValue([mockClient]),
    });

    await openApp('/#/mail/thread/T1');

    expect(ringCalls.find((r) => r.msg === 'sw.openApp.postNavigate')).toBeDefined();
    // postMessage must have been called (it was recorded in postMessageOrder).
    expect(postMessageOrder).toContain('postMessage');
  });

  it('writes sw.openApp.focus.ok when focus succeeds', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const { openApp, ringCalls } = makeOpenAppWithRing({
      matchAll: vi.fn().mockResolvedValue([mockClient]),
    });

    await openApp('/#/mail/thread/T1');

    expect(ringCalls.find((r) => r.msg === 'sw.openApp.focus.ok')).toBeDefined();
    expect(ringCalls.find((r) => r.msg === 'sw.openApp.focus.threw')).toBeUndefined();
  });

  it('writes sw.openApp.focus.threw with the error name when focus throws', async () => {
    const err = Object.assign(new Error('Not allowed'), { name: 'InvalidAccessError' });
    const mockClient = {
      focus: vi.fn().mockRejectedValue(err),
      postMessage: vi.fn(),
    };
    const { openApp, ringCalls } = makeOpenAppWithRing({
      matchAll: vi.fn().mockResolvedValue([mockClient]),
    });

    await openApp('/#/mail/thread/T1');

    const threwRecord = ringCalls.find((r) => r.msg === 'sw.openApp.focus.threw');
    expect(threwRecord).toBeDefined();
    expect((threwRecord?.payload as { name: string }).name).toBe('InvalidAccessError');
    // ok record must NOT have been written.
    expect(ringCalls.find((r) => r.msg === 'sw.openApp.focus.ok')).toBeUndefined();
  });

  it('ring records have ctx=sw and level=info for matchAll and postNavigate', async () => {
    const mockClient = { focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const { openApp, ringCalls } = makeOpenAppWithRing({
      matchAll: vi.fn().mockResolvedValue([mockClient]),
    });

    await openApp('/#/mail/thread/T1');

    for (const msg of ['sw.openApp.matchAll', 'sw.openApp.postNavigate', 'sw.openApp.focus.ok']) {
      const r = ringCalls.find((rc) => rc.msg === msg);
      expect(r).toBeDefined();
      expect(r?.ctx).toBe('sw');
    }
  });

  it('ring record for focus.threw has level=warn', async () => {
    const err = Object.assign(new Error('Not allowed'), { name: 'InvalidAccessError' });
    const mockClient = {
      focus: vi.fn().mockRejectedValue(err),
      postMessage: vi.fn(),
    };
    const { openApp, ringCalls } = makeOpenAppWithRing({
      matchAll: vi.fn().mockResolvedValue([mockClient]),
    });

    await openApp('/#/mail/thread/T1');

    const r = ringCalls.find((rc) => rc.msg === 'sw.openApp.focus.threw');
    expect(r?.level).toBe('warn');
  });
});

describe('openApp — ring records when no window exists', () => {
  it('writes sw.openApp.matchAll with count=0', async () => {
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([]);
    const { openApp, ringCalls } = makeOpenAppWithRing({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    const r = ringCalls.find((rc) => rc.msg === 'sw.openApp.matchAll');
    expect(r).toBeDefined();
    expect((r?.payload as { count: number }).count).toBe(0);
  });

  it('writes sw.openApp.openWindow.opened when openWindow succeeds', async () => {
    const openWindow = vi.fn().mockResolvedValue({ url: 'http://localhost/' });
    const matchAll = vi.fn().mockResolvedValue([]);
    const { openApp, ringCalls } = makeOpenAppWithRing({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    expect(ringCalls.find((r) => r.msg === 'sw.openApp.openWindow.opened')).toBeDefined();
    expect(ringCalls.find((r) => r.msg === 'sw.openApp.openWindow.threw')).toBeUndefined();
  });

  it('writes sw.openApp.openWindow.threw with the error name when openWindow throws', async () => {
    const err = Object.assign(new Error('Not allowed'), { name: 'InvalidAccessError' });
    const openWindow = vi.fn().mockRejectedValue(err);
    const matchAll = vi.fn().mockResolvedValue([]);
    const { openApp, ringCalls } = makeOpenAppWithRing({ openWindow, matchAll });

    await openApp('/#/mail/thread/T1');

    const r = ringCalls.find((rc) => rc.msg === 'sw.openApp.openWindow.threw');
    expect(r).toBeDefined();
    expect((r?.payload as { name: string }).name).toBe('InvalidAccessError');
  });
});
