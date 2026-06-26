/**
 * TaggedAddressStore unit tests for the methods added alongside the
 * Settings surface (REQ-SET-TAG-03..05).
 *
 * Covers:
 *   - updateFilter: success path mirrors the server row into cache.
 *   - updateFilter: rejects on a notUpdated entry.
 *   - deleteFilter: success path removes from cache.
 *   - convertToSieve: success path drops the row and returns the body.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// Use vi.hoisted so the test-state shared with the mock factories
// is created BEFORE the auto-hoisted vi.mock() calls run.
const harness = vi.hoisted(() => {
  return {
    lastBatchCalls: [] as Array<{ name: string; args: unknown }>,
    mockResponses: [] as unknown[],
    convertFilterToSieve: vi.fn(async (_id: string) => ({
      fragment: 'require ["fileinto"];',
      script: 'require ["fileinto"];',
    })),
  };
});

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc-1',
      },
    },
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../jmap/sync.svelte', () => ({ sync: { on: vi.fn() } }));

vi.mock('../jmap/client', () => ({
  jmap: {
    hasCapability: (_cap: string): boolean => true,
    batch: vi.fn(
      async (
        builder: (b: {
          call: (n: string, a: unknown, u: string[]) => { ref: (p: string) => unknown };
        }) => void,
      ): Promise<{ responses: unknown[]; sessionState: string }> => {
        harness.lastBatchCalls = [];
        builder({
          call: (n: string, a: unknown, _u: string[]) => {
            harness.lastBatchCalls.push({ name: n, args: a });
            return { ref: () => ({}) };
          },
        });
        return { responses: harness.mockResponses, sessionState: 's1' };
      },
    ),
  },
  strict: (responses: unknown[]): unknown[] => {
    for (const inv of responses as Array<[string, unknown, string]>) {
      if (inv[0] === 'error') throw new Error('jmap error');
    }
    return responses;
  },
}));

vi.mock('../api/tagged-addresses', () => ({
  convertFilterToSieve: harness.convertFilterToSieve,
  createDismissal: vi.fn(),
  deleteDismissal: vi.fn(),
  listDismissals: vi.fn(async () => ({ dismissals: [] })),
}));

import { TaggedAddressStore } from './tagged-address-store.svelte';

beforeEach(() => {
  harness.lastBatchCalls = [];
  harness.mockResponses = [];
  harness.convertFilterToSieve.mockReset();
  harness.convertFilterToSieve.mockImplementation(async () => ({
    fragment: 'require ["fileinto"];',
    script: 'require ["fileinto"];',
  }));
});

describe('TaggedAddressStore.updateFilter', () => {
  it('issues TaggedAddressFilter/set update and merges the server row into cache', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.mockResponses = [
      [
        'TaggedAddressFilter/set',
        {
          updated: {
            f1: {
              id: 'f1',
              baseIdentityId: 'A',
              suffix: 'amazon',
              action: 'label_archive',
              labelName: 'Shopping-2',
              createdAt: '2026-05-01T00:00:00Z',
              updatedAt: '2026-05-02T00:00:00Z',
            },
          },
        },
        'c0',
      ],
    ];

    const row = await store.updateFilter('f1', {
      action: 'label_archive',
      labelName: 'Shopping-2',
    });

    expect(row.action).toBe('label_archive');
    expect(row.labelName).toBe('Shopping-2');
    expect(harness.lastBatchCalls).toEqual([
      {
        name: 'TaggedAddressFilter/set',
        args: {
          accountId: 'acc-1',
          update: {
            f1: { action: 'label_archive', labelName: 'Shopping-2' },
          },
        },
      },
    ]);
    expect(store.filters[0]!.action).toBe('label_archive');
    expect(store.filters[0]!.labelName).toBe('Shopping-2');
  });

  it('throws when the server returns a notUpdated entry', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.mockResponses = [
      [
        'TaggedAddressFilter/set',
        {
          notUpdated: {
            f1: { type: 'invalidProperties', description: 'labelName too long' },
          },
        },
        'c0',
      ],
    ];

    await expect(
      store.updateFilter('f1', { labelName: 'x'.repeat(300) }),
    ).rejects.toThrow(/labelName too long/);
    // Cache should NOT have changed.
    expect(store.filters[0]!.labelName).toBe('Shopping');
  });

  it('falls back to a local merge when the server omits the updated row', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.mockResponses = [
      ['TaggedAddressFilter/set', { updated: { f1: null } }, 'c0'],
    ];
    const row = await store.updateFilter('f1', { action: 'label_archive_read' });
    expect(row.action).toBe('label_archive_read');
    expect(store.filters[0]!.action).toBe('label_archive_read');
  });
});

describe('TaggedAddressStore.deleteFilter', () => {
  it('issues TaggedAddressFilter/set destroy and removes the row from cache', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.mockResponses = [
      ['TaggedAddressFilter/set', { destroyed: ['f1'] }, 'c0'],
    ];

    await store.deleteFilter('f1');

    expect(harness.lastBatchCalls).toEqual([
      {
        name: 'TaggedAddressFilter/set',
        args: { accountId: 'acc-1', destroy: ['f1'] },
      },
    ]);
    expect(store.filters).toHaveLength(0);
  });

  it('throws when the server returns a notDestroyed entry', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.mockResponses = [
      [
        'TaggedAddressFilter/set',
        { notDestroyed: { f1: { type: 'notFound' } } },
        'c0',
      ],
    ];
    await expect(store.deleteFilter('f1')).rejects.toThrow(/notFound/);
    // Cache untouched.
    expect(store.filters).toHaveLength(1);
  });
});

describe('TaggedAddressStore.convertToSieve', () => {
  it('calls the REST endpoint, drops the row, and returns the response body', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.convertFilterToSieve.mockResolvedValueOnce({
      fragment: 'require ["fileinto"];\n',
      script: 'require ["fileinto"];\n',
    });

    const res = await store.convertToSieve('f1');

    expect(harness.convertFilterToSieve).toHaveBeenCalledWith('f1');
    expect(res.fragment).toContain('fileinto');
    expect(res.script).toContain('fileinto');
    expect(store.filters).toHaveLength(0);
  });

  it('does NOT mutate the cache when the REST call fails', async () => {
    const store = new TaggedAddressStore();
    store.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
    harness.convertFilterToSieve.mockRejectedValueOnce(new Error('500'));
    await expect(store.convertToSieve('f1')).rejects.toThrow(/500/);
    expect(store.filters).toHaveLength(1);
  });
});
