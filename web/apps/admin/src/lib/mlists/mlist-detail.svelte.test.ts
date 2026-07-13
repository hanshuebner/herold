/**
 * Tests for the mailing list detail + roster state class (epic #183,
 * REQ-MLIST-42): the config load, the roster's {items,next} pageDTO
 * unwrap (GET /api/v1/lists/{id}/members), and the member CRUD/import
 * wire shapes against internal/protoadmin/mlist_dto.go's
 * mailingListMemberDTO.
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

function memberDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 10,
    list_id: 1,
    external_address: 'someone@gmail.com',
    state: 'active',
    delivery_mode: 'each',
    added_at: '2026-07-12T18:39:05.400041Z',
    ...overrides,
  };
}

interface DetailModule {
  mlistDetail: {
    status: string;
    list: Record<string, unknown> | null;
    errorMessage: string | null;
    members: Array<Record<string, unknown>>;
    membersStatus: string;
    membersErrorMessage: string | null;
    membersCursor: string | null;
    membersHasMore: boolean;
    stateFilter: string;
    deliveryModeFilter: string;
    summary: Record<string, number> | null;
    summaryStatus: string;
    summaryErrorMessage: string | null;
    load(id: string): Promise<void>;
    loadMembers(id: string): Promise<void>;
    loadMoreMembers(id: string): Promise<void>;
    loadSummary(id: string): Promise<void>;
    updateList(id: string, payload: unknown): Promise<{ ok: boolean; errorMessage: string | null }>;
    deleteList(id: string): Promise<{ ok: boolean; errorMessage: string | null }>;
    addMember(id: string, payload: unknown): Promise<{ ok: boolean; errorMessage: string | null }>;
    updateMember(id: string, memberId: number, payload: unknown): Promise<{ ok: boolean; errorMessage: string | null }>;
    removeMember(id: string, memberId: number): Promise<{ ok: boolean; errorMessage: string | null }>;
    importMembers(
      id: string,
      members: unknown[],
    ): Promise<{ ok: boolean; errorMessage: string | null; result?: { added: number; skipped: number; results: unknown[] } }>;
    exportMembers(id: string): Promise<{ ok: boolean; errorMessage: string | null; items?: unknown[]; truncated?: boolean }>;
  };
}

function summaryDTO(overrides: Record<string, unknown> = {}) {
  return {
    active: 3,
    suspended: 1,
    unsubscribed: 0,
    pending: 0,
    awaiting_approval: 0,
    ...overrides,
  };
}

async function loadDetail(): Promise<DetailModule> {
  vi.resetModules();
  return (await import('./mlist-detail.svelte')) as unknown as DetailModule;
}

describe('mlistDetail config + roster load', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads the list config, then unwraps the {items,next} roster envelope', async () => {
    const { mlistDetail } = await loadDetail();
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url.includes('/members')) {
        return Promise.resolve(
          jsonResponse({
            items: [memberDTO(), memberDTO({ id: 11, external_address: 'other@example.com' })],
            next: null,
          }),
        );
      }
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    await mlistDetail.load('1');

    expect(mlistDetail.errorMessage).toBeNull();
    expect(mlistDetail.status).toBe('ready');
    expect(mlistDetail.list?.posting_address).toBe('announce@example.local');
    expect(mlistDetail.members).toHaveLength(2);
    expect(mlistDetail.members.at(0)?.external_address).toBe('someone@gmail.com');
    expect(mlistDetail.membersHasMore).toBe(false);
  });

  it('reports zero members, not an undefined-length envelope, when the roster page is empty', async () => {
    const { mlistDetail } = await loadDetail();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [], next: null }));
        return Promise.resolve(jsonResponse(mailingListDTO()));
      }),
    );

    await mlistDetail.load('1');

    expect(mlistDetail.members).toEqual([]);
  });

  it('maps mailingListMemberDTO fields for both internal and external members', async () => {
    const { mlistDetail } = await loadDetail();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members')) {
          return Promise.resolve(
            jsonResponse({
              items: [
                memberDTO({ id: 20, external_address: 'ext@example.com', state: 'active', delivery_mode: 'each' }),
                memberDTO({ id: 21, external_address: undefined, principal_id: 7, state: 'suspended', delivery_mode: 'nomail', bounce_score: 2.5 }),
              ],
              next: null,
            }),
          );
        }
        return Promise.resolve(jsonResponse(mailingListDTO()));
      }),
    );

    await mlistDetail.load('1');

    const ext = mlistDetail.members.find((m) => m.id === 20);
    expect(ext?.external_address).toBe('ext@example.com');
    expect(ext?.principal_id).toBeUndefined();

    const internal = mlistDetail.members.find((m) => m.id === 21);
    expect(internal?.principal_id).toBe(7);
    expect(internal?.state).toBe('suspended');
    expect(internal?.delivery_mode).toBe('nomail');
    expect(internal?.bounce_score).toBe(2.5);
  });

  it('loads the REQ-MLIST-54 member-state summary alongside the roster', async () => {
    const { mlistDetail } = await loadDetail();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members/summary')) return Promise.resolve(jsonResponse(summaryDTO()));
        if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [], next: null }));
        return Promise.resolve(jsonResponse(mailingListDTO()));
      }),
    );

    await mlistDetail.load('1');

    expect(mlistDetail.summaryStatus).toBe('ready');
    expect(mlistDetail.summary).toEqual(summaryDTO());
  });

  it('sends state/delivery_mode filters as query parameters', async () => {
    const { mlistDetail } = await loadDetail();
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    mlistDetail.stateFilter = 'active';
    mlistDetail.deliveryModeFilter = 'each';
    await mlistDetail.loadMembers('1');

    const call = fetchMock.mock.calls.find((c) => (c[0] as string).includes('/members'));
    expect(call?.[0]).toContain('state=active');
    expect(call?.[0]).toContain('delivery_mode=each');
  });
});

describe('mlistDetail member mutations', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('addMember posts createMemberRequest and reloads the roster', async () => {
    const { mlistDetail } = await loadDetail();
    let postBody: Record<string, unknown> | null = null;
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url.includes('/members')) {
        postBody = JSON.parse(init.body as string) as Record<string, unknown>;
        return Promise.resolve(jsonResponse(memberDTO({ id: 30, principal_id: 5, external_address: undefined }), 201));
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [memberDTO({ id: 30, principal_id: 5 })], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await mlistDetail.addMember('1', { principal_id: 5 });

    expect(result.ok).toBe(true);
    expect(postBody).toEqual({ principal_id: 5 });
  });

  it('updateMember sends a patchMemberRequest and updates local state', async () => {
    const { mlistDetail } = await loadDetail();
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') {
        return Promise.resolve(jsonResponse(memberDTO({ id: 30, state: 'suspended' })));
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [memberDTO({ id: 30 })], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    await mlistDetail.load('1');
    const result = await mlistDetail.updateMember('1', 30, { state: 'suspended' });

    expect(result.ok).toBe(true);
    expect(mlistDetail.members.find((m) => m.id === 30)?.state).toBe('suspended');
  });

  it('updateMember with a state transition (REQ-MLIST-55 reactivate) refreshes the summary counts', async () => {
    const { mlistDetail } = await loadDetail();
    let summaryCalls = 0;
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') {
        return Promise.resolve(jsonResponse(memberDTO({ id: 30, state: 'active', bounce_score: 0 })));
      }
      if (url.includes('/members/summary')) {
        summaryCalls += 1;
        return Promise.resolve(jsonResponse(summaryDTO({ active: 4, suspended: 0 })));
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [memberDTO({ id: 30, state: 'suspended' })], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    await mlistDetail.load('1');
    expect(summaryCalls).toBe(1);

    const result = await mlistDetail.updateMember('1', 30, { state: 'active' });

    expect(result.ok).toBe(true);
    expect(mlistDetail.members.find((m) => m.id === 30)?.state).toBe('active');
    expect(summaryCalls).toBe(2);
    expect(mlistDetail.summary).toEqual(summaryDTO({ active: 4, suspended: 0 }));
  });

  it('updateMember with only a delivery_mode change does not refetch the summary', async () => {
    const { mlistDetail } = await loadDetail();
    let summaryCalls = 0;
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') {
        return Promise.resolve(jsonResponse(memberDTO({ id: 30, delivery_mode: 'nomail', principal_id: 5, external_address: undefined })));
      }
      if (url.includes('/members/summary')) {
        summaryCalls += 1;
        return Promise.resolve(jsonResponse(summaryDTO()));
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [memberDTO({ id: 30 })], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    await mlistDetail.load('1');
    expect(summaryCalls).toBe(1);

    await mlistDetail.updateMember('1', 30, { delivery_mode: 'nomail' });

    expect(summaryCalls).toBe(1);
  });

  it('removeMember deletes and drops the row from local state', async () => {
    const { mlistDetail } = await loadDetail();
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }));
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [memberDTO({ id: 30 })], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    await mlistDetail.load('1');
    expect(mlistDetail.members).toHaveLength(1);

    const result = await mlistDetail.removeMember('1', 30);

    expect(result.ok).toBe(true);
    expect(mlistDetail.members).toHaveLength(0);
  });

  it('importMembers posts the members array and reloads on success', async () => {
    const { mlistDetail } = await loadDetail();
    let importBody: Record<string, unknown> | null = null;
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url.includes('/members/import')) {
        importBody = JSON.parse(init!.body as string) as Record<string, unknown>;
        return Promise.resolve(
          jsonResponse({
            added: 1,
            skipped: 1,
            results: [
              { index: 0, status: 'added', id: 40 },
              { index: 1, status: 'conflict', detail: 'already a member' },
            ],
          }),
        );
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await mlistDetail.importMembers('1', [{ external_address: 'a@example.com' }, { external_address: 'b@example.com' }]);

    expect(result.ok).toBe(true);
    expect(result.result?.added).toBe(1);
    expect(result.result?.skipped).toBe(1);
    expect((importBody as unknown as { members: unknown[] }).members).toHaveLength(2);
  });

  it('exportMembers unwraps the {items,next} envelope from the export endpoint', async () => {
    const { mlistDetail } = await loadDetail();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members/export')) {
          return Promise.resolve(jsonResponse({ items: [memberDTO({ id: 50 })], next: null }));
        }
        return Promise.resolve(jsonResponse({ items: [], next: null }));
      }),
    );

    const result = await mlistDetail.exportMembers('1');

    expect(result.ok).toBe(true);
    expect(result.items).toHaveLength(1);
    expect(result.truncated).toBe(false);
  });

  it('deleteList calls DELETE /api/v1/lists/{id}', async () => {
    const { mlistDetail } = await loadDetail();
    const fetchMock = vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await mlistDetail.deleteList('1');
    expect(result.ok).toBe(true);
  });

  it('updateList PATCHes patchMailingListRequest fields', async () => {
    const { mlistDetail } = await loadDetail();
    let patchBody: Record<string, unknown> | null = null;
    const fetchMock = vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') {
        patchBody = JSON.parse(init.body as string) as Record<string, unknown>;
        return Promise.resolve(jsonResponse(mailingListDTO({ display_name: 'Renamed' })));
      }
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await mlistDetail.updateList('1', { display_name: 'Renamed' });

    expect(result.ok).toBe(true);
    expect(patchBody).toEqual({ display_name: 'Renamed' });
    expect(mlistDetail.list?.display_name).toBe('Renamed');
  });
});
