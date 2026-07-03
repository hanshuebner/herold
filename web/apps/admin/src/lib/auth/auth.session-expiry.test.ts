/**
 * Tests for the admin auth state machine's session-expiry timer (re #106).
 *
 * The admin SPA reads session_idle_deadline (or session_expires_at as a
 * fallback) from the GET /api/v1/auth/me response and schedules a setTimeout
 * that calls handleUnauthorized() when the deadline passes, transitioning
 * proactively from 'ready'/'step_up_pending' to 'unauthenticated' and routing
 * to /login without waiting for the next user-initiated API call.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

vi.mock('../router/router.svelte', () => ({
  router: {
    current: '/dashboard',
    replace: vi.fn(),
    navigate: vi.fn(),
  },
}));

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

interface AuthModule {
  auth: {
    status: 'idle' | 'bootstrapping' | 'unauthenticated' | 'step_up_pending' | 'ready';
    elevationExpiresAt: Date | null;
    bootstrap(): Promise<void>;
    logout(): Promise<void>;
    handleUnauthorized(): void;
    checkSessionExpiry(): void;
  };
}

async function loadAuth(): Promise<AuthModule> {
  vi.resetModules();
  return (await import('./auth.svelte')) as unknown as AuthModule;
}

interface BootstrapOptions {
  elevationExpiresAt?: string;
  sessionIdleDeadline?: string;
  sessionExpiresAt?: string;
}

/**
 * Build a fetch stub that serves a minimal admin bootstrap:
 *   GET /api/v1/auth/me -> 200, admin role, optional timing fields
 *   GET /api/v1/server/status -> 200
 */
function makeBootstrapFetch(opts: BootstrapOptions = {}) {
  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/auth/me')) {
      const body: Record<string, unknown> = {
        principal_id: '1',
        email: 'admin@example.com',
        scopes: ['admin'],
        roles: ['admin'],
      };
      if (opts.elevationExpiresAt !== undefined)
        body['elevation_expires_at'] = opts.elevationExpiresAt;
      if (opts.sessionIdleDeadline !== undefined)
        body['session_idle_deadline'] = opts.sessionIdleDeadline;
      if (opts.sessionExpiresAt !== undefined)
        body['session_expires_at'] = opts.sessionExpiresAt;
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    if ((url as string).includes('/api/v1/server/status')) {
      return Promise.resolve(
        new Response(
          JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      );
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

describe('Admin auth session-expiry timer (re #106)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T12:00:00Z'));
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('transitions to unauthenticated when the session idle deadline timer fires', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionIdleDeadline = new Date('2026-01-01T12:00:03Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Before deadline: still ready.
    vi.advanceTimersByTime(2900);
    expect(auth.status).toBe('ready');

    // Crossing the session idle deadline transitions to unauthenticated
    // WITHOUT any user interaction (re #106).
    vi.advanceTimersByTime(200);
    expect(auth.status).toBe('unauthenticated');
  });

  it('routes to /login when the session timer fires', async () => {
    const { auth } = await loadAuth();
    const { router } = await import('../router/router.svelte');

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionIdleDeadline = new Date('2026-01-01T12:00:02Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    vi.advanceTimersByTime(3000);

    expect(auth.status).toBe('unauthenticated');
    expect(router.replace).toHaveBeenCalledWith('/login');
  });

  it('falls back to session_expires_at when session_idle_deadline is absent', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionExpiresAt = new Date('2026-01-01T12:00:02Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionExpiresAt }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    vi.advanceTimersByTime(1900);
    expect(auth.status).toBe('ready');

    vi.advanceTimersByTime(200);
    expect(auth.status).toBe('unauthenticated');
  });

  it('clears the session timer on logout so no spurious transition fires', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionIdleDeadline = new Date('2026-01-01T12:00:05Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Stub fetch for the logout POST.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    await auth.logout();
    expect(auth.status).toBe('unauthenticated');

    // Advance past the original session deadline; timer must be cancelled.
    vi.advanceTimersByTime(60_000);
    expect(auth.status).toBe('unauthenticated');
  });

  it('clears the session timer on handleUnauthorized so no double-transition fires', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionIdleDeadline = new Date('2026-01-01T12:00:05Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    auth.handleUnauthorized();
    expect(auth.status).toBe('unauthenticated');

    // Advance past the original session deadline; timer must be cancelled.
    vi.advanceTimersByTime(60_000);
    expect(auth.status).toBe('unauthenticated');
  });

  it('checkSessionExpiry() transitions to unauthenticated when deadline has lapsed', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionIdleDeadline = new Date('2026-01-01T12:00:02Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Simulate a tab returning from background past the idle deadline without
    // the JS timer having fired (browsers throttle timers in background tabs).
    vi.setSystemTime(new Date('2026-01-01T12:00:03Z'));
    auth.checkSessionExpiry();

    expect(auth.status).toBe('unauthenticated');
  });

  it('checkSessionExpiry() is a no-op when deadline is still in the future', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    const sessionIdleDeadline = new Date('2026-01-01T12:05:00Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    auth.checkSessionExpiry();
    expect(auth.status).toBe('ready');
  });

  it('fires on the next tick when bootstrap delivers an already-expired session deadline', async () => {
    const { auth } = await loadAuth();

    const elevationExpiresAt = new Date('2026-01-01T12:10:00Z').toISOString();
    // Deadline 1 minute in the past.
    const sessionIdleDeadline = new Date('2026-01-01T11:59:00Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ elevationExpiresAt, sessionIdleDeadline }));
    await auth.bootstrap();

    // Status is ready synchronously just after bootstrap.
    expect(auth.status).toBe('ready');

    // The past-deadline timer uses delay=0; flushing fires the transition.
    vi.advanceTimersByTime(0);
    expect(auth.status).toBe('unauthenticated');
  });

  it('fires while in step_up_pending so confidential content stays blanked', async () => {
    const { auth } = await loadAuth();

    // Bootstrap with NO elevation so we enter step_up_pending, but with a
    // session deadline 2 s away. The session timer must still fire.
    const sessionIdleDeadline = new Date('2026-01-01T12:00:02Z').toISOString();

    vi.stubGlobal('fetch', makeBootstrapFetch({ sessionIdleDeadline }));
    await auth.bootstrap();
    expect(auth.status).toBe('step_up_pending');

    vi.advanceTimersByTime(1900);
    expect(auth.status).toBe('step_up_pending');

    vi.advanceTimersByTime(200);
    expect(auth.status).toBe('unauthenticated');
  });
});
