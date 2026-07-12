/**
 * Tests for the dashboard state class's domain-list envelope unwrapping
 * (re #215).
 *
 * GET /api/v1/domains returns the paginated envelope {items:[...], next}
 * (internal/protoadmin/domains.go handleListDomains), the same shape the
 * dedicated Domains view unwraps via result.data.items. The dashboard
 * loader must unwrap it the same way rather than assigning the envelope
 * straight to the array-typed `domains` state.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function makeFetch(domainsBody: unknown) {
  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/domains')) {
      return Promise.resolve(jsonResponse(domainsBody));
    }
    if ((url as string).includes('/api/v1/queue/stats')) {
      return Promise.resolve(jsonResponse({ queued: 0, deferred: 0, delivered: 0, failed: 0, held: 0 }));
    }
    if ((url as string).includes('/api/v1/audit')) {
      return Promise.resolve(jsonResponse({ items: [], next: null }));
    }
    if ((url as string).includes('/api/v1/admin/clientlog/stats')) {
      return Promise.resolve(jsonResponse({}));
    }
    if ((url as string).includes('/api/v1/server/status')) {
      return Promise.resolve(jsonResponse({}));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

async function loadDashboard() {
  vi.resetModules();
  return (await import('./dashboard.svelte')) as unknown as {
    dashboard: {
      domains: { name: string; created_at: string }[];
      domainsError: string | null;
      load(): Promise<void>;
    };
  };
}

describe('Dashboard domains card (re #215)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {items,next} envelope returned by GET /api/v1/domains', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch({ items: [{ name: 'netzhansa.com', created_at: '2026-01-01T00:00:00Z' }], next: null }),
    );

    await dashboard.load();

    expect(dashboard.domainsError).toBeNull();
    expect(dashboard.domains).toHaveLength(1);
    expect(dashboard.domains.at(0)?.name).toBe('netzhansa.com');
  });

  it('reports zero domains, not an undefined-length envelope, when items is empty', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal('fetch', makeFetch({ items: [], next: null }));

    await dashboard.load();

    expect(dashboard.domainsError).toBeNull();
    expect(dashboard.domains).toEqual([]);
  });

  it('still accepts a bare array response for backward compatibility', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch([{ name: 'example.com', created_at: '2026-01-01T00:00:00Z' }]),
    );

    await dashboard.load();

    expect(dashboard.domains).toHaveLength(1);
    expect(dashboard.domains.at(0)?.name).toBe('example.com');
  });
});
