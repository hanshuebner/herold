/**
 * Unit tests for the service worker's mail-dismiss push handling
 * (REQ-PUSH-100..104, re #481).
 *
 * sw.js lives in public/ and is not a module, so its source is evaluated in
 * a controlled context (same technique as sw-notificationclick.test.ts) and
 * the internal handleMailDismiss function and the push event listener it is
 * wired from are exercised directly.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const __dir = dirname(fileURLToPath(import.meta.url));
const swSource = readFileSync(resolve(__dir, '../../../public/sw.js'), 'utf-8');

let fetchStub: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchStub = vi.fn().mockResolvedValue({ ok: true });
  vi.stubGlobal('fetch', fetchStub);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

type RingCall = { ctx: string; level: string; msg: string; payload?: unknown };
type NotificationMock = { data?: Record<string, unknown>; close: () => void };

function makeHandleMailDismiss(notifications: NotificationMock[]): {
  handleMailDismiss: (payload: Record<string, unknown>) => Promise<void>;
  ringCalls: RingCall[];
  getNotifications: ReturnType<typeof vi.fn>;
} {
  const ringCalls: RingCall[] = [];
  const getNotifications = vi.fn().mockResolvedValue(notifications);
  const mock = {
    addEventListener: () => {},
    clients: {},
    registration: { scope: 'http://localhost/', getNotifications },
    skipWaiting: () => {},
    _ringWrite: (ctx: string, level: string, msg: string, payload?: unknown) => {
      ringCalls.push({ ctx, level, msg, payload });
    },
  };
  // eslint-disable-next-line no-new-func
  const f = new Function('self', `${swSource}\nreturn { handleMailDismiss };`);
  return {
    handleMailDismiss: (
      f(mock) as { handleMailDismiss: (payload: Record<string, unknown>) => Promise<void> }
    ).handleMailDismiss,
    ringCalls,
    getNotifications,
  };
}

describe('handleMailDismiss — mail-dismiss push (re #481)', () => {
  it('closes the notification whose thread tag and emailId match', async () => {
    const notif: NotificationMock = {
      data: { kind: 'mail', emailId: '42', threadId: 't7' },
      close: vi.fn(),
    };
    const { handleMailDismiss, getNotifications } = makeHandleMailDismiss([notif]);

    await handleMailDismiss({
      kind: 'mail-dismiss',
      threadId: 't7',
      emailId: '42',
      reason: 'seen',
    });

    expect(getNotifications).toHaveBeenCalledWith({ tag: 't7' });
    expect(notif.close).toHaveBeenCalledTimes(1);
  });

  it('falls back to emailId as the lookup tag when threadId is absent', async () => {
    const notif: NotificationMock = { data: { kind: 'mail', emailId: '42' }, close: vi.fn() };
    const { handleMailDismiss, getNotifications } = makeHandleMailDismiss([notif]);

    await handleMailDismiss({ kind: 'mail-dismiss', emailId: '42', reason: 'destroyed' });

    expect(getNotifications).toHaveBeenCalledWith({ tag: '42' });
    expect(notif.close).toHaveBeenCalledTimes(1);
  });

  it('leaves a same-thread notification for a different, still-unread message (conversation rule)', async () => {
    // The tag-based lookup matches on thread, but the notification currently
    // showing represents email 99, not the dismissed email 42 -- a still
    // unread sibling in the same thread, so it stays.
    const notif: NotificationMock = {
      data: { kind: 'mail', emailId: '99', threadId: 't7' },
      close: vi.fn(),
    };
    const { handleMailDismiss } = makeHandleMailDismiss([notif]);

    await handleMailDismiss({
      kind: 'mail-dismiss',
      threadId: 't7',
      emailId: '42',
      reason: 'seen',
    });

    expect(notif.close).not.toHaveBeenCalled();
  });

  it('ignores a non-mail notification sharing the same tag', async () => {
    const notif: NotificationMock = { data: { kind: 'chat' }, close: vi.fn() };
    const { handleMailDismiss } = makeHandleMailDismiss([notif]);

    await handleMailDismiss({
      kind: 'mail-dismiss',
      threadId: 't7',
      emailId: '42',
      reason: 'seen',
    });

    expect(notif.close).not.toHaveBeenCalled();
  });

  it('closes every matching notification when more than one is returned', async () => {
    const n1: NotificationMock = {
      data: { kind: 'mail', emailId: '42', threadId: 't7' },
      close: vi.fn(),
    };
    const n2: NotificationMock = {
      data: { kind: 'mail', emailId: '42', threadId: 't7' },
      close: vi.fn(),
    };
    const { handleMailDismiss } = makeHandleMailDismiss([n1, n2]);

    await handleMailDismiss({
      kind: 'mail-dismiss',
      threadId: 't7',
      emailId: '42',
      reason: 'left-inbox',
    });

    expect(n1.close).toHaveBeenCalledTimes(1);
    expect(n2.close).toHaveBeenCalledTimes(1);
  });

  it('records the dismissal in the debug ring with reason and closed count', async () => {
    const notif: NotificationMock = {
      data: { kind: 'mail', emailId: '42', threadId: 't7' },
      close: vi.fn(),
    };
    const { handleMailDismiss, ringCalls } = makeHandleMailDismiss([notif]);

    await handleMailDismiss({
      kind: 'mail-dismiss',
      threadId: 't7',
      emailId: '42',
      reason: 'seen',
    });

    expect(ringCalls).toContainEqual(
      expect.objectContaining({
        ctx: 'sw',
        msg: 'sw.push.dismiss',
        payload: expect.objectContaining({
          tag: 't7',
          emailId: '42',
          reason: 'seen',
          closed: 1,
        }),
      }),
    );
  });

  it('is a no-op and does not call getNotifications when neither threadId nor emailId is present', async () => {
    const { handleMailDismiss, getNotifications } = makeHandleMailDismiss([]);

    await handleMailDismiss({ kind: 'mail-dismiss', reason: 'seen' });

    expect(getNotifications).not.toHaveBeenCalled();
  });
});

// ── push event listener wiring ────────────────────────────────────────────

type PushHandler = (event: {
  data: { json: () => unknown } | null;
  waitUntil: (p: Promise<unknown>) => void;
}) => void;

function capturePushHandler(overrides: {
  getNotifications?: ReturnType<typeof vi.fn>;
  showNotification?: ReturnType<typeof vi.fn>;
  matchAll?: ReturnType<typeof vi.fn>;
}): {
  push: PushHandler;
  showNotification: ReturnType<typeof vi.fn>;
  getNotifications: ReturnType<typeof vi.fn>;
} {
  const listeners: Record<string, (event: unknown) => void> = {};
  const showNotification = overrides.showNotification ?? vi.fn().mockResolvedValue(undefined);
  const getNotifications = overrides.getNotifications ?? vi.fn().mockResolvedValue([]);
  const matchAll = overrides.matchAll ?? vi.fn().mockResolvedValue([]);
  const mock = {
    addEventListener: (type: string, handler: (event: unknown) => void) => {
      listeners[type] = handler;
    },
    clients: { matchAll },
    registration: { scope: 'http://localhost/', showNotification, getNotifications },
    skipWaiting: () => {},
    _ringWrite: () => {},
  };
  // eslint-disable-next-line no-new-func
  const f = new Function('self', swSource);
  f(mock);
  return {
    push: listeners.push as PushHandler,
    showNotification,
    getNotifications,
  };
}

describe('push event — mail-dismiss dispatch (re #481)', () => {
  it('never calls showNotification for a mail-dismiss payload', async () => {
    const { push, showNotification, getNotifications } = capturePushHandler({});
    const payload = { kind: 'mail-dismiss', threadId: 't7', emailId: '42', reason: 'seen' };

    let waited: Promise<unknown> = Promise.resolve();
    push({
      data: { json: () => payload },
      waitUntil: (p) => {
        waited = p;
      },
    });
    await waited;

    expect(showNotification).not.toHaveBeenCalled();
    expect(getNotifications).toHaveBeenCalledWith({ tag: 't7' });
  });

  it('closes the matching notification via the full push dispatch path', async () => {
    const notif: NotificationMock = {
      data: { kind: 'mail', emailId: '42', threadId: 't7' },
      close: vi.fn(),
    };
    const getNotifications = vi.fn().mockResolvedValue([notif]);
    const { push, showNotification } = capturePushHandler({ getNotifications });
    const payload = { kind: 'mail-dismiss', threadId: 't7', emailId: '42', reason: 'destroyed' };

    let waited: Promise<unknown> = Promise.resolve();
    push({
      data: { json: () => payload },
      waitUntil: (p) => {
        waited = p;
      },
    });
    await waited;

    expect(notif.close).toHaveBeenCalledTimes(1);
    expect(showNotification).not.toHaveBeenCalled();
  });

  it('does not suppress the dismiss handling when a tab is open (unlike arrival pushes)', async () => {
    // Arrival pushes are suppressed while a tab is open (re #83) because the
    // page shows its own desktop notification instead. A dismissal must run
    // regardless of open tabs -- it withdraws state, it does not show one.
    const matchAll = vi.fn().mockResolvedValue([{}]);
    const getNotifications = vi.fn().mockResolvedValue([]);
    const { push } = capturePushHandler({ matchAll, getNotifications });
    const payload = { kind: 'mail-dismiss', threadId: 't7', emailId: '42', reason: 'seen' };

    let waited: Promise<unknown> = Promise.resolve();
    push({
      data: { json: () => payload },
      waitUntil: (p) => {
        waited = p;
      },
    });
    await waited;

    expect(getNotifications).toHaveBeenCalledWith({ tag: 't7' });
  });
});
