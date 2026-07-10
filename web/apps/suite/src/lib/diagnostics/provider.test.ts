/**
 * Unit tests for the webapp-diagnostics/1 provider.
 *
 * Simulates the postMessage handshake documented in the bug-reporter
 * extension's PROTOCOL.md: a same-origin `message` event carrying
 * { __webappDiagnostics: 'request', v: 1, nonce } must produce a reply
 * carrying the same nonce and a well-formed Descriptor. Forged (foreign
 * origin / non-window source) and version-mismatched requests must be
 * silently ignored.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// ── Singleton mocks ─────────────────────────────────────────────────────

const mockAuth: {
  status: 'idle' | 'bootstrapping' | 'ready' | 'unauthenticated' | 'error';
  principalId: string | null;
  session: { username: string } | null;
} = {
  status: 'ready',
  principalId: '42',
  session: { username: 'alice@example.local' },
};
vi.mock('../auth/auth.svelte', () => ({ auth: mockAuth }));

const mockRouter = {
  current: '/mail/thread/abc-123',
  get parts(): string[] {
    return this.current.split('?')[0]!.split('/').filter(Boolean);
  },
};
vi.mock('../router/router.svelte', () => ({ router: mockRouter }));

const mockCompose = { isOpen: false };
vi.mock('../compose/compose.svelte', () => ({ compose: mockCompose }));

const mockComposeStack = { minimized: [] as unknown[] };
vi.mock('../compose/compose-stack.svelte', () => ({ composeStack: mockComposeStack }));

const mockDebugRecords: Array<{
  ts: string;
  ctx: 'sw' | 'page';
  level: string;
  msg: string;
  payload?: string;
}> = [
  {
    ts: '2026-07-10T12:00:00.000Z',
    ctx: 'page',
    level: 'error',
    msg: 'api-error',
    payload: JSON.stringify({ status: 500, path: '/jmap' }),
  },
  {
    ts: '2026-07-10T12:00:01.000Z',
    ctx: 'page',
    level: 'info',
    msg: 'route-changed',
  },
];
const mockReadAll = vi.fn(async () => mockDebugRecords);
vi.mock('../debug-ring/debug-ring', () => ({ readAll: mockReadAll }));

const { initDiagnosticsProvider, _internals_forTest } = await import('./provider');

// ── Helpers ───────────────────────────────────────────────────────────

/**
 * Dispatch a simulated incoming request message. Uses dispatchEvent with an
 * explicit `source: window` rather than window.postMessage(): happy-dom's
 * own postMessage loopback synthesizes a fresh Window-like object for
 * event.source that is not reference-equal to the test's `window` global,
 * even though real browsers preserve identity for same-window messaging.
 * Constructing the MessageEvent directly is the standard way to simulate an
 * incoming 'message' and lets the provider's real event.source === window
 * check (the mandatory PROTOCOL.md verification) run unmodified.
 */
function dispatchRequest(data: unknown, opts?: { origin?: string; source?: unknown }): void {
  window.dispatchEvent(
    new MessageEvent('message', {
      data,
      origin: opts?.origin ?? location.origin,
      source: 'source' in (opts ?? {}) ? (opts!.source as Window) : window,
    }),
  );
}

function postRequest(v: unknown, nonce: unknown): void {
  dispatchRequest({ __webappDiagnostics: 'request', v, nonce });
}

function waitForResponse(nonce: string, timeoutMs = 500): Promise<Record<string, unknown> | null> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      window.removeEventListener('message', handler);
      resolve(null);
    }, timeoutMs);
    function handler(event: MessageEvent): void {
      const data = event.data as Record<string, unknown> | undefined;
      if (data?.['__webappDiagnostics'] === 'response' && data['nonce'] === nonce) {
        clearTimeout(timer);
        window.removeEventListener('message', handler);
        resolve(data);
      }
    }
    window.addEventListener('message', handler);
  });
}

