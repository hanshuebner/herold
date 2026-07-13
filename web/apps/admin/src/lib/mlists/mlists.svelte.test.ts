/**
 * Tests for the mailing lists state class's envelope unwrapping and field
 * mapping against the server's mailingListDTO (epic #183, REQ-MLIST-42).
 *
 * GET /api/v1/lists wraps its payload in the {items, next} pageDTO envelope
 * (internal/protoadmin/mlist.go's mailingListPage). The loader must unwrap
 * this rather than assigning the envelope straight to the
 * MailingListSummary[]-typed items state (re #215/#216/#218: the same class
 * of bug hit the dashboard and principals loaders before this).
 *
 * Each item must match internal/protoadmin/mlist_dto.go's mailingListDTO
 * field-for-field: numeric ids, `posting_address`/`display_name` (not
 * `name`), `owner_principal_id`, and `subject_tag`/`max_message_size_bytes`
 * as omitempty optionals.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function mailingListDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    principal_id: 100,
    posting_address: 'announce@example.local',
    domain: 'example.local',
    display_name: 'Announcements',
    owner_principal_id: 2,
    arc_seal: true,
    posting_policy: 'open',
    subscribe_policy: 'closed',
    created_at: '2026-07-12T18:39:05.400041Z',
    updated_at: '2026-07-12T18:39:05.425518Z',
    ...overrides,
  };
}

function makeFetch(body: unknown) {
  return vi.fn().mockImplementation((url: string) => {
    if (url.includes('/api/v1/lists')) {
      return Promise.resolve(jsonResponse(body));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

interface MlistsModule {
  mlists: {
    status: string;
    items: Array<Record<string, unknown>>;
    cursor: string | null;
    hasMore: boolean;
    errorMessage: string | null;
    load(): Promise<void>;
    loadMore(): Promise<void>;
    create(payload: unknown): Promise<{ ok: boolean; errorMessage: string | null; id?: number }>;
  };
}

async function loadMlists(): Promise<MlistsModule> {
  vi.resetModules();
  return (await import('./mlists.svelte')) as unknown as MlistsModule;
}

describe('mlists list envelope unwrapping', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('unwraps the {items,next} envelope returned by GET /api/v1/lists', async () => {
    const { mlists } = await loadMlists();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [mailingListDTO(), mailingListDTO({ id: 2, posting_address: 'dev@example.local', display_name: 'Dev list' })],
        next: null,
      }),
    );

    await mlists.load();

    expect(mlists.errorMessage).toBeNull();
    expect(mlists.status).toBe('ready');
    expect(mlists.items).toHaveLength(2);
    expect(mlists.items.at(0)?.posting_address).toBe('announce@example.local');
    expect(mlists.hasMore).toBe(false);
  });

  it('reports zero lists, not an undefined-length envelope, when items is empty', async () => {
    const { mlists } = await loadMlists();
    vi.stubGlobal('fetch', makeFetch({ items: [], next: null }));

    await mlists.load();

    expect(mlists.errorMessage).toBeNull();
    expect(mlists.items).toEqual([]);
  });

  it('maps every mailingListDTO field, not a guessed shape', async () => {
    const { mlists } = await loadMlists();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [
          mailingListDTO({
            subject_tag: 'ann',
            max_message_size_bytes: 10485760,
          }),
        ],
        next: null,
      }),
    );

    await mlists.load();

    const item = mlists.items.at(0);
    expect(item).toBeDefined();
    expect(item?.id).toBe(1);
    expect(item?.principal_id).toBe(100);
    expect(item?.posting_address).toBe('announce@example.local');
    expect(item?.domain).toBe('example.local');
    expect(item?.display_name).toBe('Announcements');
    expect(item?.owner_principal_id).toBe(2);
    expect(item?.subject_tag).toBe('ann');
    expect(item?.arc_seal).toBe(true);
    expect(item?.posting_policy).toBe('open');
    expect(item?.subscribe_policy).toBe('closed');
    expect(item?.max_message_size_bytes).toBe(10485760);
  });

  it('sets hasMore/cursor from the pageDTO next field, and loadMore appends', async () => {
    const { mlists } = await loadMlists();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [mailingListDTO({ id: 1 })],
        next: '1',
      }),
    );

    await mlists.load();
    expect(mlists.hasMore).toBe(true);
    expect(mlists.cursor).toBe('1');

    vi.stubGlobal(
      'fetch',
      makeFetch({
        items: [mailingListDTO({ id: 2, posting_address: 'second@example.local' })],
        next: null,
      }),
    );

    await mlists.loadMore();
    expect(mlists.items).toHaveLength(2);
    expect(mlists.items.at(-1)?.posting_address).toBe('second@example.local');
    expect(mlists.hasMore).toBe(false);
  });

  it('create() posts createMailingListRequest fields and reloads on success', async () => {
    const { mlists } = await loadMlists();
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        const body = JSON.parse(init.body as string) as Record<string, unknown>;
        expect(body.posting_address).toBe('team@example.local');
        expect(body.display_name).toBe('Team list');
        return Promise.resolve(jsonResponse(mailingListDTO({ id: 9, posting_address: 'team@example.local', display_name: 'Team list' }), 201));
      }
      return Promise.resolve(jsonResponse({ items: [mailingListDTO({ id: 9 })], next: null }));
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await mlists.create({ posting_address: 'team@example.local', display_name: 'Team list' });

    expect(result.ok).toBe(true);
    expect(result.id).toBe(9);
  });
});
