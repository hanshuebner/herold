/**
 * CategorySettings store unit tests — Wave 3.13.
 *
 * Tests the pure helpers and the store's action surface using vi.mock
 * for the JMAP client, auth, sync, and toast singletons.
 *
 * Coverage:
 *   1. Helper: categoryKeyword / emailCategory / emailMatchesTab
 *   2. Default categories render correctly when the server returns synthesis
 *   3. setCategories — optimistic update + server persistence
 *   4. Cannot remove Primary — server rejects; optimistic patch reverts
 *   5. recategorise — fires the RPC; recategorising flag is set
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  _internals_forTest,
  categorySettings,
  type Category,
} from './category-settings.svelte';
import type { Invocation } from '../jmap/types';

const { categoryKeyword, emailCategory, emailMatchesTab, DEFAULT_CATEGORIES } =
  _internals_forTest;

// ── JMAP client mock ──────────────────────────────────────────────────────

vi.mock('../jmap/client', () => {
  let batchImpl:
    | (() => Promise<{ responses: unknown[]; sessionState: string }>)
    | (() => { responses: unknown[]; sessionState: string })
    | null = null;

  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      const calls: unknown[] = [];
      builder({
        call: (_name: string, _args: unknown, _using: string[]) => {
          calls.push({ name: _name, args: _args });
          return {
            ref: (path: string) => ({
              resultOf: `c${calls.length - 1}`,
              name: _name,
              path,
            }),
          };
        },
      });
      if (batchImpl) return batchImpl();
      return { responses: [], sessionState: 'state-1' };
    }),
    hasCapability: vi.fn(() => true),
    downloadUrl: vi.fn(() => null),
  };

  return {
    jmap,
    strict: (responses: unknown[]) => {
      for (const r of responses) {
        if (Array.isArray(r) && r[0] === 'error') {
          throw new Error((r[1] as { description?: string }).description ?? 'method error');
        }
      }
      return responses;
    },
    __setBatchImpl: (impl: typeof batchImpl) => {
      batchImpl = impl;
    },
  };
});

// ── Auth mock ─────────────────────────────────────────────────────────────

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'account-1',
      },
      capabilities: {
        'urn:ietf:params:jmap:mail': {},
        'https://netzhansa.com/jmap/categorise': { bulkRecategoriseEnabled: true },
      },
      apiUrl: '/jmap',
      downloadUrl: '/jmap/download/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'test@example.com',
      accounts: {},
      state: 'sess-1',
    },
    principalId: 'principal-alice',
    errorMessage: null,
    needsStepUp: false,
  },
}));

// ── Sync mock ─────────────────────────────────────────────────────────────

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(),
    start: vi.fn(),
    stop: vi.fn(),
  },
}));

// ── Toast mock ────────────────────────────────────────────────────────────

vi.mock('../toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    undo: vi.fn(),
    current: null,
  },
}));

// ── Helper to get the mock module ─────────────────────────────────────────

async function getJmapMock() {
  return (await import('../jmap/client')) as unknown as {
    jmap: { batch: ReturnType<typeof vi.fn>; hasCapability: ReturnType<typeof vi.fn> };
    strict: (responses: unknown[]) => unknown[];
    __setBatchImpl: (
      impl:
        | (() => Promise<{ responses: unknown[]; sessionState: string }>)
        | (() => { responses: unknown[]; sessionState: string })
        | null,
    ) => void;
  };
}

async function getToastMock() {
  return (await import('../toast/toast.svelte')) as unknown as {
    toast: { show: ReturnType<typeof vi.fn> };
  };
}

// ─────────────────────────────────────────────────────────────────────────
// Test suite 1: pure helper functions
// ─────────────────────────────────────────────────────────────────────────

describe('categoryKeyword', () => {
  it('lowercases the name and prefixes $category-', () => {
    expect(categoryKeyword('Social')).toBe('$category-social');
    expect(categoryKeyword('Promotions')).toBe('$category-promotions');
    expect(categoryKeyword('Primary')).toBe('$category-primary');
  });

  it('replaces spaces with hyphens', () => {
    expect(categoryKeyword('My Category')).toBe('$category-my-category');
  });
});

describe('emailCategory', () => {
  const cats: Category[] = [
    { id: 'primary', name: 'Primary', order: 0 },
    { id: 'social', name: 'Social', order: 1 },
    { id: 'promotions', name: 'Promotions', order: 2 },
  ];

  it('returns null when no $category-* keyword is present', () => {
    expect(emailCategory({ $seen: true }, cats)).toBeNull();
  });

  it('returns the matching category name', () => {
    expect(emailCategory({ '$category-social': true }, cats)).toBe('Social');
    expect(emailCategory({ '$category-promotions': true }, cats)).toBe('Promotions');
  });

  it('returns null for an unknown $category-* keyword (outside the category set)', () => {
    expect(emailCategory({ '$category-unknown': true }, cats)).toBeNull();
  });
});

describe('emailMatchesTab', () => {
  const cats: Category[] = [
    { id: 'primary', name: 'Primary', order: 0 },
    { id: 'social', name: 'Social', order: 1 },
  ];

  it('Primary tab (null) matches emails with no category keyword', () => {
    expect(emailMatchesTab({ $seen: true }, null, cats)).toBe(true);
  });

  it('Primary tab does not match an email with a category keyword', () => {
    expect(emailMatchesTab({ '$category-social': true }, null, cats)).toBe(false);
  });

  it('named tab matches the correct category keyword', () => {
    expect(emailMatchesTab({ '$category-social': true }, 'Social', cats)).toBe(true);
  });

  it('named tab does not match a different category', () => {
    expect(emailMatchesTab({ '$category-social': true }, 'Promotions', cats)).toBe(false);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 2: store load — default synthesis
// ─────────────────────────────────────────────────────────────────────────

describe('categorySettings.load — default synthesis', () => {
  beforeEach(async () => {
    // Reset load state.
    (categorySettings as unknown as { loadStatus: string }).loadStatus = 'idle';
    (categorySettings as unknown as { loadError: null }).loadError = null;
  });

  it('populates the default category set when the server returns an empty list', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/get',
          {
            list: [],
            state: 'state-cat-1',
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await categorySettings.load(true);

    expect(categorySettings.loadStatus).toBe('ready');
    expect(categorySettings.categories).toHaveLength(DEFAULT_CATEGORIES.length);
    expect(categorySettings.categories[0]!.name).toBe('Primary');

    mock.__setBatchImpl(null);
  });

  it('loads and sorts categories from the server response', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/get',
          {
            list: [
              {
                id: 'singleton',
                categories: [
                  { id: 'social', name: 'Social', order: 1 },
                  { id: 'primary', name: 'Primary', order: 0 },
                  { id: 'promotions', name: 'Promotions', order: 2 },
                ],
                systemPrompt: 'Custom prompt',
                defaultPrompt: 'Default prompt',
                enabled: true,
              },
            ],
            state: 'state-cat-2',
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await categorySettings.load(true);

    expect(categorySettings.categories[0]!.name).toBe('Primary');
    expect(categorySettings.categories[1]!.name).toBe('Social');
    expect(categorySettings.categories[2]!.name).toBe('Promotions');
    expect(categorySettings.systemPrompt).toBe('Custom prompt');
    expect(categorySettings.defaultPrompt).toBe('Default prompt');

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 3: setCategories — rename persists and re-renders
// ─────────────────────────────────────────────────────────────────────────

describe('categorySettings.setCategories — rename persists', () => {
  beforeEach(async () => {
    (categorySettings as unknown as { loadStatus: string }).loadStatus = 'ready';
    (categorySettings as unknown as { categories: Category[] }).categories = [
      { id: 'primary', name: 'Primary', order: 0 },
      { id: 'social', name: 'Social', order: 1 },
    ];
  });

  it('applies the rename optimistically and keeps it after a successful set', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/set',
          { updated: { singleton: null }, notUpdated: null },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const renamed: Category[] = [
      { id: 'primary', name: 'Primary', order: 0 },
      { id: 'social', name: 'Networking', order: 1 },
    ];
    await categorySettings.setCategories(renamed);

    expect(categorySettings.categories[1]!.name).toBe('Networking');

    mock.__setBatchImpl(null);
  });

  it('reverts the optimistic rename when the server returns notUpdated', async () => {
    const toastMock = await getToastMock();
    toastMock.toast.show.mockClear();

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/set',
          {
            notUpdated: {
              singleton: { type: 'invalidProperties', description: 'Cannot remove Primary' },
            },
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const original = categorySettings.categories.map((c) => ({ ...c }));
    const bad: Category[] = [
      // Intentionally an empty list (no Primary) — server rejects this.
      { id: 'social', name: 'Social', order: 0 },
    ];
    await categorySettings.setCategories(bad);

    // The categories should have reverted to the original list.
    expect(categorySettings.categories).toHaveLength(original.length);
    expect(categorySettings.categories[0]!.name).toBe(original[0]!.name);

    // A toast error should have been surfaced.
    expect(toastMock.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 4: recategorise — fires the RPC and sets the flag
// ─────────────────────────────────────────────────────────────────────────

describe('categorySettings.recategorise', () => {
  beforeEach(() => {
    (categorySettings as unknown as { loadStatus: string }).loadStatus = 'ready';
    (categorySettings as unknown as { recategorising: boolean }).recategorising = false;
  });

  it('sets recategorising=true while the RPC is in flight', async () => {
    let resolveServer!: (v: { responses: unknown[]; sessionState: string }) => void;
    const serverPromise = new Promise<{ responses: unknown[]; sessionState: string }>(
      (res) => { resolveServer = res; },
    );

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => serverPromise);

    const recatPromise = categorySettings.recategorise('inbox-recent');

    // While the promise is pending the flag should be true.
    expect(categorySettings.recategorising).toBe(true);

    // Resolve the server response.
    resolveServer({
      responses: [
        [
          'CategorySettings/recategorise',
          { jobId: 'job-1', state: 'running' },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    });

    await recatPromise;
    // After success the flag stays true (cleared on state-change reload).
    // We only assert the RPC was issued.
    expect(mock.jmap.batch).toHaveBeenCalled();

    mock.__setBatchImpl(null);
  });

  it('clears recategorising on RPC error and shows a toast', async () => {
    const toastMock = await getToastMock();
    toastMock.toast.show.mockClear();

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        ['error', { type: 'serverFail', description: 'No LLM configured' }, 'c0'] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await categorySettings.recategorise('inbox-recent');

    expect(categorySettings.recategorising).toBe(false);
    expect(toastMock.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );

    mock.__setBatchImpl(null);
  });
});
