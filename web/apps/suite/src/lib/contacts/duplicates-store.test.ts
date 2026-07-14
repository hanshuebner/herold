/**
 * Tests for the contacts duplicate-check store (re #220): incremental
 * paging + reclustering, selection, bulk delete, and bulk merge.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../jmap/client', () => ({
  jmap: {
    session: null,
    hasCapability: vi.fn(() => true),
    batch: vi.fn(),
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:contacts': 'account1',
      },
    },
    principalId: 'p1',
  },
}));

import { duplicatesStore } from './duplicates-store.svelte';
import { jmap } from '../jmap/client';

const mockBatch = vi.mocked(jmap.batch);

function card(id: string, name: string, email?: string, phone?: string): Record<string, unknown> {
  return {
    id,
    name: { full: name },
    emails: email ? { e1: { '@type': 'EmailAddress', address: email } } : {},
    phones: phone ? { p1: { '@type': 'Phone', number: phone } } : {},
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  duplicatesStore.candidates = [];
  duplicatesStore.clusters = [];
  duplicatesStore.rows = [];
  duplicatesStore.total = null;
  duplicatesStore.status = 'idle';
  duplicatesStore.clearSelection();
});

describe('init / #loadPage', () => {
  it('loads the first page and clusters duplicates found so far', async () => {
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { total: 3, ids: ['c1', 'c2', 'c3'] }, '0'],
        [
          'Contact/get',
          {
            list: [
              card('c1', 'Ada Lovelace', 'ada@example.local'),
              card('c2', 'Ada Lovelace2', 'ada@example.local'),
              card('c3', 'Unrelated Person', 'nobody@example.local'),
            ],
          },
          '1',
        ],
      ],
    } as never);

    await duplicatesStore.init();

    expect(duplicatesStore.status).toBe('ready');
    expect(duplicatesStore.candidates).toHaveLength(3);
    // Only c1/c2 share an email -- c3 is not a flagged row.
    expect(duplicatesStore.rows.map((r) => r.id).sort()).toEqual(['c1', 'c2']);
    expect(duplicatesStore.rows.find((r) => r.id === 'c1')!.match.emails).toEqual([
      'ada@example.local',
    ]);
  });
});

describe('loadMore', () => {
  it('appends a second page and reclusters over the full candidate set', async () => {
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { total: 2, ids: ['c1'] }, '0'],
        ['Contact/get', { list: [card('c1', 'Solo Person', 'solo@example.local')] }, '1'],
      ],
    } as never);
    await duplicatesStore.init();
    expect(duplicatesStore.rows).toHaveLength(0); // no duplicate yet

    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { total: 2, ids: ['c2'] }, '0'],
        ['Contact/get', { list: [card('c2', 'Solo Person2', 'solo@example.local')] }, '1'],
      ],
    } as never);
    await duplicatesStore.loadMore();

    expect(duplicatesStore.candidates).toHaveLength(2);
    expect(duplicatesStore.rows.map((r) => r.id).sort()).toEqual(['c1', 'c2']);
  });
});

describe('selection', () => {
  beforeEach(() => {
    duplicatesStore.rows = [
      { id: 'c1', displayName: 'A', emails: [], phones: [], photoBlobId: null, clusterIndex: 0, reasons: ['email'], match: { emails: [], phones: [], closeNames: [] } },
      { id: 'c2', displayName: 'B', emails: [], phones: [], photoBlobId: null, clusterIndex: 0, reasons: ['email'], match: { emails: [], phones: [], closeNames: [] } },
    ];
  });

  it('toggleSelected adds then removes', () => {
    duplicatesStore.toggleSelected('c1');
    expect(duplicatesStore.selectedIds.has('c1')).toBe(true);
    duplicatesStore.toggleSelected('c1');
    expect(duplicatesStore.selectedIds.has('c1')).toBe(false);
  });

  it('toggleSelectAllVisible selects then clears', () => {
    duplicatesStore.toggleSelectAllVisible(['c1', 'c2']);
    expect([...duplicatesStore.selectedIds].sort()).toEqual(['c1', 'c2']);
    duplicatesStore.toggleSelectAllVisible(['c1', 'c2']);
    expect(duplicatesStore.selectedIds.size).toBe(0);
  });
});

describe('bulkDelete', () => {
  it('destroys given ids, drops them from candidates, and reclusters', async () => {
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { total: 2, ids: ['c1', 'c2'] }, '0'],
        [
          'Contact/get',
          {
            list: [
              card('c1', 'Dup One', 'dup@example.local'),
              card('c2', 'Dup Two', 'dup@example.local'),
            ],
          },
          '1',
        ],
      ],
    } as never);
    await duplicatesStore.init();
    expect(duplicatesStore.rows).toHaveLength(2);

    mockBatch.mockResolvedValueOnce({
      responses: [['Contact/set', { destroyed: ['c2'] }, '0']],
    } as never);
    const failed = await duplicatesStore.bulkDelete(['c2']);

    expect(failed).toEqual([]);
    expect(duplicatesStore.candidates.map((c) => c.id)).toEqual(['c1']);
    // c1 no longer has a duplicate peer -- it drops out of the flagged rows.
    expect(duplicatesStore.rows).toHaveLength(0);
  });
});

describe('bulkMerge', () => {
  it('merges a fully-selected cluster via a single Contact/set update+destroy', async () => {
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { total: 2, ids: ['c1', 'c2'] }, '0'],
        [
          'Contact/get',
          {
            list: [
              card('c1', 'Dup One', 'dup@example.local'),
              card('c2', 'Dup Two', 'dup@example.local'),
            ],
          },
          '1',
        ],
      ],
    } as never);
    await duplicatesStore.init();

    // Contact/get (full card refetch) then Contact/set.
    mockBatch.mockResolvedValueOnce({
      responses: [
        [
          'Contact/get',
          {
            list: [
              card('c1', 'Dup One', 'dup@example.local'),
              card('c2', 'Dup Two', 'dup@example.local'),
            ],
          },
          '0',
        ],
      ],
    } as never);
    mockBatch.mockResolvedValueOnce({
      responses: [['Contact/set', { updated: { c1: {} }, destroyed: ['c2'] }, '0']],
    } as never);

    const result = await duplicatesStore.bulkMerge(['c1', 'c2']);

    expect(result.mergedClusters).toBe(1);
    expect(result.skipped).toEqual([]);
    expect(mockBatch).toHaveBeenCalledTimes(3); // init page + get + set
    expect(duplicatesStore.candidates.map((c) => c.id)).toEqual(['c1']);
  });

  it('skips a cluster with fewer than 2 selected members', async () => {
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { total: 2, ids: ['c1', 'c2'] }, '0'],
        [
          'Contact/get',
          {
            list: [
              card('c1', 'Dup One', 'dup@example.local'),
              card('c2', 'Dup Two', 'dup@example.local'),
            ],
          },
          '1',
        ],
      ],
    } as never);
    await duplicatesStore.init();

    const result = await duplicatesStore.bulkMerge(['c1']);
    expect(result.mergedClusters).toBe(0);
    expect(result.skipped).toEqual(['c1']);
  });
});
