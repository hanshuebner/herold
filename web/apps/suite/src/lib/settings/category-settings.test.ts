/**
 * CategorySettings store unit tests (revised 2026-04-28, REQ-CAT-40/41).
 *
 * Tests the pure helpers and the store's action surface using vi.mock
 * for the JMAP client, auth, sync, and toast singletons.
 *
 * Coverage:
 *   1. Helper: categoryKeyword / emailCategory / emailMatchesTab
 *   2. Default state when the server returns an empty list
 *   3. Load: derivedCategories populated from the server response
 *   4. setSystemPrompt -- optimistic update + server persistence
 *   5. reset -- clears derivedCategories, reverts prompt on failure
 *   6. recategorise -- fires the RPC; recategorising flag is set
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  _internals_forTest,
  categorySettings,
} from './category-settings.svelte';
import type { Invocation } from '../jmap/types';

const { categoryKeyword, emailCategory, emailMatchesTab, DEFAULT_PROMPT } =
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
  const cats = ['Primary', 'Social', 'Promotions'];

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
  const cats = ['Primary', 'Social'];

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

describe('DEFAULT_PROMPT', () => {
  it('includes the five Gmail-style category names', () => {
    expect(DEFAULT_PROMPT).toContain('Primary');
    expect(DEFAULT_PROMPT).toContain('Social');
    expect(DEFAULT_PROMPT).toContain('Promotions');
    expect(DEFAULT_PROMPT).toContain('Updates');
    expect(DEFAULT_PROMPT).toContain('Forums');
  });

  it('instructs the LLM to return JSON with categories and assigned fields', () => {
    expect(DEFAULT_PROMPT).toContain('"categories"');
    expect(DEFAULT_PROMPT).toContain('"assigned"');
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 2: store load
// ─────────────────────────────────────────────────────────────────────────

describe('categorySettings.load -- empty list synthesis', () => {
  beforeEach(async () => {
    (categorySettings as unknown as { loadStatus: string }).loadStatus = 'idle';
    (categorySettings as unknown as { loadError: null }).loadError = null;
  });

  it('sets derivedCategories to [] when the server returns an empty list', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/get',
          { list: [], state: 'state-cat-1' },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await categorySettings.load(true);

    expect(categorySettings.loadStatus).toBe('ready');
    expect(categorySettings.derivedCategories).toHaveLength(0);

    mock.__setBatchImpl(null);
  });

  it('populates derivedCategories from the server response', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/get',
          {
            list: [
              {
                id: 'singleton',
                derivedCategories: ['Primary', 'Social', 'Promotions'],
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

    expect(categorySettings.derivedCategories).toEqual(['Primary', 'Social', 'Promotions']);
    expect(categorySettings.systemPrompt).toBe('Custom prompt');
    expect(categorySettings.defaultPrompt).toBe('Default prompt');

    mock.__setBatchImpl(null);
  });

  it('handles a missing derivedCategories field gracefully', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/get',
          {
            list: [
              {
                id: 'singleton',
                systemPrompt: 'Some prompt',
              },
            ],
            state: 'state-cat-3',
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await categorySettings.load(true);

    expect(categorySettings.derivedCategories).toEqual([]);

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 3: setSystemPrompt
// ─────────────────────────────────────────────────────────────────────────

describe('categorySettings.setSystemPrompt', () => {
  beforeEach(() => {
    (categorySettings as unknown as { loadStatus: string }).loadStatus = 'ready';
    (categorySettings as unknown as { systemPrompt: string }).systemPrompt = 'Old prompt';
  });

  it('applies the prompt optimistically and keeps it after a successful set', async () => {
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

    await categorySettings.setSystemPrompt('New prompt');

    expect(categorySettings.systemPrompt).toBe('New prompt');

    mock.__setBatchImpl(null);
  });

  it('reverts the optimistic prompt when the server returns notUpdated', async () => {
    const toastMock = await getToastMock();
    toastMock.toast.show.mockClear();

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'CategorySettings/set',
          {
            notUpdated: {
              singleton: { type: 'invalidProperties', description: 'Prompt too long' },
            },
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await categorySettings.setSystemPrompt('Bad prompt');

    // Should have reverted to the original.
    expect(categorySettings.systemPrompt).toBe('Old prompt');
    expect(toastMock.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 4: reset
// ─────────────────────────────────────────────────────────────────────────

describe('categorySettings.reset', () => {
  beforeEach(() => {
    (categorySettings as unknown as { loadStatus: string }).loadStatus = 'ready';
    (categorySettings as unknown as { systemPrompt: string }).systemPrompt = 'Custom prompt';
    (categorySettings as unknown as { defaultPrompt: string }).defaultPrompt = 'Default prompt';
    (categorySettings as unknown as { derivedCategories: string[] }).derivedCategories = [
      'Primary',
      'Social',
    ];
  });

  it('clears derivedCategories immediately and resets the prompt', async () => {
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

    await categorySettings.reset();

    // derivedCategories must be cleared (server will refill after next classifier call).
    expect(categorySettings.derivedCategories).toEqual([]);
    // systemPrompt should now be the default.
    expect(categorySettings.systemPrompt).toBe('Default prompt');

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 5: recategorise
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
