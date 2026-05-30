/**
 * Tests for the auth state machine's session-expiry timer.
 *
 * The server returns `session_expires_at` (RFC 3339 UTC) on POST /auth/login
 * and GET /auth/me. The Suite SPA must schedule a setTimeout that calls
 * signalUnauthenticated() the moment that deadline passes, so the LoginView
 * appears without waiting for the user's next interaction.
 *
 * The session cookie itself is HttpOnly so the SPA cannot inspect it; the
 * server-provided session_expires_at is the only way to know in advance when
 * the cookie will be rejected.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// -----------------------------------------------------------------------
// Mocks for the singletons auth depends on. Declared before the dynamic
// import (the auth.svelte module is loaded fresh in each test so we can
// observe the bootstrap-side timer scheduling without singleton bleed).
// -----------------------------------------------------------------------

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

const apiGet = vi.fn();

vi.mock('../api/client', () => ({
  get: apiGet,
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

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

interface AuthModule {
  auth: {
    status: 'idle' | 'bootstrapping' | 'ready' | 'unauthenticated' | 'error';
    sessionExpiresAt: Date | null;
    login(args: { email: string; password: string; totpCode?: string }): Promise<void>;
    bootstrap(): Promise<void>;
    logout(): Promise<void>;
    signalUnauthenticated(): void;
  };
}

/**
 * Load auth.svelte fresh so the singleton's internal timers and $state
 * start at a known baseline.
 */
async function loadAuth(): Promise<AuthModule> {
  vi.resetModules();
  return (await import('./auth.svelte')) as unknown as AuthModule;
}

/** Build a successful 200 response for POST /auth/login. */
function loginOkResponse(expiresAt: string): Response {
  return new Response(
    JSON.stringify({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_expires_at: expiresAt,
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  );
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

describe('Auth session-expiry timer', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T12:00:00Z'));
    jmapBootstrap.mockClear();
    apiGet.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('schedules a signalUnauthenticated() call at session_expires_at after login', async () => {
    const { auth } = await loadAuth();

    // Two seconds in the future so the timer is observably bounded.
    const expiresAt = new Date('2026-01-01T12:00:02Z');

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkResponse(expiresAt.toISOString())));
    // bootstrap() chain (login() re-runs bootstrap() on 200). The /auth/me
    // response carries session_expires_at too in production; mirror that
    // here so the bootstrap path does not blank the value the login body set.
    apiGet.mockResolvedValue({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_expires_at: expiresAt.toISOString(),
    });

    await auth.login({ email: 'user@example.com', password: 'pw' });

    expect(auth.status).toBe('ready');
    expect(auth.sessionExpiresAt?.toISOString()).toBe(expiresAt.toISOString());

    // Advance to just before the deadline -- still 'ready'.
    vi.advanceTimersByTime(1900);
    expect(auth.status).toBe('ready');

    // Crossing the deadline transitions to 'unauthenticated' WITHOUT any
    // further fetch call -- this is the whole point of the timer.
    vi.advanceTimersByTime(200);
    expect(auth.status).toBe('unauthenticated');
  });

  it('clears the timer on logout so the auth state stays at logout-supplied value', async () => {
    const { auth } = await loadAuth();
    const expiresAt = new Date('2026-01-01T12:00:05Z');

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkResponse(expiresAt.toISOString())));
    apiGet.mockResolvedValue({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_expires_at: expiresAt.toISOString(),
    });

    await auth.login({ email: 'user@example.com', password: 'pw' });
    expect(auth.status).toBe('ready');

    // Re-stub fetch for the logout POST so it resolves cleanly.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    await auth.logout();
    expect(auth.status).toBe('unauthenticated');
    expect(auth.sessionExpiresAt).toBeNull();

    // Advance past the original expiry; no second transition should fire
    // (the timer must have been cleared). Status must remain at the value
    // set by logout, not get retriggered.
    vi.advanceTimersByTime(60_000);
    expect(auth.status).toBe('unauthenticated');
  });

  it('captures session_expires_at from /auth/me during bootstrap and schedules the timer', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:00:03Z');
    apiGet.mockResolvedValue({
      principal_id: 7,
      email: 'reload@example.com',
      scopes: ['end-user'],
      session_expires_at: expiresAt.toISOString(),
    });

    await auth.bootstrap();
    expect(auth.status).toBe('ready');
    expect(auth.sessionExpiresAt?.toISOString()).toBe(expiresAt.toISOString());

    vi.advanceTimersByTime(3_100);
    expect(auth.status).toBe('unauthenticated');
  });

  it('does not schedule a timer when /auth/me omits session_expires_at (pre-fix server)', async () => {
    const { auth } = await loadAuth();

    apiGet.mockResolvedValue({
      principal_id: 7,
      email: 'reload@example.com',
      scopes: ['end-user'],
    });

    await auth.bootstrap();
    expect(auth.status).toBe('ready');
    expect(auth.sessionExpiresAt).toBeNull();

    vi.advanceTimersByTime(48 * 60 * 60 * 1000);
    expect(auth.status).toBe('ready');
  });
});
