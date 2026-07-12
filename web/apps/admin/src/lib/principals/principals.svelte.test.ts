/**
 * Tests for the principals state class's envelope unwrapping (re #216)
 * and field mapping against the server's principalDTO (re #218).
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
 *
 * Each item in `items` must match internal/protoadmin/types.go's
 * principalDTO field-for-field: `id` is a JSON number, the email field
 * is named `canonical_email` (not `email`), and `flags` is an array of
 * flag-name strings (not a numeric bitmask).
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface Summary {
  id: number;
  canonical_email: string;
  display_name: string;
  flags: string[];
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
          { id: 1, canonical_email: 'admin@example.local', display_name: 'Admin', flags: ['admin'], created_at: '2026-01-01T00:00:00Z' },
          { id: 2, canonical_email: 'alice@example.local', display_name: 'Alice', flags: [], created_at: '2026-01-01T00:00:00Z' },
        ],
        next: null,
      }),
    );

    await principals.load();

    expect(principals.errorMessage).toBeNull();
    expect(principals.status).toBe('ready');
    expect(principals.items).toHaveLength(2);
    expect(principals.items.at(0)?.canonical_email).toBe('admin@example.local');
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
          id: i + 1,
          canonical_email: `user${i}@example.local`,
          display_name: '',
          flags: [],
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
          { id: 51, canonical_email: 'last@example.local', display_name: '', flags: [], created_at: '2026-01-01T00:00:00Z' },
        ],
        next: null,
      }),
    );

    await principals.loadMore();
    expect(principals.items).toHaveLength(51);
    expect(principals.items.at(-1)?.canonical_email).toBe('last@example.local');
  });
});

describe('Principals list field mapping against principalDTO (re #218)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('maps canonical_email, numeric id, and string-array flags from a representative principalDTO response', async () => {
    const { principals } = await loadPrincipals();
    // Shape taken verbatim from internal/protoadmin/types.go's principalDTO
    // (toPrincipalDTO), as observed from a live GET /api/v1/principals
    // response against a dev-instance.sh seed.
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [
          {
            id: 1,
            kind: 'user',
            canonical_email: 'admin@example.local',
            quota_bytes: 0,
            flags: ['admin', 'super_admin', 'totp_enabled'],
            totp_enabled: true,
            seen_addresses_enabled: false,
            created_at: '2026-07-12T18:39:05.400041Z',
            updated_at: '2026-07-12T18:39:05.425518Z',
          },
          {
            id: 2,
            kind: 'user',
            canonical_email: 'alice@example.local',
            quota_bytes: 0,
            flags: [],
            totp_enabled: false,
            seen_addresses_enabled: false,
            created_at: '2026-07-12T18:39:05.596376Z',
            updated_at: '2026-07-12T18:39:05.597678Z',
          },
        ],
        next: null,
      }),
    );

    await principals.load();

    expect(principals.errorMessage).toBeNull();
    expect(principals.items).toHaveLength(2);

    const admin = principals.items.at(0);
    expect(admin).toBeDefined();
    expect(admin?.id).toBe(1);
    expect(typeof admin?.id).toBe('number');
    expect(admin?.canonical_email).toBe('admin@example.local');
    expect(admin?.flags).toEqual(['admin', 'super_admin', 'totp_enabled']);
    expect(admin?.flags.includes('admin')).toBe(true);
    expect(admin?.flags.includes('totp_enabled')).toBe(true);
    expect(admin?.flags.includes('disabled')).toBe(false);

    const alice = principals.items.at(1);
    expect(alice).toBeDefined();
    expect(alice?.id).toBe(2);
    expect(alice?.canonical_email).toBe('alice@example.local');
    expect(alice?.flags).toEqual([]);

    // The pagination cursor is derived from the numeric id, coerced to the
    // string the `after` query parameter expects.
    expect(principals.cursor).toBe('2');
  });
});
