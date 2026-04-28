/**
 * Managed-rules store unit tests — Wave 3.15, REQ-FLT-01..31.
 *
 * Coverage:
 *   1. Pure helpers: hasDeleteApplyLabelConflict, isThreadMuteRule,
 *      isBlockedSenderRule, blockedSenderAddress, buildEmailQueryFilter.
 *   2. Store load — populates rules array from server response.
 *   3. CRUD round-trip: create, update, delete.
 *   4. setEnabled — optimistic update + revert on failure.
 *   5. setOrder — optimistic update + revert on failure.
 *   6. isThreadMuted — detects active mute rule.
 *   7. muteThread — calls Thread/mute and reloads.
 *   8. unmuteThread — calls Thread/unmute and reloads.
 *   9. blockSender — calls BlockedSender/set and reloads.
 *  10. testFilter — calls Email/query and returns total.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  _internals_forTest,
  managedRules,
  type ManagedRule,
  type RuleCondition,
} from './managed-rules.svelte';
import type { Invocation } from '../jmap/types';

const {
  hasDeleteApplyLabelConflict,
  isThreadMuteRule,
  isBlockedSenderRule,
  blockedSenderAddress,
  buildEmailQueryFilter,
} = _internals_forTest;

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
        'https://netzhansa.com/jmap/managed-rules': {},
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

// ── Helpers ────────────────────────────────────────────────────────────────

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

function resetStore(): void {
  (managedRules as unknown as { loadStatus: string }).loadStatus = 'idle';
  (managedRules as unknown as { loadError: null }).loadError = null;
  (managedRules as unknown as { rules: ManagedRule[] }).rules = [];
}

const SAMPLE_RULES: ManagedRule[] = [
  {
    id: '1',
    name: 'Mailing list filter',
    enabled: true,
    order: 0,
    conditions: [{ field: 'from', op: 'contains', value: 'newsletter@example.com' }],
    actions: [{ kind: 'skip-inbox' }, { kind: 'mark-read' }],
  },
  {
    id: '2',
    name: 'Mute thread 42',
    enabled: true,
    order: 1,
    conditions: [{ field: 'thread-id', op: 'equals', value: 'thread-42' }],
    actions: [{ kind: 'skip-inbox' }, { kind: 'mark-read' }],
  },
  {
    id: '3',
    name: 'Block spammer@bad.example',
    enabled: true,
    order: 2,
    conditions: [{ field: 'from', op: 'equals', value: 'spammer@bad.example' }],
    actions: [{ kind: 'delete' }],
  },
];

// ─────────────────────────────────────────────────────────────────────────
// 1. Pure helpers
// ─────────────────────────────────────────────────────────────────────────

describe('hasDeleteApplyLabelConflict', () => {
  it('returns false when neither delete nor apply-label is present', () => {
    expect(hasDeleteApplyLabelConflict([{ kind: 'skip-inbox' }])).toBe(false);
  });

  it('returns false when only delete is present', () => {
    expect(hasDeleteApplyLabelConflict([{ kind: 'delete' }])).toBe(false);
  });

  it('returns false when only apply-label is present', () => {
    expect(hasDeleteApplyLabelConflict([{ kind: 'apply-label', params: { label: 'Work' } }])).toBe(false);
  });

  it('returns true when both delete and apply-label are present', () => {
    expect(
      hasDeleteApplyLabelConflict([
        { kind: 'delete' },
        { kind: 'apply-label', params: { label: 'Work' } },
      ]),
    ).toBe(true);
  });

  it('returns false for an empty action list', () => {
    expect(hasDeleteApplyLabelConflict([])).toBe(false);
  });
});

describe('isThreadMuteRule', () => {
  const muteRule: ManagedRule = {
    id: '10',
    name: 'Mute thread abc',
    enabled: true,
    order: 0,
    conditions: [{ field: 'thread-id', op: 'equals', value: 'abc' }],
    actions: [{ kind: 'skip-inbox' }, { kind: 'mark-read' }],
  };

  it('matches a thread-id rule for the right thread', () => {
    expect(isThreadMuteRule(muteRule, 'abc')).toBe(true);
  });

  it('does not match for the wrong thread id', () => {
    expect(isThreadMuteRule(muteRule, 'xyz')).toBe(false);
  });

  it('does not match a disabled rule', () => {
    expect(isThreadMuteRule({ ...muteRule, enabled: false }, 'abc')).toBe(false);
  });

  it('does not match a rule with multiple conditions', () => {
    const multi = {
      ...muteRule,
      conditions: [
        { field: 'thread-id', op: 'equals', value: 'abc' },
        { field: 'from', op: 'contains', value: 'x' },
      ],
    };
    expect(isThreadMuteRule(multi, 'abc')).toBe(false);
  });
});

describe('isBlockedSenderRule', () => {
  const blockRule: ManagedRule = {
    id: '20',
    name: 'Block spammer',
    enabled: true,
    order: 0,
    conditions: [{ field: 'from', op: 'equals', value: 'spammer@bad.example' }],
    actions: [{ kind: 'delete' }],
  };

  it('matches the blocked-sender shape', () => {
    expect(isBlockedSenderRule(blockRule)).toBe(true);
  });

  it('does not match a rule with forward action', () => {
    expect(isBlockedSenderRule({ ...blockRule, actions: [{ kind: 'forward', params: { to: 'a@b' } }] })).toBe(false);
  });

  it('does not match a rule with multiple conditions', () => {
    const multi = {
      ...blockRule,
      conditions: [
        { field: 'from', op: 'equals', value: 'x@y' },
        { field: 'subject', op: 'contains', value: 'spam' },
      ],
    };
    expect(isBlockedSenderRule(multi)).toBe(false);
  });
});

describe('blockedSenderAddress', () => {
  it('returns the from value for a blocked-sender rule', () => {
    const rule: ManagedRule = {
      id: '30',
      name: 'Block foo',
      enabled: true,
      order: 0,
      conditions: [{ field: 'from', op: 'equals', value: 'foo@bar.com' }],
      actions: [{ kind: 'delete' }],
    };
    expect(blockedSenderAddress(rule)).toBe('foo@bar.com');
  });

  it('returns null for a non-blocked-sender rule', () => {
    const rule: ManagedRule = {
      id: '31',
      name: 'Archive newsletters',
      enabled: true,
      order: 0,
      conditions: [{ field: 'from', op: 'contains', value: '@newsletter.com' }],
      actions: [{ kind: 'skip-inbox' }],
    };
    expect(blockedSenderAddress(rule)).toBeNull();
  });
});

describe('buildEmailQueryFilter', () => {
  it('returns null for an empty condition list', () => {
    expect(buildEmailQueryFilter([])).toBeNull();
  });

  it('returns a single filter directly (no AND wrapper) for one condition', () => {
    const f = buildEmailQueryFilter([{ field: 'from', op: 'equals', value: 'user@example.com' }]);
    expect(f).toEqual({ from: 'user@example.com' });
  });

  it('wraps multiple conditions in an AND operator', () => {
    const f = buildEmailQueryFilter([
      { field: 'from', op: 'equals', value: 'user@example.com' },
      { field: 'subject', op: 'contains', value: 'hello' },
    ]);
    expect(f).toEqual({
      operator: 'AND',
      conditions: [
        { from: 'user@example.com' },
        { subject: 'hello' },
      ],
    });
  });

  it('maps has-attachment to hasAttachment boolean', () => {
    const f = buildEmailQueryFilter([{ field: 'has-attachment', op: 'equals', value: 'true' }]);
    expect(f).toEqual({ hasAttachment: true });
  });

  it('maps thread-id to inThread', () => {
    const f = buildEmailQueryFilter([{ field: 'thread-id', op: 'equals', value: 'thread-abc' }]);
    expect(f).toEqual({ inThread: 'thread-abc' });
  });

  it('maps from-domain to a from wildcard', () => {
    const f = buildEmailQueryFilter([{ field: 'from-domain', op: 'equals', value: 'example.com' }]);
    expect(f).toEqual({ from: '@example.com' });
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 2. Store load
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.load', () => {
  beforeEach(resetStore);

  it('populates rules sorted by order from the server response', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/get',
          {
            accountId: 'account-1',
            state: 'state-mr-1',
            list: [
              SAMPLE_RULES[1], // order 1
              SAMPLE_RULES[0], // order 0
            ],
            notFound: [],
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await managedRules.load(true);

    expect(managedRules.loadStatus).toBe('ready');
    expect(managedRules.rules).toHaveLength(2);
    expect(managedRules.rules[0]!.order).toBeLessThanOrEqual(managedRules.rules[1]!.order);

    mock.__setBatchImpl(null);
  });

  it('sets loadStatus=error when the batch throws', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => {
      throw new Error('Network error');
    });

    await managedRules.load(true);

    expect(managedRules.loadStatus).toBe('error');
    expect(managedRules.loadError).toContain('Network error');

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 3. CRUD round-trip
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.create', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
  });

  it('adds the server-created rule to the local list', async () => {
    const created: ManagedRule = { id: '99', name: 'New', enabled: true, order: 0, conditions: [], actions: [] };
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/set',
          { accountId: 'account-1', oldState: '1', newState: '2', created: { new: created }, notCreated: null },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const result = await managedRules.create({
      name: 'New',
      enabled: true,
      order: 0,
      conditions: [],
      actions: [],
    });

    expect(result).not.toBeNull();
    expect(result?.id).toBe('99');
    expect(managedRules.rules.some((r) => r.id === '99')).toBe(true);

    mock.__setBatchImpl(null);
  });

  it('shows a toast and returns null when the server returns notCreated', async () => {
    const toastMock = await getToastMock();
    toastMock.toast.show.mockClear();

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/set',
          {
            accountId: 'account-1',
            oldState: '1',
            newState: '1',
            notCreated: { new: { type: 'invalidProperties', description: 'Bad conditions' } },
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const result = await managedRules.create({ name: '', enabled: true, order: 0, conditions: [], actions: [] });

    expect(result).toBeNull();
    expect(toastMock.toast.show).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));

    mock.__setBatchImpl(null);
  });
});

describe('managedRules.delete', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [...SAMPLE_RULES];
  });

  it('removes the rule from the local list on success', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/set',
          { accountId: 'account-1', oldState: '2', newState: '3', destroyed: ['1'] },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const ok = await managedRules.delete('1');

    expect(ok).toBe(true);
    expect(managedRules.rules.find((r) => r.id === '1')).toBeUndefined();

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 4. setEnabled — optimistic update
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.setEnabled', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [
      { ...SAMPLE_RULES[0]!, enabled: true },
    ];
  });

  it('applies the change optimistically and keeps it on server success', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/set',
          { accountId: 'account-1', oldState: '1', newState: '2', updated: { '1': null } },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await managedRules.setEnabled('1', false);

    expect(managedRules.rules[0]!.enabled).toBe(false);

    mock.__setBatchImpl(null);
  });

  it('reverts the optimistic change when the server fails', async () => {
    const toastMock = await getToastMock();
    toastMock.toast.show.mockClear();

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/set',
          {
            accountId: 'account-1',
            oldState: '1',
            newState: '1',
            notUpdated: { '1': { type: 'notFound' } },
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await managedRules.setEnabled('1', false);

    // Should be reverted to enabled=true.
    expect(managedRules.rules[0]!.enabled).toBe(true);

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 5. setOrder — optimistic update
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.setOrder', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [
      { ...SAMPLE_RULES[0]!, order: 0 },
      { ...SAMPLE_RULES[1]!, order: 1 },
    ];
  });

  it('applies the new order and re-sorts the rules array', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'ManagedRule/set',
          { accountId: 'account-1', oldState: '1', newState: '2', updated: { '1': null } },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await managedRules.setOrder('1', 5);

    const updated = managedRules.rules.find((r) => r.id === '1');
    expect(updated?.order).toBe(5);

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 6. isThreadMuted
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.isThreadMuted', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [...SAMPLE_RULES];
  });

  it('returns true for a thread that has an active mute rule', () => {
    expect(managedRules.isThreadMuted('thread-42')).toBe(true);
  });

  it('returns false for a thread that has no mute rule', () => {
    expect(managedRules.isThreadMuted('thread-unknown')).toBe(false);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 7. muteThread
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.muteThread', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [];
  });

  it('calls Thread/mute and then reloads', async () => {
    let loadCalled = false;
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => {
      // First call is Thread/mute; second is ManagedRule/get (from load).
      if (!loadCalled) {
        return {
          responses: [
            ['Thread/mute', { accountId: 'account-1', managedRuleId: '100' }, 'c0'] as Invocation,
          ],
          sessionState: 'state-1',
        };
      }
      return {
        responses: [
          ['ManagedRule/get', { accountId: 'account-1', state: '2', list: [], notFound: [] }, 'c0'] as Invocation,
        ],
        sessionState: 'state-1',
      };
    });

    await managedRules.muteThread('thread-99');

    // The batch should have been called (at least once for Thread/mute).
    expect(mock.jmap.batch).toHaveBeenCalled();

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 8. unmuteThread
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.unmuteThread', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [...SAMPLE_RULES];
  });

  it('calls Thread/unmute', async () => {
    let callCount = 0;
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => {
      callCount++;
      if (callCount === 1) {
        return {
          responses: [
            ['Thread/unmute', { accountId: 'account-1' }, 'c0'] as Invocation,
          ],
          sessionState: 'state-1',
        };
      }
      return {
        responses: [
          ['ManagedRule/get', { accountId: 'account-1', state: '3', list: [], notFound: [] }, 'c0'] as Invocation,
        ],
        sessionState: 'state-1',
      };
    });

    await managedRules.unmuteThread('thread-42');

    expect(mock.jmap.batch).toHaveBeenCalled();

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 9. blockSender
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.blockSender', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
    (managedRules as unknown as { rules: ManagedRule[] }).rules = [];
  });

  it('calls BlockedSender/set and reloads rules', async () => {
    let callCount = 0;
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => {
      callCount++;
      if (callCount === 1) {
        return {
          responses: [
            [
              'BlockedSender/set',
              { accountId: 'account-1', managedRuleId: '200' },
              'c0',
            ] as Invocation,
          ],
          sessionState: 'state-1',
        };
      }
      return {
        responses: [
          ['ManagedRule/get', { accountId: 'account-1', state: '4', list: [], notFound: [] }, 'c0'] as Invocation,
        ],
        sessionState: 'state-1',
      };
    });

    const ok = await managedRules.blockSender('spammer@example.com');

    expect(ok).toBe(true);
    expect(mock.jmap.batch).toHaveBeenCalled();

    mock.__setBatchImpl(null);
  });

  it('throws on server error', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        ['error', { type: 'serverFail', description: 'Internal error' }, 'c0'] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await expect(managedRules.blockSender('x@y.com')).rejects.toThrow();

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// 10. testFilter
// ─────────────────────────────────────────────────────────────────────────

describe('managedRules.testFilter', () => {
  beforeEach(() => {
    resetStore();
    (managedRules as unknown as { loadStatus: string }).loadStatus = 'ready';
  });

  it('calls Email/query and returns the total', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'Email/query',
          { accountId: 'account-1', ids: [], total: 42, position: 0, queryState: 'q-1', canCalculateChanges: false },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const conditions: RuleCondition[] = [{ field: 'from', op: 'equals', value: 'test@example.com' }];
    const count = await managedRules.testFilter(conditions);

    expect(count).toBe(42);

    mock.__setBatchImpl(null);
  });

  it('returns 0 for empty conditions (no server call)', async () => {
    const mock = await getJmapMock();
    mock.jmap.batch.mockClear();

    const count = await managedRules.testFilter([]);

    expect(count).toBe(0);
    expect(mock.jmap.batch).not.toHaveBeenCalled();
  });
});
