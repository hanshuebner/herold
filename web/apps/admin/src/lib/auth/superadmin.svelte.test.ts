/**
 * Tests for superAdminState.check() (re #239).
 *
 * /api/v1/auth/me and /api/v1/server/status do not yet carry a "superadmin"
 * role string (internal/protoadmin/session_auth.go's principalRoles() doc
 * comment: "superadmin requires a future schema addition"), so
 * superadmin.svelte.ts probes GET /api/v1/principals/{self} for the
 * super_admin flag instead. These tests pin:
 *   - a principal carrying super_admin resolves to true
 *   - a plain admin (no super_admin flag) resolves to false
 *   - the result is cached per signed-in principal id (no re-fetch for the
 *     same principal)
 *   - a different principal id (a sign-out/sign-in cycle within the same
 *     tab) re-probes rather than serving the previous principal's answer
 *   - no principal signed in -> false without calling fetch
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

// Mock the router before dynamic imports: auth.svelte.ts imports it at
// module scope and touches window.location.hash / history.replaceState on
// construction, which router.svelte.ts's own module-level singleton does
// for real in happy-dom. Stubbing keeps this test focused on auth.principal.
vi.mock('../router/router.svelte', () => ({
  router: {
    current: '/dashboard',
    replace: vi.fn(),
    navigate: vi.fn(),
  },
}));

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface AuthPrincipal {
  id: string;
  email: string;
  scopes: string[];
}

interface AuthModule {
  auth: {
    principal: AuthPrincipal | null;
  };
}

interface SuperAdminModule {
  superAdminState: {
    isSuperAdmin: boolean;
    check(): Promise<boolean>;
  };
}

/**
 * Load auth.svelte and superadmin.svelte fresh in the same reset epoch so
 * superadmin.svelte's `import { auth } from './auth.svelte'` resolves to the
 * same singleton instance the test manipulates directly.
 */
async function loadModules(): Promise<{
  auth: AuthModule['auth'];
  superAdminState: SuperAdminModule['superAdminState'];
}> {
  vi.resetModules();
  const authMod = (await import('./auth.svelte')) as unknown as AuthModule;
  const superAdminMod = (await import('./superadmin.svelte')) as unknown as SuperAdminModule;
  return { auth: authMod.auth, superAdminState: superAdminMod.superAdminState };
}

describe('superAdminState.check (re #239)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('resolves true for a principal carrying the super_admin flag', async () => {
    const { auth, superAdminState } = await loadModules();
    auth.principal = { id: '1', email: 'admin@example.local', scopes: [] };
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      expect(url).toBe('/api/v1/principals/1');
      return Promise.resolve(jsonResponse({ flags: ['admin', 'super_admin', 'totp_enabled'] }));
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await superAdminState.check();

    expect(result).toBe(true);
    expect(superAdminState.isSuperAdmin).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('resolves false for a plain admin without the super_admin flag', async () => {
    const { auth, superAdminState } = await loadModules();
    auth.principal = { id: '5', email: 'operator@example.local', scopes: [] };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ flags: ['admin'] })));

    const result = await superAdminState.check();

    expect(result).toBe(false);
    expect(superAdminState.isSuperAdmin).toBe(false);
  });

  it('caches the result for the same principal id and does not refetch', async () => {
    const { auth, superAdminState } = await loadModules();
    auth.principal = { id: '1', email: 'admin@example.local', scopes: [] };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ flags: ['admin', 'super_admin'] }));
    vi.stubGlobal('fetch', fetchMock);

    await superAdminState.check();
    await superAdminState.check();
    await superAdminState.check();

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('re-probes when a different principal signs in within the same tab', async () => {
    const { auth, superAdminState } = await loadModules();
    auth.principal = { id: '1', email: 'admin@example.local', scopes: [] };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ flags: ['admin', 'super_admin'] })));
    expect(await superAdminState.check()).toBe(true);

    // A sign-out/sign-in cycle within the same tab swaps auth.principal
    // without a page reload; the cached answer must not leak across it.
    auth.principal = { id: '5', email: 'operator@example.local', scopes: [] };
    const secondFetch = vi.fn().mockResolvedValue(jsonResponse({ flags: ['admin'] }));
    vi.stubGlobal('fetch', secondFetch);

    const result = await superAdminState.check();

    expect(result).toBe(false);
    expect(superAdminState.isSuperAdmin).toBe(false);
    expect(secondFetch).toHaveBeenCalledWith('/api/v1/principals/5', expect.objectContaining({ method: 'GET' }));
  });

  it('returns false without calling fetch when no principal is signed in', async () => {
    const { auth, superAdminState } = await loadModules();
    auth.principal = null;
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    const result = await superAdminState.check();

    expect(result).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
