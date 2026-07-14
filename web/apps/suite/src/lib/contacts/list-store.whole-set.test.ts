/**
 * Tests for the contacts list store's "select all N matching" whole-set
 * selection (re #221): resolveSelectionIds, the filter-scoped id
 * enumeration, and bulkDelete's chunked Contact/set calls.
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
  },
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(() => () => {}),
  },
}));

import { contactsListStore } from './list-store.svelte';
import { jmap } from '../jmap/client';

const mockBatch = vi.mocked(jmap.batch);

function seedRows(): void {
  contactsListStore.rows = [
    { id: 'c1', displayName: 'Ada Lovelace', secondary: 'ada@example.local', photoBlobId: null },
    { id: 'c2', displayName: 'Grace Hopper', secondary: 'grace@example.local', photoBlobId: null },
  ];
  contactsListStore.total = 500;
  contactsListStore.searchText = '';
  contactsListStore.activeBookId = null;
  contactsListStore.setGroup(null, null);
}

beforeEach(() => {
  vi.clearAllMocks();
  contactsListStore.clearSelection();
  seedRows();
});

describe('selectAllMatching / resolveSelectionIds', () => {
  it('resolves to the loaded selection when whole-set mode is off', async () => {
    contactsListStore.selectAllVisible(['c1', 'c2']);
    const ids = await contactsListStore.resolveSelectionIds();
    expect(ids.sort()).toEqual(['c1', 'c2']);
    expect(mockBatch).not.toHaveBeenCalled();
  });

  it('enumerates every matching id via Contact/query when whole-set mode is on', async () => {
    contactsListStore.selectAllVisible(['c1', 'c2']);
    contactsListStore.selectAllMatching();

    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/query', { ids: Array.from({ length: 500 }, (_, i) => `id${i}`) }, '0'],
      ],
    } as never);
    mockBatch.mockResolvedValueOnce({
      responses: [['Contact/query', { ids: ['id500', 'id501'] }, '0']],
    } as never);

    const ids = await contactsListStore.resolveSelectionIds();

    expect(ids.length).toBe(502);
    expect(mockBatch).toHaveBeenCalledTimes(2);
  });

  it('falls back to the plain selection in group scope even when whole-set mode is on', async () => {
    contactsListStore.setGroup('g1', ['c1', 'c2']);
    // setGroup fires an async #reload() (Contact/get for the member ids);
    // let it settle and clear the mock before asserting on resolveSelectionIds.
    await Promise.resolve();
    await Promise.resolve();
    mockBatch.mockClear();

    contactsListStore.selectAllVisible(['c1', 'c2']);
    contactsListStore.selectAllMatching();

    const ids = await contactsListStore.resolveSelectionIds();
    expect(ids.sort()).toEqual(['c1', 'c2']);
    expect(mockBatch).not.toHaveBeenCalled();
  });

  it('toggleSelectAllVisible and selectAllVisible drop whole-set mode', async () => {
    contactsListStore.selectAllMatching();
    contactsListStore.selectAllVisible(['c1']);
    expect(await contactsListStore.resolveSelectionIds()).toEqual(['c1']);

    contactsListStore.selectAllMatching();
    contactsListStore.toggleSelectAllVisible(['c1', 'c2']);
    expect((await contactsListStore.resolveSelectionIds()).sort()).toEqual(['c1', 'c2']);
  });
});

describe('bulkDelete chunking', () => {
  it('splits a destroy list larger than 500 into multiple Contact/set calls in one batch', async () => {
    const ids = Array.from({ length: 750 }, (_, i) => `id${i}`);
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/set', { destroyed: ids.slice(0, 500) }, '0'],
        ['Contact/set', { destroyed: ids.slice(500) }, '1'],
      ],
    } as never);

    const failed = await contactsListStore.bulkDelete(ids);

    expect(failed).toEqual([]);
    expect(mockBatch).toHaveBeenCalledTimes(1);
  });
});
