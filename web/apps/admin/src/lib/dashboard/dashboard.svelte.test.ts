/**
 * Tests for the dashboard state class's envelope unwrapping (re #215).
 *
 * Several admin REST endpoints wrap their payload in an envelope object
 * rather than returning the bare shape the Dashboard cards render:
 *
 *   GET /api/v1/domains        -> {items:[...], next}  (handleListDomains)
 *   GET /api/v1/audit          -> {items:[...], next}  (handleAuditLog)
 *   GET /api/v1/queue/stats    -> {counts:{...}}       (handleQueueStats)
 *
 * The dedicated Domains/Audit views already unwrap these envelopes; the
 * dashboard loader must do the same instead of assigning the envelope
 * straight to the array/map-typed card state (which makes `.length` or a
 * per-state count come out `undefined`, so the card renders empty even
 * when the server has data).
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface FetchBodies {
  domains?: unknown;
  audit?: unknown;
  queueStats?: unknown;
}

function makeFetch(bodies: FetchBodies = {}) {
  const {
    domains = { items: [], next: null },
    audit = { items: [], next: null },
    queueStats = { counts: { queued: 0, deferred: 0, delivered: 0, failed: 0, held: 0 } },
  } = bodies;

  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/domains')) {
      return Promise.resolve(jsonResponse(domains));
    }
    if ((url as string).includes('/api/v1/queue/stats')) {
      return Promise.resolve(jsonResponse(queueStats));
    }
    if ((url as string).includes('/api/v1/audit')) {
      return Promise.resolve(jsonResponse(audit));
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

interface DashboardModule {
  dashboard: {
    domains: { name: string; created_at: string }[];
    domainsError: string | null;
    auditEntries: { id: string; action: string; created_at: string }[];
    auditError: string | null;
    queueStats: Record<string, number | undefined> | null;
    queueTotal: number;
    load(): Promise<void>;
  };
}

async function loadDashboard(): Promise<DashboardModule> {
  vi.resetModules();
  return (await import('./dashboard.svelte')) as unknown as DashboardModule;
}

describe('Dashboard domains card (re #215)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {items,next} envelope returned by GET /api/v1/domains', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        domains: { items: [{ name: 'netzhansa.com', created_at: '2026-01-01T00:00:00Z' }], next: null },
      }),
    );

    await dashboard.load();

    expect(dashboard.domainsError).toBeNull();
    expect(dashboard.domains).toHaveLength(1);
    expect(dashboard.domains.at(0)?.name).toBe('netzhansa.com');
  });

  it('reports zero domains, not an undefined-length envelope, when items is empty', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal('fetch', makeFetch({ domains: { items: [], next: null } }));

    await dashboard.load();

    expect(dashboard.domainsError).toBeNull();
    expect(dashboard.domains).toEqual([]);
  });

  it('still accepts a bare array response for backward compatibility', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch({ domains: [{ name: 'example.com', created_at: '2026-01-01T00:00:00Z' }] }),
    );

    await dashboard.load();

    expect(dashboard.domains).toHaveLength(1);
    expect(dashboard.domains.at(0)?.name).toBe('example.com');
  });
});

describe('Dashboard recent-activity (audit) card (re #215)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {items,next} envelope returned by GET /api/v1/audit', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        audit: {
          items: [
            { id: '1', action: 'auth.step_up', created_at: '2026-07-12T10:00:00Z' },
            { id: '2', action: 'auth.login', created_at: '2026-07-12T09:59:00Z' },
          ],
          next: null,
        },
      }),
    );

    await dashboard.load();

    expect(dashboard.auditError).toBeNull();
    expect(dashboard.auditEntries).toHaveLength(2);
    expect(dashboard.auditEntries.at(0)?.action).toBe('auth.step_up');
  });

  it('reports zero entries, not an undefined-length envelope, when items is empty', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal('fetch', makeFetch({ audit: { items: [], next: null } }));

    await dashboard.load();

    expect(dashboard.auditError).toBeNull();
    expect(dashboard.auditEntries).toEqual([]);
  });

  it('still accepts a bare array response for backward compatibility', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch({ audit: [{ id: '1', action: 'auth.login', created_at: '2026-07-12T09:59:00Z' }] }),
    );

    await dashboard.load();

    expect(dashboard.auditEntries).toHaveLength(1);
    expect(dashboard.auditEntries.at(0)?.action).toBe('auth.login');
  });
});

describe('Dashboard queue card (re #215)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {counts:{...}} envelope returned by GET /api/v1/queue/stats', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        queueStats: { counts: { queued: 3, deferred: 2, held: 1, delivered: 0, failed: 0 } },
      }),
    );

    await dashboard.load();

    expect(dashboard.queueStats?.queued).toBe(3);
    expect(dashboard.queueStats?.deferred).toBe(2);
    expect(dashboard.queueStats?.held).toBe(1);
    // queueTotal = queued + deferred + held (see dashboard.svelte.ts $derived)
    expect(dashboard.queueTotal).toBe(6);
  });

  it('still accepts a bare per-state map for backward compatibility', async () => {
    const { dashboard } = await loadDashboard();
    vi.stubGlobal('fetch', makeFetch({ queueStats: { queued: 4, deferred: 0, held: 0 } }));

    await dashboard.load();

    expect(dashboard.queueStats?.queued).toBe(4);
    expect(dashboard.queueTotal).toBe(4);
  });
});
