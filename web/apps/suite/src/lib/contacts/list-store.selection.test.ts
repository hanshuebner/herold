/**
 * Tests for the contacts list store's multi-select / bulk-delete surface
 * (re #191): selectedIds toggling, select-all/clear, and bulkDelete's
 * Contact/set destroy call plus local row/selection reconciliation.
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
    { id: 'c3', displayName: 'Katherine Johnson', secondary: 'katherine@example.local', photoBlobId: null },
  ];
  contactsListStore.total = 3;
}

beforeEach(() => {
  vi.clearAllMocks();
  contactsListStore.clearSelection();
  seedRows();
});

describe('selection toggling', () => {
  it('toggleSelected adds then removes an id', () => {
    contactsListStore.toggleSelected('c1');
    expect(contactsListStore.selectedIds.has('c1')).toBe(true);
    contactsListStore.toggleSelected('c1');
    expect(contactsListStore.selectedIds.has('c1')).toBe(false);
  });

  it('selectAllVisible replaces the selection with exactly the given ids', () => {
    contactsListStore.toggleSelected('c2');
    contactsListStore.selectAllVisible(['c1', 'c2', 'c3']);
    expect([...contactsListStore.selectedIds].sort()).toEqual(['c1', 'c2', 'c3']);
  });

  it('clearSelection empties the set', () => {
    contactsListStore.selectAllVisible(['c1', 'c2']);
    contactsListStore.clearSelection();
    expect(contactsListStore.selectedIds.size).toBe(0);
  });
});

describe('bulkDelete', () => {
  it('destroys the given ids via a single Contact/set call, drops rows, and clears selection', async () => {
    contactsListStore.selectAllVisible(['c1', 'c3']);
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/set', { destroyed: ['c1', 'c3'], notDestroyed: {} }, '0'],
      ],
    } as never);

    const failed = await contactsListStore.bulkDelete(['c1', 'c3']);

    expect(failed).toEqual([]);
    expect(mockBatch).toHaveBeenCalledTimes(1);
    expect(contactsListStore.rows.map((r) => r.id)).toEqual(['c2']);
    expect(contactsListStore.total).toBe(1);
    expect(contactsListStore.selectedIds.size).toBe(0);
  });

  it('reports ids the server did not destroy and leaves their rows intact', async () => {
    contactsListStore.selectAllVisible(['c1', 'c2']);
    mockBatch.mockResolvedValueOnce({
      responses: [
        ['Contact/set', { destroyed: ['c1'], notDestroyed: { c2: { type: 'forbidden' } } }, '0'],
      ],
    } as never);

    const failed = await contactsListStore.bulkDelete(['c1', 'c2']);

    expect(failed).toEqual(['c2']);
    expect(contactsListStore.rows.map((r) => r.id)).toEqual(['c2', 'c3']);
    expect(contactsListStore.selectedIds.has('c2')).toBe(true);
    expect(contactsListStore.selectedIds.has('c1')).toBe(false);
  });

  it('returns all ids and leaves rows untouched on a JMAP error response', async () => {
    mockBatch.mockResolvedValueOnce({
      responses: [['error', { type: 'serverFail' }, '0']],
    } as never);

    const failed = await contactsListStore.bulkDelete(['c1']);

    expect(failed).toEqual(['c1']);
    expect(contactsListStore.rows.map((r) => r.id)).toEqual(['c1', 'c2', 'c3']);
  });

  it('is a no-op for an empty id list', async () => {
    const failed = await contactsListStore.bulkDelete([]);
    expect(failed).toEqual([]);
    expect(mockBatch).not.toHaveBeenCalled();
  });
});