describe('webapp-diagnostics/1 provider', () => {
  beforeEach(() => {
    mockAuth.status = 'ready';
    mockAuth.principalId = '42';
    mockAuth.session = { username: 'alice@example.local' };
    mockRouter.current = '/mail/thread/abc-123';
    mockCompose.isOpen = false;
    mockComposeStack.minimized = [];
    mockReadAll.mockClear();
    initDiagnosticsProvider();
  });

  afterEach(() => {
    _internals_forTest.resetInstalled();
  });

  it('answers a same-origin v1 request with a well-formed Descriptor', async () => {
    const nonce = 'nonce-well-formed';
    const responsePromise = waitForResponse(nonce);
    postRequest(1, nonce);
    const response = await responsePromise;

    expect(response).not.toBeNull();
    expect(response!['__webappDiagnostics']).toBe('response');
    expect(response!['v']).toBe(1);
    expect(response!['nonce']).toBe(nonce);

    const payload = response!['payload'] as Record<string, unknown>;
    expect(payload).toBeTruthy();
    const app = payload['app'] as Record<string, unknown>;
    expect(app['id']).toBe('herold-suite');
    expect(app['name']).toBe('Herold Suite');
    expect(typeof app['version']).toBe('string');

    const principal = payload['principal'] as Record<string, unknown>;
    expect(principal['id']).toBe('42');
    expect(principal['label']).toBe('alice@example.local');

    const context = payload['context'] as Record<string, unknown>;
    expect(context['route']).toBe('/mail/thread/abc-123');
    expect(context['threadId']).toBe('abc-123');
    expect(context['composeOpen']).toBe(false);

    expect(Array.isArray(payload['logs'])).toBe(true);
    const logs = payload['logs'] as Array<Record<string, unknown>>;
    expect(logs.length).toBe(2);
    expect(logs[0]!['level']).toBe('error');
    expect(logs[0]!['msg']).toBe('api-error');
    expect(typeof logs[0]!['ts']).toBe('number');
    // Non-sensitive small payload is forwarded, parsed back to an object.
    expect(logs[0]!['payload']).toEqual({ status: 500, path: '/jmap' });

    expect(payload['private']).toBeNull();
    expect(payload['captureCookies']).toEqual(['herold_session', 'herold_public_csrf']);
  });

  it('omits principal when not logged in', async () => {
    mockAuth.status = 'unauthenticated';
    mockAuth.principalId = null;
    mockAuth.session = null;

    const nonce = 'nonce-logged-out';
    const responsePromise = waitForResponse(nonce);
    postRequest(1, nonce);
    const response = await responsePromise;

    const payload = response!['payload'] as Record<string, unknown>;
    expect(payload['principal']).toBeUndefined();
  });

  it('redacts a debug-ring payload that looks like it carries a credential', async () => {
    mockDebugRecords.push({
      ts: '2026-07-10T12:00:02.000Z',
      ctx: 'page',
      level: 'debug',
      msg: 'auth-refresh',
      payload: JSON.stringify({ token: 'super-secret-value' }),
    });

    const nonce = 'nonce-redact';
    const responsePromise = waitForResponse(nonce);
    postRequest(1, nonce);
    const response = await responsePromise;

    const payload = response!['payload'] as Record<string, unknown>;
    const logs = payload['logs'] as Array<Record<string, unknown>>;
    const sensitiveLog = logs.find((l) => l['msg'] === 'auth-refresh');
    expect(sensitiveLog).toBeTruthy();
    expect(sensitiveLog!['payload']).toBeUndefined();

    mockDebugRecords.pop();
  });

  it('ignores a request with the wrong protocol version', async () => {
    const nonce = 'nonce-wrong-version';
    const responsePromise = waitForResponse(nonce, 150);
    postRequest(2, nonce);
    const response = await responsePromise;
    expect(response).toBeNull();
  });

  it('ignores a message with a foreign origin even if window-sourced', async () => {
    const nonce = 'nonce-foreign-origin';
    const responsePromise = waitForResponse(nonce, 150);
    window.dispatchEvent(
      new MessageEvent('message', {
        data: { __webappDiagnostics: 'request', v: 1, nonce },
        origin: 'https://evil.example',
        source: window,
      }),
    );
    const response = await responsePromise;
    expect(response).toBeNull();
  });

  it('ignores a message whose source is not window', async () => {
    const nonce = 'nonce-foreign-source';
    const responsePromise = waitForResponse(nonce, 150);
    window.dispatchEvent(
      new MessageEvent('message', {
        data: { __webappDiagnostics: 'request', v: 1, nonce },
        origin: location.origin,
        source: null,
      }),
    );
    const response = await responsePromise;
    expect(response).toBeNull();
  });

  it('ignores messages missing the __webappDiagnostics request marker', async () => {
    const nonce = 'nonce-unrelated';
    const responsePromise = waitForResponse(nonce, 150);
    window.postMessage({ some: 'unrelated payload', nonce }, location.origin);
    const response = await responsePromise;
    expect(response).toBeNull();
  });
});
