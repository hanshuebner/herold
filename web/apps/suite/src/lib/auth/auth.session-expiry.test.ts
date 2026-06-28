/**
 * Tests for the auth state machine's session-expiry timer (re #77).
 *
 * The server returns `session_idle_deadline` (RFC 3339 UTC) on POST /auth/login
 * and GET /auth/me. The Suite SPA schedules a setTimeout at that deadline so
 * the LoginView appears the moment the idle window closes, without waiting for
 * the user's next network interaction.
 *
 * The session cookie (herold_public_session) is HttpOnly so the SPA cannot
 * inspect it; the server-provided session_idle_deadline is the only in-band
 * signal the client has for proactive timer scheduling (REQ-AUTH-75, REQ-AS-11).
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
let capturedOnUnauthenticated: ((problemType: string | null) => void) | null = null;

vi.mock('../api/client', () => ({
  get: apiGet,
  setOnUnauthenticated: vi.fn((cb: (problemType: string | null) => void) => {
    capturedOnUnauthenticated = cb;
  }),
}));

vi.mock('../jmap/errors', () => ({
  UnauthenticatedError: class UnauthenticatedError extends Error {
    readonly status = 401;
    readonly problemType: string | null;
    constructor(message = 'Session expired', problemType: string | null = null) {
      super(message);
      this.name = 'UnauthenticatedError';
      this.problemType = problemType;
    }
  },
}));

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

interface AuthModule {
  auth: {
    status: 'idle' | 'bootstrapping' | 'ready' | 'unauthenticated' | 'error';
    sessionIdleDeadline: Date | null;
    forcedLoginReason: 'expired' | 'revoked' | null;
    login(args: { email: string; password: string; totpCode?: string }): Promise<void>;
    bootstrap(): Promise<void>;
    logout(): Promise<void>;
    signalUnauthenticated(reason?: 'expired' | 'revoked' | null): void;
  };
}

/**
 * Load auth.svelte fresh so the singleton's internal timers and $state
 * start at a known baseline. The `setOnUnauthenticated` capture resets
 * between imports because vi.resetModules() re-executes the module body.
 */
async function loadAuth(): Promise<AuthModule> {
  capturedOnUnauthenticated = null;
  vi.resetModules();
  return (await import('./auth.svelte')) as unknown as AuthModule;
}

/** Build a successful 200 response for POST /auth/login with session_idle_deadline. */
function loginOkResponse(idleDeadline: string): Response {
  return new Response(
    JSON.stringify({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_idle_deadline: idleDeadline,
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  );
}

/** Build a 200 response with the legacy session_expires_at field (old server builds). */
function loginOkLegacyResponse(expiresAt: string): Response {
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

describe('Auth session idle-deadline timer (re #77)', () => {
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

  it('schedules signalUnauthenticated("expired") at session_idle_deadline after login', async () => {
    const { auth } = await loadAuth();

    // Two seconds in the future so the timer is observably bounded.
    const deadline = new Date('2026-01-01T12:00:02Z');

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkResponse(deadline.toISOString())));
    // bootstrap() re-runs after login(). The /auth/me response carries
    // session_idle_deadline so the timer does not get cleared by loadMe().
    apiGet.mockResolvedValue({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_idle_deadline: deadline.toISOString(),
    });

    await auth.login({ email: 'user@example.com', password: 'pw' });

    expect(auth.status).toBe('ready');
    expect(auth.sessionIdleDeadline?.toISOString()).toBe(deadline.toISOString());

    // Advance to just before the deadline -- still 'ready'.
    vi.advanceTimersByTime(1900);
    expect(auth.status).toBe('ready');

    // Crossing the deadline transitions to 'unauthenticated' with reason 'expired'
    // WITHOUT any further fetch call -- the timer fires proactively (REQ-AS-11).
    vi.advanceTimersByTime(200);
    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBe('expired');
  });

  it('clears the timer on logout so the auth state stays at logout-supplied value', async () => {
    const { auth } = await loadAuth();
    const deadline = new Date('2026-01-01T12:00:05Z');

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkResponse(deadline.toISOString())));
    apiGet.mockResolvedValue({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_idle_deadline: deadline.toISOString(),
    });

    await auth.login({ email: 'user@example.com', password: 'pw' });
    expect(auth.status).toBe('ready');

    // Re-stub fetch for the logout POST so it resolves cleanly.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    await auth.logout();
    expect(auth.status).toBe('unauthenticated');
    expect(auth.sessionIdleDeadline).toBeNull();
    expect(auth.forcedLoginReason).toBeNull();

    // Advance past the original deadline; no second transition should fire
    // (the timer must have been cleared). Status must remain at the value
    // set by logout, not get retriggered.
    vi.advanceTimersByTime(60_000);
    expect(auth.status).toBe('unauthenticated');
  });

  it('captures session_idle_deadline from /auth/me during bootstrap and schedules the timer', async () => {
    const { auth } = await loadAuth();

    const deadline = new Date('2026-01-01T12:00:03Z');
    apiGet.mockResolvedValue({
      principal_id: 7,
      email: 'reload@example.com',
      scopes: ['end-user'],
      session_idle_deadline: deadline.toISOString(),
    });

    await auth.bootstrap();
    expect(auth.status).toBe('ready');
    expect(auth.sessionIdleDeadline?.toISOString()).toBe(deadline.toISOString());

    vi.advanceTimersByTime(3_100);
    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBe('expired');
  });

  it('falls back to session_expires_at when session_idle_deadline is absent (old server)', async () => {
    const { auth } = await loadAuth();

    const expiresAt = new Date('2026-01-01T12:00:04Z');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkLegacyResponse(expiresAt.toISOString())));
    apiGet.mockResolvedValue({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_expires_at: expiresAt.toISOString(),
    });

    await auth.login({ email: 'user@example.com', password: 'pw' });
    expect(auth.status).toBe('ready');
    expect(auth.sessionIdleDeadline?.toISOString()).toBe(expiresAt.toISOString());

    vi.advanceTimersByTime(4_100);
    expect(auth.status).toBe('unauthenticated');
  });

  it('does not schedule a timer when /auth/me omits both deadline fields (pre-fix server)', async () => {
    const { auth } = await loadAuth();

    apiGet.mockResolvedValue({
      principal_id: 7,
      email: 'reload@example.com',
      scopes: ['end-user'],
    });

    await auth.bootstrap();
    expect(auth.status).toBe('ready');
    expect(auth.sessionIdleDeadline).toBeNull();

    vi.advanceTimersByTime(48 * 60 * 60 * 1000);
    expect(auth.status).toBe('ready');
  });

  it('transitions to unauthenticated on the next tick when the deadline is already past at login time', async () => {
    const { auth } = await loadAuth();

    // Deadline is 1 second IN THE PAST relative to current fake time.
    // This represents a server returning a near-future deadline that then
    // slipped into the past by the time the client processes it (or the
    // bootstrap path returning from a page reload with an already-expired
    // session_idle_deadline).
    const deadline = new Date('2026-01-01T11:59:59Z');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkResponse(deadline.toISOString())));
    apiGet.mockResolvedValue({
      principal_id: 42,
      email: 'user@example.com',
      scopes: ['end-user'],
      session_idle_deadline: deadline.toISOString(),
    });

    await auth.login({ email: 'user@example.com', password: 'pw' });

    // After login() + bootstrap() complete, status is 'ready'. The past-
    // deadline timer (delay 0) is still queued in the setTimeout queue.
    // Advancing by 0 ms flushes it; the expiry signal transitions to
    // 'unauthenticated' (REQ-AS-11). We use setTimeout(delay 0) rather
    // than a synchronous call so the bootstrap flow can complete first
    // without the signal being overridden by `status = 'ready'`.
    expect(auth.status).toBe('ready');
    vi.advanceTimersByTime(0);
    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBe('expired');
  });
});

