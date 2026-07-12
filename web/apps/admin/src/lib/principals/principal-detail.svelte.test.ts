/**
 * Tests for the principal-detail state class's field mapping against
 * principalDTO (re #218).
 *
 * GET /api/v1/principals/:id returns internal/protoadmin/types.go's
 * principalDTO directly (no envelope): `id` is a JSON number, the email
 * field is `canonical_email`, and `flags` is an array of flag-name
 * strings, not a bitmask. PATCH /api/v1/principals/:id expects the same
 * shape on the way in -- patchPrincipalRequest.Flags is `*[]string`
 * (internal/protoadmin/principals.go) -- so a PATCH body carrying a
 * numeric `flags` either fails to decode or silently drops every flag
 * the caller did not resend.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function makeFetch(principal: unknown) {
  return vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    const u = url as string;
    if (u.includes('/api-keys')) return Promise.resolve(jsonResponse([]));
    if (u.includes('/oidc-links')) return Promise.resolve(jsonResponse([]));
    if (u.includes('/api/v1/principals/') && (!init || init.method === undefined || init.method === 'GET')) {
      return Promise.resolve(jsonResponse(principal));
    }
    if (u.includes('/api/v1/principals/') && init?.method === 'PATCH') {
      return Promise.resolve(jsonResponse(principal));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

interface PrincipalDetailModule {
  principalDetail: {
    status: string;
    principal: {
      id: number;
      canonical_email: string;
      display_name: string;
      flags: string[];
      quota_bytes: number;
      created_at: string;
    } | null;
    totpEnabled: boolean;
    load(id: string): Promise<void>;
    updateProfile(
      id: string,
      patch: { display_name?: string; quota_bytes?: number; flags?: string[] },
    ): Promise<{ ok: boolean; errorMessage: string | null }>;
  };
}

async function loadModule(): Promise<PrincipalDetailModule> {
  vi.resetModules();
  return (await import('./principal-detail.svelte')) as unknown as PrincipalDetailModule;
}

describe('Principal detail field mapping against principalDTO (re #218)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('maps canonical_email, numeric id, and string-array flags from a representative principalDTO response', async () => {
    const { principalDetail } = await loadModule();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        id: 1,
        kind: 'user',
        canonical_email: 'admin@example.local',
        display_name: '',
        quota_bytes: 0,
        flags: ['admin', 'super_admin', 'totp_enabled'],
        totp_enabled: true,
        seen_addresses_enabled: false,
        created_at: '2026-07-12T18:39:05.400041Z',
        updated_at: '2026-07-12T18:39:05.425518Z',
      }),
    );

    await principalDetail.load('1');

    expect(principalDetail.status).toBe('ready');
    expect(principalDetail.principal).not.toBeNull();
    expect(principalDetail.principal?.id).toBe(1);
    expect(typeof principalDetail.principal?.id).toBe('number');
    expect(principalDetail.principal?.canonical_email).toBe('admin@example.local');
    expect(principalDetail.principal?.flags).toEqual(['admin', 'super_admin', 'totp_enabled']);
    // totpEnabled is derived from string-array membership, not a bitmask AND.
    expect(principalDetail.totpEnabled).toBe(true);
  });

  it('derives totpEnabled as false when "totp_enabled" is absent from flags', async () => {
    const { principalDetail } = await loadModule();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        id: 2,
        canonical_email: 'alice@example.local',
        display_name: '',
        quota_bytes: 0,
        flags: [],
        totp_enabled: false,
        created_at: '2026-01-01T00:00:00Z',
      }),
    );

    await principalDetail.load('2');

    expect(principalDetail.principal?.flags).toEqual([]);
    expect(principalDetail.totpEnabled).toBe(false);
  });

  it('sends the PATCH flags field as a string array, not a number', async () => {
    const { principalDetail } = await loadModule();
    const fetchMock = makeFetch({
      id: 1,
      canonical_email: 'admin@example.local',
      display_name: 'Admin',
      quota_bytes: 0,
      flags: ['admin', 'totp_enabled', 'ignore_download_limits'],
      created_at: '2026-01-01T00:00:00Z',
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await principalDetail.updateProfile('1', {
      display_name: 'Admin',
      flags: ['admin', 'totp_enabled', 'ignore_download_limits'],
    });

    expect(result.ok).toBe(true);

    const patchCall = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patchCall).toBeDefined();
    const body = JSON.parse((patchCall?.[1] as RequestInit).body as string) as {
      flags?: unknown;
    };
    expect(Array.isArray(body.flags)).toBe(true);
    expect(body.flags).toEqual(['admin', 'totp_enabled', 'ignore_download_limits']);
  });
});
