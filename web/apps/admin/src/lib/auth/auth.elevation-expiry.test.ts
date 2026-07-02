/**
 * Tests for the admin auth state machine's elevation-expiry timer (re #106).
 *
 * The admin SPA stores elevationExpiresAt from the bootstrap response and
 * schedules a setTimeout to transition proactively from 'ready' to
 * 'step_up_pending' when the TOTP elevation window closes, without waiting
 * for the next API call to return 403 step_up_required.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Mock the router before dynamic imports so auth.svelte.ts does not try to
// access window.location.hash or history.replaceState in happy-dom.
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
    updateElevation(raw: string | null): void;
    checkElevationExpiry(): void;
    handleUnauthorized(): void;
  };
}

/**
 * Load auth.svelte fresh so the singleton's internal timers and $state
 * start at a known baseline. vi.resetModules() re-executes the module body,
 * giving each test an independent Auth instance.
 */
async function loadAuth(): Promise<AuthModule> {
  vi.resetModules();
  return (await import('./auth.svelte')) as unknown as AuthModule;
}

/**
 * Build a fetch stub that handles both bootstrap calls:
 *   GET /api/v1/auth/me -> 200, admin role, given elevation_expires_at
 *   GET /api/v1/server/status -> 200
 */
function makeBootstrapFetch(elevationExpiresAt: string) {
  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/auth/me')) {
      return Promise.resolve(
        new Response(
          JSON.stringify({
            principal_id: '1',
            email: 'admin@example.com',
            scopes: ['admin'],
            roles: ['admin'],
            elevation_expires_at: elevationExpiresAt,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
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

describe('Admin auth elevation-expiry timer (re #106)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T12:00:00Z'));
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('transitions to step_up_pending when the elevation timer fires', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:00:02Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(expiresAt.toISOString()));

    await auth.bootstrap();
    expect(auth.status).toBe('ready');
    expect(auth.elevationExpiresAt?.toISOString()).toBe(expiresAt.toISOString());

    // Before expiry: still in 'ready'.
    vi.advanceTimersByTime(1900);
    expect(auth.status).toBe('ready');

    // Crossing the expiry: proactively transitions to 'step_up_pending'
    // WITHOUT any API call (re #106).
    vi.advanceTimersByTime(200);
    expect(auth.status).toBe('step_up_pending');
    expect(auth.elevationExpiresAt).toBeNull();
  });

  it('clears the timer on logout so status stays at the logout-supplied value', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:00:05Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(expiresAt.toISOString()));

    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Re-stub fetch for the logout POST.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    await auth.logout();
    expect(auth.status).toBe('unauthenticated');
    expect(auth.elevationExpiresAt).toBeNull();

    // Advance past the original expiry; the timer must have been cancelled.
    vi.advanceTimersByTime(60_000);
    expect(auth.status).toBe('unauthenticated');
  });

  it('re-arms the timer after updateElevation() with a fresh step-up expiry', async () => {
    const { auth } = await loadAuth();

    // Bootstrap with elevation that expires in 2 s.
    const firstExpiry = new Date('2026-01-01T12:00:02Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(firstExpiry.toISOString()));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // 1 s passes, then step-up refreshes the elevation with a new expiry 3 s
    // from the current fake time (i.e. at t=4 s absolute).
    vi.advanceTimersByTime(1000);
    const newExpiry = new Date('2026-01-01T12:00:04Z');
    auth.updateElevation(newExpiry.toISOString());
    expect(auth.elevationExpiresAt?.toISOString()).toBe(newExpiry.toISOString());

    // The original timer (would have fired at t=2 s) must be cancelled.
    vi.advanceTimersByTime(1100); // t=2.1 s — still ready
    expect(auth.status).toBe('ready');

    // New timer fires at t=4 s.
    vi.advanceTimersByTime(1900); // t=4 s
    expect(auth.status).toBe('step_up_pending');
  });

  it('fires on the next tick when updateElevation() is called with an expired timestamp', async () => {
    const { auth } = await loadAuth();

    // Bootstrap with a future elevation so we reach 'ready'.
    const futureExpiry = new Date('2026-01-01T12:05:00Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(futureExpiry.toISOString()));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Call updateElevation() with a timestamp already in the past. This
    // represents a step-up response with an expired elevation_expires_at —
    // an unlikely but defensive scenario. The timer is scheduled with
    // delay=0 so it fires after the current call stack unwinds.
    const pastExpiry = new Date('2026-01-01T11:59:00Z');
    auth.updateElevation(pastExpiry.toISOString());

    // Status is still 'ready' synchronously — the timer hasn't fired yet.
    expect(auth.status).toBe('ready');

    // Flushing the timer queue transitions to 'step_up_pending' (re #106).
    vi.advanceTimersByTime(0);
    expect(auth.status).toBe('step_up_pending');
  });

  it('checkElevationExpiry() transitions to step_up_pending when elevation has lapsed', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:00:01Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(expiresAt.toISOString()));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Move the clock past expiry to simulate a tab returning from background.
    vi.setSystemTime(new Date('2026-01-01T12:00:02Z'));
    auth.checkElevationExpiry();

    expect(auth.status).toBe('step_up_pending');
    expect(auth.elevationExpiresAt).toBeNull();
  });

  it('checkElevationExpiry() is a no-op when elevation is still active', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:05:00Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(expiresAt.toISOString()));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    auth.checkElevationExpiry();
    expect(auth.status).toBe('ready');
  });

  it('handleUnauthorized() cancels the timer so no spurious step_up_pending fires', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:00:05Z');
    vi.stubGlobal('fetch', makeBootstrapFetch(expiresAt.toISOString()));
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    auth.handleUnauthorized();
    expect(auth.status).toBe('unauthenticated');

    vi.advanceTimersByTime(60_000);
    expect(auth.status).toBe('unauthenticated');
  });
});
