/**
 * Tests for the principals state class's envelope unwrapping (re #216).
 *
 * GET /api/v1/principals wraps its payload in the pageDTO envelope
 * (internal/protoadmin/principals.go handleListPrincipals):
 *
 *   {"items": [...], "next": "<cursor>"|null}
 *
 * The loader must unwrap this instead of assigning the envelope
 * object straight to the PrincipalSummary[]-typed items state (which
 * makes the Svelte #each fall back to an empty array, so the view
 * renders "no principals found" even when the server has data).
 *
 * The cursor query parameter must also be named `after` (matching
 * handleListPrincipals's r.URL.Query().Get("after")), not `after_id`,
 * which the server never reads.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface Summary {
  id: string;
  email: string;
  display_name: string;
  flags: number;
  created_at: string;
}

function makeFetch(body: unknown) {
  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/principals')) {
      return Promise.resolve(jsonResponse(body));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

interface PrincipalsModule {
  principals: {
    status: string;
    items: Summary[];
    cursor: string;
    hasMore: boolean;
    errorMessage: string | null;
    load(): Promise<void>;
    loadMore(): Promise<void>;
  };
}

async function loadPrincipals(): Promise<PrincipalsModule> {
  vi.resetModules();
  return (await import('./principals.svelte')) as unknown as PrincipalsModule;
}

describe('Principals list envelope unwrapping (re #216)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {items,next} envelope returned by GET /api/v1/principals', async () => {
    const { principals } = await loadPrincipals();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [
          { id: '1', email: 'admin@example.local', display_name: 'Admin', flags: 1, created_at: '2026-01-01T00:00:00Z' },
          { id: '2', email: 'alice@example.local', display_name: 'Alice', flags: 0, created_at: '2026-01-01T00:00:00Z' },
        ],
        next: null,
      }),
    );

    await principals.load();

    expect(principals.errorMessage).toBeNull();
    expect(principals.status).toBe('ready');
    expect(principals.items).toHaveLength(2);
    expect(principals.items.at(0)?.email).toBe('admin@example.local');
  });

  it('reports zero principals, not an undefined-length envelope, when items is empty', async () => {
    const { principals } = await loadPrincipals();
    vi.stubGlobal('fetch', makeFetch({ items: [], next: null }));

    await principals.load();

    expect(principals.errorMessage).toBeNull();
    expect(principals.items).toEqual([]);
  });

  it('sends the pagination cursor as `after`, not `after_id`', async () => {
    const { principals } = await loadPrincipals();
    const fetchMock = makeFetch({ items: [], next: null });
    vi.stubGlobal('fetch', fetchMock);

    await principals.load();

    const calledUrl = fetchMock.mock.calls[0]?.[0] as string;
    expect(calledUrl).toContain('after=0');
    expect(calledUrl).not.toContain('after_id');
  });

  it('loadMore unwraps the envelope and appends items', async () => {
    const { principals } = await loadPrincipals();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: Array.from({ length: 50 }, (_, i) => ({
          id: String(i + 1),
          email: `user${i}@example.local`,
          display_name: '',
          flags: 0,
          created_at: '2026-01-01T00:00:00Z',
        })),
        next: '50',
      }),
    );

    await principals.load();
    expect(principals.items).toHaveLength(50);
    expect(principals.hasMore).toBe(true);

    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [
          { id: '51', email: 'last@example.local', display_name: '', flags: 0, created_at: '2026-01-01T00:00:00Z' },
        ],
        next: null,
      }),
    );

    await principals.loadMore();
    expect(principals.items).toHaveLength(51);
    expect(principals.items.at(-1)?.email).toBe('last@example.local');
  });
});
