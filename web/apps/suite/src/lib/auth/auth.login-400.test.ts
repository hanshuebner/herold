/**
 * auth.svelte — HTTP 400 response handling (re #53).
 *
 * The login endpoint returns 400 when required fields are missing or
 * malformed. Before this fix the auth module surfaced the raw string
 * "Unexpected response: HTTP 400" to the UI.
 *
 * After the fix:
 *   - auth.errorMessage is set to a friendly English string.
 *   - The thrown Error carries the same friendly message.
 *   - The raw "HTTP 400" string never appears in either.
 *   - If the 400 body contains a "message" field the module uses it.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// ── Singleton deps — must be mocked before the dynamic import ─────────────

const jmapBootstrap = vi.fn(async () => ({
  capabilities: {},
  primaryAccounts: {},
  username: 'user@example.com',
  apiUrl: '/jmap/api',
  downloadUrl: '/jmap/download/{accountId}/{blobId}/{name}?type={type}',
  uploadUrl: '/jmap/upload/{accountId}',
  eventSourceUrl: '/jmap/eventsource',
  state: 's0',
}));

vi.mock('../jmap/client', () => ({
  jmap: { bootstrap: jmapBootstrap },
  setJmapOnUnauthenticated: vi.fn(),
}));

vi.mock('../api/client', () => ({
  get: vi.fn(),
  setOnUnauthenticated: vi.fn(),
}));

vi.mock('../jmap/errors', () => ({
  UnauthenticatedError: class UnauthenticatedError extends Error {
    readonly status = 401;
    constructor(message = 'Session expired') {
      super(message);
      this.name = 'UnauthenticatedError';
    }
  },
}));

// ── Types ─────────────────────────────────────────────────────────────────

interface AuthModule {
  auth: {
    status: string;
    errorMessage: string | null;
    login(args: { email: string; password: string; totpCode?: string }): Promise<void>;
  };
}

async function loadAuth(): Promise<AuthModule> {
  vi.resetModules();
  return (await import('./auth.svelte')) as unknown as AuthModule;
}

// ── Helpers ───────────────────────────────────────────────────────────────

function badRequestResponse(body?: { message?: string }): Response {
  return new Response(body ? JSON.stringify(body) : '', {
    status: 400,
    headers: { 'Content-Type': 'application/json' },
  });
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('auth.login — HTTP 400 handling (re #53)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('sets a friendly errorMessage on 400 (no body message)', async () => {
    const { auth } = await loadAuth();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(badRequestResponse()));

    await expect(auth.login({ email: '', password: '' })).rejects.toThrow();
    expect(auth.errorMessage).not.toContain('HTTP 400');
    expect(auth.errorMessage).toBeTruthy();
  });

  it('uses the server-provided message when the 400 body has one', async () => {
    const { auth } = await loadAuth();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(badRequestResponse({ message: 'email is required' })),
    );

    await expect(auth.login({ email: '', password: '' })).rejects.toThrow('email is required');
    expect(auth.errorMessage).toBe('email is required');
  });

  it('falls back to a generic friendly string when 400 body has no message', async () => {
    const { auth } = await loadAuth();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(badRequestResponse({})));

    await expect(auth.login({ email: '', password: '' })).rejects.toThrow();
    expect(auth.errorMessage).not.toBeNull();
    expect(auth.errorMessage).not.toContain('HTTP 400');
    // The fallback must be a non-empty human-readable string.
    expect((auth.errorMessage ?? '').length).toBeGreaterThan(5);
  });

  it('throws an Error with the friendly message so callers can display it', async () => {
    const { auth } = await loadAuth();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(badRequestResponse({ message: 'missing password' })),
    );

    let caught: Error | null = null;
    try {
      await auth.login({ email: 'x@x.com', password: '' });
    } catch (e) {
      caught = e as Error;
    }
    expect(caught).not.toBeNull();
    expect(caught!.message).toBe('missing password');
    expect(caught!.message).not.toContain('HTTP 400');
  });
});
