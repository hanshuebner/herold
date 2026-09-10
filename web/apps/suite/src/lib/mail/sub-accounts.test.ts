/**
 * Tests for the sub-accounts store (issue #212, REQ-MAIL-SUB-01..09).
 *
 * Covers: discovering sub-accounts from the session's `accounts` map
 * minus `primaryAccounts` (the "merge" this store performs), capability
 * gating (empty list when the capability is absent), and that its
 * EventSource handlers key strictly on accountId -- a push for an
 * account this store has not discovered is a no-op, and a push for a
 * known sub-account refreshes only that entry.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Identity, Mailbox } from './types';

type SyncHandler = (newState: string, accountId: string) => void;

const syncHandlers: Record<string, SyncHandler[]> = {};

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn((type: string, handler: SyncHandler) => {
      (syncHandlers[type] ??= []).push(handler);
      return () => {
        syncHandlers[type] = (syncHandlers[type] ?? []).filter((h) => h !== handler);
      };
    }),
  },
}));

const hasCapability = vi.fn(() => true);
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), hasCapability },
  strict: (r: unknown[]) => r,
}));

let mockSession: {
  capabilities: Record<string, unknown>;
  primaryAccounts: Record<string, string>;
  accounts: Record<string, { name: string; isPersonal: boolean; isReadOnly: boolean; accountCapabilities: Record<string, unknown> }>;
} | null = null;

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    get session() {
      return mockSession;
    },
  },
  registerAccountResetCallback: vi.fn(),
}));

function makeIdentity(id: string, email: string): Identity {
  return {
    id,
    name: email,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
  };
}

function makeMailbox(id: string, role: string | null, unreadThreads = 0): Mailbox {
  return {
    id,
    name: role ?? id,
    role,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads,
  };
}

const { subAccounts } = await import('./sub-accounts.svelte');
const { jmap } = await import('../jmap/client');

describe('subAccounts.refresh', () => {
  beforeEach(() => {
    vi.mocked(jmap.batch).mockReset();
    hasCapability.mockReturnValue(true);
    subAccounts.reset();
    mockSession = {
      capabilities: {},
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-primary' },
      accounts: {
        'acct-primary': { name: 'alice@example.com', isPersonal: true, isReadOnly: false, accountCapabilities: {} },
        'acct-sub-1': { name: 'club@example.com', isPersonal: true, isReadOnly: false, accountCapabilities: {} },
      },
    };
  });

  it('discovers sub-accounts as session.accounts minus session.primaryAccounts', async () => {
    vi.mocked(jmap.batch).mockImplementation(async (fn) => {
      fn({ call: () => ({ ref: () => ({}) }) } as never);
      return {
        responses: [
          ['Identity/get', { list: [makeIdentity('99', 'club@example.com')] }, 'c0'],
          ['Mailbox/get', { list: [makeMailbox('mb-1', 'inbox', 3)] }, 'c1'],
        ],
        sessionState: 's1',
      };
    });

    await subAccounts.refresh();

    expect(subAccounts.list).toHaveLength(1);
    expect(subAccounts.list[0]?.accountId).toBe('acct-sub-1');
    expect(subAccounts.list[0]?.name).toBe('club@example.com');
    expect(subAccounts.list[0]?.identity?.id).toBe('99');
    expect(subAccounts.list[0]?.unreadThreads).toBe(3);
  });

  it('returns an empty list when the sub-accounts capability is absent', async () => {
    hasCapability.mockReturnValue(false);
    await subAccounts.refresh();
    expect(subAccounts.list).toHaveLength(0);
    expect(jmap.batch).not.toHaveBeenCalled();
  });
});

describe('subAccounts EventSource handling', () => {
  beforeEach(() => {
    vi.mocked(jmap.batch).mockReset();
    hasCapability.mockReturnValue(true);
    subAccounts.reset();
    mockSession = {
      capabilities: {},
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-primary' },
      accounts: {
        'acct-primary': { name: 'alice@example.com', isPersonal: true, isReadOnly: false, accountCapabilities: {} },
        'acct-sub-1': { name: 'club@example.com', isPersonal: true, isReadOnly: false, accountCapabilities: {} },
      },
    };
  });

  it('is inert for a push against an account it has not discovered (e.g. the caller\'s own primary account)', () => {
    expect(subAccounts.list).toHaveLength(0);
    for (const handler of syncHandlers['Mailbox'] ?? []) {
      handler('new-state', 'acct-primary');
    }
    // No batch call was made for the unknown/primary accountId.
    expect(jmap.batch).not.toHaveBeenCalled();
  });

  it('refreshes only the pushed sub-account, keyed by accountId', async () => {
    vi.mocked(jmap.batch).mockImplementation(async (fn) => {
      fn({ call: () => ({ ref: () => ({}) }) } as never);
      return {
        responses: [
          ['Identity/get', { list: [makeIdentity('99', 'club@example.com')] }, 'c0'],
          ['Mailbox/get', { list: [makeMailbox('mb-1', 'inbox', 1)] }, 'c1'],
        ],
        sessionState: 's1',
      };
    });
    await subAccounts.refresh();
    expect(subAccounts.list[0]?.unreadThreads).toBe(1);

    vi.mocked(jmap.batch).mockImplementation(async (fn) => {
      fn({ call: () => ({ ref: () => ({}) }) } as never);
      return {
        responses: [
          ['Identity/get', { list: [makeIdentity('99', 'club@example.com')] }, 'c0'],
          ['Mailbox/get', { list: [makeMailbox('mb-1', 'inbox', 5)] }, 'c1'],
        ],
        sessionState: 's2',
      };
    });

    const handlers = syncHandlers['Mailbox'] ?? [];
    expect(handlers.length).toBeGreaterThan(0);
    for (const handler of handlers) handler('new-state-2', 'acct-sub-1');
    // refreshOne is fire-and-forget; wait a tick for the promise to settle.
    await new Promise((r) => setTimeout(r, 0));

    expect(subAccounts.list[0]?.unreadThreads).toBe(5);
  });
});

describe('subAccounts.removeSeparation', () => {
  beforeEach(() => {
    vi.mocked(jmap.batch).mockReset();
    hasCapability.mockReturnValue(true);
    subAccounts.reset();
    mockSession = {
      capabilities: {},
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-primary' },
      accounts: {
        'acct-primary': { name: 'alice@example.com', isPersonal: true, isReadOnly: false, accountCapabilities: {} },
      },
    };
  });

  it('addresses Identity/set via the sub-account\'s own accountId and refreshes on success', async () => {
    let sentArgs: unknown = null;
    vi.mocked(jmap.batch).mockImplementationOnce(async (fn) => {
      fn({
        call: (name: string, args: unknown) => {
          sentArgs = args;
          return { ref: () => ({}) };
        },
      } as never);
      return {
        responses: [['Identity/set', { updated: { '99': { subAccountId: null } } }, 'c0']],
        sessionState: 's1',
      };
    });
    // refresh() call inside removeSeparation, with no more sub-accounts left.
    vi.mocked(jmap.batch).mockImplementationOnce(async () => ({
      responses: [],
      sessionState: 's2',
    }));

    await subAccounts.removeSeparation('acct-sub-1', '99', true);

    expect(sentArgs).toEqual({
      accountId: 'acct-sub-1',
      update: { '99': { separated: false, keepMail: true } },
    });
    expect(subAccounts.list).toHaveLength(0);
  });

  it('throws on notUpdated without refreshing', async () => {
    vi.mocked(jmap.batch).mockImplementationOnce(async (fn) => {
      fn({ call: () => ({ ref: () => ({}) }) } as never);
      return {
        responses: [
          ['Identity/set', { notUpdated: { '99': { type: 'forbidden', description: 'nope' } } }, 'c0'],
        ],
        sessionState: 's1',
      };
    });

    await expect(subAccounts.removeSeparation('acct-sub-1', '99', true)).rejects.toThrow('nope');
    expect(jmap.batch).toHaveBeenCalledTimes(1);
  });
});