describe('Auth forced-login reason from typed 401 (REQ-AS-10, REQ-AUTH-76, re #77)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T12:00:00Z'));
    jmapBootstrap.mockClear();
    apiGet.mockClear();
    capturedOnUnauthenticated = null;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('sets forcedLoginReason "expired" when the registered callback receives session_expired type', async () => {
    const { auth } = await loadAuth();

    // Bootstrap so we start in 'ready' state.
    apiGet.mockResolvedValue({ principal_id: 1, email: 'u@example.com', scopes: [] });
    await auth.bootstrap();
    expect(auth.status).toBe('ready');

    // Simulate the REST client receiving a typed 401.
    expect(capturedOnUnauthenticated).not.toBeNull();
    capturedOnUnauthenticated!('https://netzhansa.com/problems/session_expired');

    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBe('expired');
  });

  it('sets forcedLoginReason "revoked" when callback receives session_revoked type', async () => {
    const { auth } = await loadAuth();

    apiGet.mockResolvedValue({ principal_id: 1, email: 'u@example.com', scopes: [] });
    await auth.bootstrap();

    capturedOnUnauthenticated!('https://netzhansa.com/problems/session_revoked');

    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBe('revoked');
  });

  it('sets forcedLoginReason null when callback receives an unrecognised type', async () => {
    const { auth } = await loadAuth();

    apiGet.mockResolvedValue({ principal_id: 1, email: 'u@example.com', scopes: [] });
    await auth.bootstrap();

    capturedOnUnauthenticated!('https://netzhansa.com/problems/unauthorized');

    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBeNull();
  });

  it('sets forcedLoginReason null when callback receives a null type (untyped 401)', async () => {
    const { auth } = await loadAuth();

    apiGet.mockResolvedValue({ principal_id: 1, email: 'u@example.com', scopes: [] });
    await auth.bootstrap();

    capturedOnUnauthenticated!(null);

    expect(auth.status).toBe('unauthenticated');
    expect(auth.forcedLoginReason).toBeNull();
  });

  it('clears forcedLoginReason to null after successful re-login', async () => {
    const { auth } = await loadAuth();

    apiGet.mockResolvedValue({ principal_id: 1, email: 'u@example.com', scopes: [] });
    await auth.bootstrap();

    // Simulate an expired-session 401.
    capturedOnUnauthenticated!('https://netzhansa.com/problems/session_expired');
    expect(auth.forcedLoginReason).toBe('expired');

    // Re-login: 200 response, then bootstrap() re-runs.
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(loginOkResponse(
      new Date('2026-01-01T12:00:10Z').toISOString(),
    )));
    apiGet.mockResolvedValue({
      principal_id: 1,
      email: 'u@example.com',
      scopes: [],
      session_idle_deadline: new Date('2026-01-01T12:00:10Z').toISOString(),
    });

    await auth.login({ email: 'u@example.com', password: 'pw' });

    expect(auth.status).toBe('ready');
    expect(auth.forcedLoginReason).toBeNull();
  });
});
