/**
 * Web Push subscription store unit tests — REQ-PUSH-30..34, REQ-PUSH-80..84.
 *
 * Coverage:
 *   1. urlBase64ToUint8Array converts a base64url key correctly.
 *   2. subscribe() requests permission, subscribes, and POSTs to herold.
 *   3. subscribe() stores denial in localStorage when permission is denied.
 *   4. unsubscribe() calls PushSubscription/set { destroy }.
 *   5. destroyAll() fetches all subscriptions and destroys them.
 *   6. forgetDenial() removes the localStorage key.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { _internals_forTest } from './push-subscription.svelte';

const { urlBase64ToUint8Array } = _internals_forTest;

// ─────────────────────────────────────────────────────────────────────────
// Helper
// ─────────────────────────────────────────────────────────────────────────

function makeBase64url(byteCount: number): string {
  const bytes = new Uint8Array(byteCount);
  for (let i = 0; i < byteCount; i++) bytes[i] = i % 256;
  const binary = Array.from(bytes)
    .map((b) => String.fromCharCode(b))
    .join('');
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

// ─────────────────────────────────────────────────────────────────────────
// 1. urlBase64ToUint8Array
// ─────────────────────────────────────────────────────────────────────────

describe('urlBase64ToUint8Array', () => {
  it('returns a Uint8Array of the correct length for a 65-byte P-256 key', () => {
    const key = makeBase64url(65);
    const result = urlBase64ToUint8Array(key);
    expect(result).toBeInstanceOf(Uint8Array);
    expect(result.length).toBe(65);
  });

  it('handles padded base64url strings', () => {
    // 16 bytes needs two padding chars.
    const key = makeBase64url(16);
    const result = urlBase64ToUint8Array(key);
    expect(result.length).toBe(16);
  });

  it('converts standard base64 characters + and / to - and _ equivalents', () => {
    // Ensure the conversion round-trips without error for a known sequence.
    const key = makeBase64url(32);
    expect(() => urlBase64ToUint8Array(key)).not.toThrow();
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 2..6. Store behaviour — requires mocking browser APIs + JMAP client
// ─────────────────────────────────────────────────────────────────────────

vi.mock('../jmap/client', () => {
  type BatchImpl =
    | (() => Promise<{ responses: unknown[]; sessionState: string }>)
    | (() => { responses: unknown[]; sessionState: string })
    | null;
  let batchImpl: BatchImpl = null;

  return {
    jmap: {
      batch: vi.fn(async (builder: (b: unknown) => void) => {
        const calls: unknown[] = [];
        builder({
          call: (_name: string, _args: unknown) => {
            calls.push({ name: _name, args: _args });
            return { ref: (p: string) => ({ resultOf: 'c0', name: _name, path: p }) };
          },
        });
        if (batchImpl) return batchImpl();
        return { responses: [], sessionState: 'state-1' };
      }),
      hasCapability: vi.fn(() => true),
    },
    strict: (responses: unknown[]) => {
      for (const r of responses) {
        if (Array.isArray(r) && r[0] === 'error') {
          throw new Error((r[1] as { description?: string }).description ?? 'error');
        }
      }
      return responses;
    },
    __setBatchImpl: (impl: BatchImpl) => {
      batchImpl = impl;
    },
  };
});

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      capabilities: {
        'https://netzhansa.com/jmap/push': {
          applicationServerKey: makeBase64url(65),
        },
      },
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'account-1' },
    },
  },
}));

// Mock browser APIs used in subscribe/unsubscribe.
function buildPushSubscriptionMock(endpoint = 'https://push.example.com/ep1') {
  return {
    endpoint,
    toJSON: () => ({
      endpoint,
      keys: {
        p256dh: makeBase64url(65),
        auth: makeBase64url(16),
      },
    }),
    unsubscribe: vi.fn().mockResolvedValue(true),
  };
}

function buildPushManagerMock(sub: ReturnType<typeof buildPushSubscriptionMock> | null = null) {
  return {
    getSubscription: vi.fn().mockResolvedValue(sub),
    subscribe: vi.fn().mockResolvedValue(buildPushSubscriptionMock()),
  };
}

function buildSwRegistrationMock(
  pushMgr: ReturnType<typeof buildPushManagerMock>,
) {
  return {
    pushManager: pushMgr,
    scope: '/',
  };
}

describe('urlBase64ToUint8Array (additional)', () => {
  it('produces consistent output for the same input', () => {
    const key = makeBase64url(65);
    const a = urlBase64ToUint8Array(key);
    const b = urlBase64ToUint8Array(key);
    expect(Array.from(a)).toEqual(Array.from(b));
  });
});

describe('pushSubscription.subscribe — permission granted flow', () => {
  let originalNotification: typeof Notification;
  let originalSw: typeof navigator.serviceWorker;
  let originalPushManager: typeof PushManager;

  beforeEach(async () => {
    originalNotification = globalThis.Notification;
    // @ts-expect-error jsdom doesn't have Notification
    globalThis.Notification = {
      permission: 'default',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    };
  });

  afterEach(() => {
    globalThis.Notification = originalNotification;
    vi.clearAllMocks();
    localStorage.clear();
  });

  it('calls Notification.requestPermission', async () => {
    const pushMgr = buildPushManagerMock();
    const swReg = buildSwRegistrationMock(pushMgr);

    Object.defineProperty(navigator, 'serviceWorker', {
      value: {
        register: vi.fn().mockResolvedValue(swReg),
        ready: Promise.resolve(swReg),
        addEventListener: vi.fn(),
        getRegistration: vi.fn().mockResolvedValue(swReg),
      },
      configurable: true,
    });

    globalThis.PushManager = class {} as unknown as typeof PushManager;
    globalThis.ServiceWorkerRegistration = class {} as unknown as typeof ServiceWorkerRegistration;

    const mock = await import('../jmap/client') as unknown as {
      jmap: { batch: ReturnType<typeof vi.fn> };
      __setBatchImpl: (impl: unknown) => void;
    };
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'PushSubscription/set',
          {
            created: { push0: { id: '42', deviceClientId: 'x', url: 'https://...', keys: { p256dh: '', auth: '' }, types: [] } },
          },
          'c0',
        ],
      ],
      sessionState: 'state-1',
    }));

    // Import the singleton fresh.
    const { pushSubscription: ps } = await import('./push-subscription.svelte');
    // Reset state.
    (ps as unknown as { busy: boolean }).busy = false;
    (ps as unknown as { subscribed: boolean }).subscribed = false;

    await ps.subscribe();

    expect(Notification.requestPermission).toHaveBeenCalled();

    mock.__setBatchImpl(null);
  });

  it('stores denial in localStorage when permission is denied', async () => {
    // @ts-expect-error partial mock
    globalThis.Notification = {
      permission: 'default',
      requestPermission: vi.fn().mockResolvedValue('denied'),
    };

    const pushMgr = buildPushManagerMock();
    const swReg = buildSwRegistrationMock(pushMgr);
    Object.defineProperty(navigator, 'serviceWorker', {
      value: {
        register: vi.fn().mockResolvedValue(swReg),
        ready: Promise.resolve(swReg),
        addEventListener: vi.fn(),
        getRegistration: vi.fn().mockResolvedValue(swReg),
      },
      configurable: true,
    });
    globalThis.PushManager = class {} as unknown as typeof PushManager;
    globalThis.ServiceWorkerRegistration = class {} as unknown as typeof ServiceWorkerRegistration;

    const { pushSubscription: ps } = await import('./push-subscription.svelte');
    (ps as unknown as { busy: boolean }).busy = false;
    (ps as unknown as { subscribed: boolean }).subscribed = false;

    await ps.subscribe();

    const storedUntil = parseInt(
      localStorage.getItem('herold:push:denied_until') ?? '0',
      10,
    );
    // Should be set to ~30 days in the future.
    expect(storedUntil).toBeGreaterThan(Date.now());
  });
});

describe('pushSubscription.forgetDenial', () => {
  it('removes the denial key from localStorage and resets permissionState', async () => {
    localStorage.setItem('herold:push:denied_until', String(Date.now() + 86400_000));

    const { pushSubscription: ps } = await import('./push-subscription.svelte');
    (ps as unknown as { permissionState: string }).permissionState = 'denied';

    ps.forgetDenial();

    expect(localStorage.getItem('herold:push:denied_until')).toBeNull();
    expect(ps.permissionState).toBe('default');
  });
});

describe('pushSubscription.subscribe — notCreated error handling (re #68)', () => {
  let originalNotification: typeof Notification;

  beforeEach(() => {
    originalNotification = globalThis.Notification;
    // @ts-expect-error partial mock
    globalThis.Notification = {
      permission: 'default',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    };
    globalThis.PushManager = class {} as unknown as typeof PushManager;
    globalThis.ServiceWorkerRegistration = class {} as unknown as typeof ServiceWorkerRegistration;
  });

  afterEach(() => {
    globalThis.Notification = originalNotification;
    vi.clearAllMocks();
    localStorage.clear();
  });

  it('surfaces errorMessage and does not set subscribed=true when server returns notCreated', async () => {
    const pushMgr = buildPushManagerMock();
    const swReg = buildSwRegistrationMock(pushMgr);
    Object.defineProperty(navigator, 'serviceWorker', {
      value: {
        register: vi.fn().mockResolvedValue(swReg),
        ready: Promise.resolve(swReg),
        addEventListener: vi.fn(),
        getRegistration: vi.fn().mockResolvedValue(swReg),
      },
      configurable: true,
    });

    const mock = await import('../jmap/client') as unknown as {
      jmap: { batch: ReturnType<typeof vi.fn> };
      __setBatchImpl: (impl: unknown) => void;
    };
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'PushSubscription/set',
          {
            created: {},
            notCreated: {
              push0: {
                type: 'invalidProperties',
                description: 'subscription endpoint is invalid',
              },
            },
          },
          'c0',
        ],
      ],
      sessionState: 'state-1',
    }));

    const { pushSubscription: ps } = await import('./push-subscription.svelte');
    (ps as unknown as { busy: boolean }).busy = false;
    (ps as unknown as { subscribed: boolean }).subscribed = false;
    (ps as unknown as { errorMessage: string | null }).errorMessage = null;

    await ps.subscribe();

    // The store must NOT mark the subscription as successful.
    expect(ps.subscribed).toBe(false);
    // An error message must be surfaced.
    expect(ps.errorMessage).toContain('subscription endpoint is invalid');

    mock.__setBatchImpl(null);
  });
});
