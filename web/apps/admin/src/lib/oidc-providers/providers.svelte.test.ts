/**
 * Tests for the OIDC providers list state class (re #239).
 *
 * GET /api/v1/oidc/providers wraps its payload in the {items, next} pageDTO
 * envelope (internal/protoadmin/oidc.go's handleListOIDCProviders). Each
 * item mirrors internal/protoadmin/types.go's oidcProviderDTO field-for-
 * field, including the epic #188 authz_trusted flag.
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

function makeFetch(body: unknown, status = 200) {
  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/oidc/providers')) {
      return Promise.resolve(jsonResponse(body, status));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

interface OIDCProvidersModule {
  oidcProviders: {
    status: string;
    items: Array<Record<string, unknown>>;
    errorMessage: string | null;
    load(): Promise<void>;
    refresh(): Promise<void>;
  };
}

async function loadModule(): Promise<OIDCProvidersModule> {
  vi.resetModules();
  return (await import('./providers.svelte')) as unknown as OIDCProvidersModule;
}

describe('oidcProviders list state (re #239)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {items,next} envelope and maps every oidcProviderDTO field', async () => {
    const { oidcProviders } = await loadModule();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [
          providerDTO(),
          providerDTO({ id: 'trusted-idp', name: 'trusted-idp', authz_trusted: true, auto_provision: true, auto_provision_domain: 'example.local' }),
        ],
        next: null,
      }),
    );

    await oidcProviders.load();

    expect(oidcProviders.errorMessage).toBeNull();
    expect(oidcProviders.status).toBe('ready');
    expect(oidcProviders.items).toHaveLength(2);
    const first = oidcProviders.items.at(0);
    expect(first?.id).toBe('fakeoidc');
    expect(first?.issuer).toBe('https://issuer.example.test');
    expect(first?.client_id).toBe('fakeoidc-client');
    expect(first?.scopes).toEqual(['openid', 'email', 'profile']);
    expect(first?.authz_trusted).toBe(false);
    const second = oidcProviders.items.at(1);
    expect(second?.authz_trusted).toBe(true);
    expect(second?.auto_provision_domain).toBe('example.local');
  });

  it('reports zero providers, not an undefined-length envelope, when items is empty', async () => {
    const { oidcProviders } = await loadModule();
    vi.stubGlobal('fetch', makeFetch({ items: [], next: null }));

    await oidcProviders.load();

    expect(oidcProviders.errorMessage).toBeNull();
    expect(oidcProviders.items).toEqual([]);
    expect(oidcProviders.status).toBe('ready');
  });

  it('sets status=error and surfaces the server error message on a failed load', async () => {
    const { oidcProviders } = await loadModule();
    vi.stubGlobal(
      'fetch',
      makeFetch({ type: 'about:blank', title: 'forbidden', status: 403, detail: 'admin privileges required' }, 403),
    );

    await oidcProviders.load();

    expect(oidcProviders.status).toBe('error');
    expect(oidcProviders.errorMessage).toContain('forbidden');
  });

  it('refresh() reloads the list', async () => {
    const { oidcProviders } = await loadModule();
    vi.stubGlobal('fetch', makeFetch({ items: [providerDTO()], next: null }));
    await oidcProviders.load();
    expect(oidcProviders.items).toHaveLength(1);

    vi.stubGlobal(
      'fetch',
      makeFetch({ items: [providerDTO(), providerDTO({ id: 'second', name: 'second' })], next: null }),
    );
    await oidcProviders.refresh();

    expect(oidcProviders.items).toHaveLength(2);
  });
});
