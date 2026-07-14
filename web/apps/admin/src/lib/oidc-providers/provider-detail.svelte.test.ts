/**
 * Tests for the OIDC provider detail state class (re #239).
 *
 * Covers the three composed resources under a provider id (provider row,
 * claim allowlist, claim-mapping rules) and, critically, that every
 * mutation method (setAuthzTrusted, addAllowlistClaim, deleteAllowlistClaim,
 * createRule, deleteRule) re-fetches its resource from the server afterwards
 * rather than trusting the mutation response or writing client-only state.
 * Each mutation test below makes the mutating response (PUT/POST) carry a
 * DELIBERATELY STALE value and the follow-up GET carry the true value, then
 * asserts the state ends up holding the GET's value -- this fails if a
 * future change short-circuits the re-fetch and trusts the mutation
 * response instead.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function providerDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 'fakeoidc',
    name: 'fakeoidc',
    issuer: 'https://issuer.example.test',
    client_id: 'fakeoidc-client',
    scopes: ['openid', 'email', 'profile'],
    auto_provision: false,
    authz_trusted: false,
    created_at: '2026-07-14T04:39:38.793884Z',
    ...overrides,
  };
}

function ruleDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    provider: 'fakeoidc',
    claim: 'groups',
    match_value: 'mail-admins',
    resource_kind: 'domain',
    resource_id: 'example.local',
    level: 'operator',
    created_by: 1,
    orphaned: false,
    author_authority_valid: true,
    created_at: '2026-07-14T04:41:57.025963Z',
    ...overrides,
  };
}

interface ProviderDetailModule {
  providerDetail: {
    status: string;
    provider: Record<string, unknown> | null;
    allowlist: Array<{ claim: string }>;
    rules: Array<Record<string, unknown>>;
    errorMessage: string | null;
    load(id: string): Promise<void>;
    setAuthzTrusted(id: string, trusted: boolean): Promise<{ ok: boolean; errorMessage: string | null }>;
    addAllowlistClaim(id: string, claim: string): Promise<{ ok: boolean; errorMessage: string | null }>;
    deleteAllowlistClaim(id: string, claim: string): Promise<{ ok: boolean; errorMessage: string | null }>;
    createRule(id: string, payload: Record<string, unknown>): Promise<{ ok: boolean; errorMessage: string | null }>;
    deleteRule(id: string, ruleId: number): Promise<{ ok: boolean; errorMessage: string | null }>;
  };
}

async function loadModule(): Promise<ProviderDetailModule> {
  vi.resetModules();
  return (await import('./provider-detail.svelte')) as unknown as ProviderDetailModule;
}

describe('providerDetail state (re #239)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('load() populates provider, allowlist, and rules from their three GETs', async () => {
    const { providerDetail } = await loadModule();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        const u = url as string;
        if (u === '/api/v1/oidc/providers/fakeoidc') {
          return Promise.resolve(jsonResponse(providerDTO()));
        }
        if (u.endsWith('/claim-allowlist')) {
          return Promise.resolve(jsonResponse({ items: [{ claim: 'groups' }], next: null }));
        }
        if (u.endsWith('/claim-mapping-rules')) {
          return Promise.resolve(jsonResponse({ items: [ruleDTO()], next: null }));
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    await providerDetail.load('fakeoidc');

    expect(providerDetail.status).toBe('ready');
    expect(providerDetail.provider?.id).toBe('fakeoidc');
    expect(providerDetail.allowlist).toEqual([{ claim: 'groups' }]);
    expect(providerDetail.rules).toHaveLength(1);
    expect(providerDetail.rules.at(0)?.claim).toBe('groups');
  });

  it('load() sets status=error when the provider GET fails', async () => {
    const { providerDetail } = await loadModule();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if ((url as string) === '/api/v1/oidc/providers/fakeoidc') {
          return Promise.resolve(
            jsonResponse({ type: 'about:blank', title: 'not found', status: 404 }, 404),
          );
        }
        return Promise.resolve(jsonResponse({ items: [], next: null }));
      }),
    );

    await providerDetail.load('fakeoidc');

    expect(providerDetail.status).toBe('error');
    expect(providerDetail.provider).toBeNull();
  });

  it('setAuthzTrusted PUTs the new value and applies the RE-FETCHED provider, not the PUT response', async () => {
    const { providerDetail } = await loadModule();
    let putBody: Record<string, unknown> | null = null;
    let getCount = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        const u = url as string;
        if (u === '/api/v1/oidc/providers/fakeoidc/authz-trusted' && init?.method === 'PUT') {
          putBody = JSON.parse(init.body as string) as Record<string, unknown>;
          // Deliberately stale: the PUT response still says untrusted. If the
          // component trusted this response instead of re-fetching, the
          // assertion below on providerDetail.provider.authz_trusted would fail.
          return Promise.resolve(jsonResponse(providerDTO({ authz_trusted: false })));
        }
        if (u === '/api/v1/oidc/providers/fakeoidc') {
          getCount += 1;
          return Promise.resolve(jsonResponse(providerDTO({ authz_trusted: true })));
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    const result = await providerDetail.setAuthzTrusted('fakeoidc', true);

    expect(result.ok).toBe(true);
    expect(putBody).toEqual({ authz_trusted: true });
    expect(getCount).toBe(1);
    expect(providerDetail.provider?.authz_trusted).toBe(true);
  });

  it('addAllowlistClaim POSTs the claim and reloads the allowlist from a follow-up GET', async () => {
    const { providerDetail } = await loadModule();
    let postBody: Record<string, unknown> | null = null;
    let allowlistGetCount = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        const u = url as string;
        if (u === '/api/v1/oidc/providers/fakeoidc/claim-allowlist' && init?.method === 'POST') {
          postBody = JSON.parse(init.body as string) as Record<string, unknown>;
          return Promise.resolve(jsonResponse({ claim: 'groups' }, 201));
        }
        if (u === '/api/v1/oidc/providers/fakeoidc/claim-allowlist') {
          allowlistGetCount += 1;
          return Promise.resolve(jsonResponse({ items: [{ claim: 'groups' }], next: null }));
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    const result = await providerDetail.addAllowlistClaim('fakeoidc', 'groups');

    expect(result.ok).toBe(true);
    expect(postBody).toEqual({ claim: 'groups' });
    expect(allowlistGetCount).toBe(1);
    expect(providerDetail.allowlist).toEqual([{ claim: 'groups' }]);
  });

  it('deleteAllowlistClaim DELETEs the claim and reloads the allowlist afterwards', async () => {
    const { providerDetail } = await loadModule();
    let deletedPath: string | null = null;
    let allowlistGetCount = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        const u = url as string;
        if (init?.method === 'DELETE') {
          deletedPath = u;
          return Promise.resolve(new Response(null, { status: 204 }));
        }
        if (u === '/api/v1/oidc/providers/fakeoidc/claim-allowlist') {
          allowlistGetCount += 1;
          // Reflects the post-delete server state: the claim is now gone.
          return Promise.resolve(jsonResponse({ items: [], next: null }));
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    const result = await providerDetail.deleteAllowlistClaim('fakeoidc', 'groups');

    expect(result.ok).toBe(true);
    expect(deletedPath).toBe('/api/v1/oidc/providers/fakeoidc/claim-allowlist/groups');
    expect(allowlistGetCount).toBe(1);
    expect(providerDetail.allowlist).toEqual([]);
  });

  it('createRule POSTs the payload and reloads rules from a follow-up GET, not the POST response', async () => {
    const { providerDetail } = await loadModule();
    let postBody: Record<string, unknown> | null = null;
    let rulesGetCount = 0;
    const payload = {
      claim: 'groups',
      match_value: 'mail-admins',
      resource_kind: 'domain',
      resource_id: 'example.local',
      level: 'operator',
    };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        const u = url as string;
        if (u === '/api/v1/oidc/providers/fakeoidc/claim-mapping-rules' && init?.method === 'POST') {
          postBody = JSON.parse(init.body as string) as Record<string, unknown>;
          // Deliberately different id/match_value than the GET below, so the
          // final state can only match if it came from the re-fetch.
          return Promise.resolve(jsonResponse(ruleDTO({ id: 999, match_value: 'stale-from-post' }), 201));
        }
        if (u === '/api/v1/oidc/providers/fakeoidc/claim-mapping-rules') {
          rulesGetCount += 1;
          return Promise.resolve(jsonResponse({ items: [ruleDTO({ id: 1, match_value: 'mail-admins' })], next: null }));
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    const result = await providerDetail.createRule('fakeoidc', payload);

    expect(result.ok).toBe(true);
    expect(postBody).toEqual(payload);
    expect(rulesGetCount).toBe(1);
    expect(providerDetail.rules).toHaveLength(1);
    expect(providerDetail.rules.at(0)?.id).toBe(1);
    expect(providerDetail.rules.at(0)?.match_value).toBe('mail-admins');
  });

  it('deleteRule DELETEs the rule id and reloads rules afterwards', async () => {
    const { providerDetail } = await loadModule();
    let deletedPath: string | null = null;
    let rulesGetCount = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        const u = url as string;
        if (init?.method === 'DELETE') {
          deletedPath = u;
          return Promise.resolve(new Response(null, { status: 204 }));
        }
        if (u === '/api/v1/oidc/providers/fakeoidc/claim-mapping-rules') {
          rulesGetCount += 1;
          return Promise.resolve(jsonResponse({ items: [], next: null }));
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    const result = await providerDetail.deleteRule('fakeoidc', 1);

    expect(result.ok).toBe(true);
    expect(deletedPath).toBe('/api/v1/oidc/providers/fakeoidc/claim-mapping-rules/1');
    expect(rulesGetCount).toBe(1);
    expect(providerDetail.rules).toEqual([]);
  });

  it('surfaces the server error message and leaves prior state untouched on a failed mutation', async () => {
    const { providerDetail } = await loadModule();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        const u = url as string;
        if (u === '/api/v1/oidc/providers/fakeoidc/authz-trusted' && init?.method === 'PUT') {
          return Promise.resolve(
            jsonResponse({ type: 'about:blank', title: 'forbidden', status: 403, detail: 'superadmin required' }, 403),
          );
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    const result = await providerDetail.setAuthzTrusted('fakeoidc', true);

    expect(result.ok).toBe(false);
    expect(result.errorMessage).toContain('forbidden');
    expect(providerDetail.provider).toBeNull();
  });
});
