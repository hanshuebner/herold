/**
 * IMAPImportHandle excludedFolders round-trip tests (re #305).
 *
 * Verifies that:
 *   - create() forwards excludedFolders in the IMAPImport/set create call
 *     and the created account's excludedFolders is stored in the handle.
 *   - update() forwards excludedFolders in the IMAPImport/set update call.
 *   - IMAPImport/get responses without an excludedFolders property (the
 *     wire form omits it when empty) surface as account.excludedFolders
 *     being undefined, not a thrown error.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { IMAPImportAccount } from './imap-import';

// ── JMAP client mock ────────────────────────────────────────────────────────

interface RecordedCall {
  name: string;
  args: unknown;
}

type BatchHandler = (
  call: RecordedCall,
) => { responses: unknown[]; sessionState: string };

vi.mock('./client', () => {
  let handler: BatchHandler | null = null;
  const allCalls: RecordedCall[] = [];

  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      const calls: RecordedCall[] = [];
      builder({
        call: (name: string, args: unknown) => {
          calls.push({ name, args });
          return { ref: () => ({}) };
        },
      });
      allCalls.push(...calls);
      const last = calls[calls.length - 1];
      if (!handler || !last) return { responses: [], sessionState: 'state-1' };
      return handler(last);
    }),
  };

  return {
    jmap,
    strict: (responses: unknown[]) => responses,
    __setBatchHandler: (impl: BatchHandler) => {
      handler = impl;
    },
    __allCalls: allCalls,
    __resetCalls: () => {
      allCalls.length = 0;
    },
  };
});

import { imapImportStore } from './imap-import-store.svelte';

interface ClientMock {
  __setBatchHandler: (impl: BatchHandler) => void;
  __allCalls: RecordedCall[];
  __resetCalls: () => void;
}

async function getClientMock(): Promise<ClientMock> {
  return (await import('./client')) as unknown as ClientMock;
}

function lastCall(clientMock: ClientMock): RecordedCall {
  const calls = clientMock.__allCalls;
  const last = calls[calls.length - 1];
  if (!last) throw new Error('no batch call recorded');
  return last;
}

function callByName(clientMock: ClientMock, name: string): RecordedCall {
  const found = [...clientMock.__allCalls].reverse().find((c) => c.name === name);
  if (!found) throw new Error(`no ${name} call recorded`);
  return found;
}

const baseAccount: IMAPImportAccount = {
  id: 'acc1',
  identityId: 'id1',
  accountName: 'Test',
  host: 'imap.example.com',
  port: 993,
  tlsMode: 'implicit',
  username: 'user@example.com',
  authMethod: 'app_password',
  backfillHorizon: '90d',
  state: 'enabled',
  lastSuccessAt: null,
  lastError: '',
  deletePropagates: true,
  hasCredential: true,
};

describe('IMAPImportHandle excludedFolders (re #305)', () => {
  let clientMock: ClientMock;

  beforeEach(async () => {
    vi.clearAllMocks();
    clientMock = await getClientMock();
    clientMock.__resetCalls();
    // Each test starts from a clean cache entry so the module-level store
    // singleton does not carry status/account state across tests.
    imapImportStore.evict('id1');
  });

  it('forwards excludedFolders on create and stores the echoed value', async () => {
    clientMock.__setBatchHandler((call) => ({
      responses: [
        [
          call.name,
          {
            accountId: 'acct1',
            newState: 's2',
            created: {
              new1: { ...baseAccount, excludedFolders: ['Spam', 'Promo'] },
            },
          },
        ],
      ],
      sessionState: 'state-2',
    }));

    const handle = imapImportStore.forIdentity('id1', 'acct1');
    const created = await handle.create({
      accountName: 'Test',
      host: 'imap.example.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'user@example.com',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      credential: 'secret',
      excludedFolders: ['Spam', 'Promo'],
    });

    const { name, args } = lastCall(clientMock);
    expect(name).toBe('IMAPImport/set');
    const createArgs = (args as { create: Record<string, unknown> }).create.new1 as {
      excludedFolders?: string[];
    };
    expect(createArgs.excludedFolders).toEqual(['Spam', 'Promo']);
    expect(created.excludedFolders).toEqual(['Spam', 'Promo']);
    expect(handle.account?.excludedFolders).toEqual(['Spam', 'Promo']);
  });

  it('forwards excludedFolders on update, including an explicit empty array', async () => {
    // update() issues an IMAPImport/set call, then refresh()es via
    // IMAPImport/get; respond to each by call name so both legs resolve.
    clientMock.__setBatchHandler((call) => {
      if (call.name === 'IMAPImport/set') {
        return {
          responses: [
            [
              'IMAPImport/set',
              {
                accountId: 'acct1',
                newState: 's3',
                updated: { acc1: { ...baseAccount, excludedFolders: [] } },
              },
            ],
          ],
          sessionState: 'state-3',
        };
      }
      return {
        responses: [
          [
            'IMAPImport/get',
            {
              accountId: 'acct1',
              state: 's3',
              list: [{ ...baseAccount, excludedFolders: [] }],
              notFound: [],
            },
          ],
        ],
        sessionState: 'state-3',
      };
    });

    const handle = imapImportStore.forIdentity('id1', 'acct1');
    const updated = await handle.update('acc1', { excludedFolders: [] });

    const setCall = callByName(clientMock, 'IMAPImport/set');
    const updateArgs = (setCall.args as { update: Record<string, unknown> }).update
      .acc1 as { excludedFolders?: string[] };
    expect(updateArgs.excludedFolders).toEqual([]);
    expect(updated.excludedFolders).toEqual([]);
    expect(handle.account?.excludedFolders).toEqual([]);
  });

  it('surfaces an absent excludedFolders wire property as undefined (omitted-when-empty)', async () => {
    clientMock.__setBatchHandler(() => ({
      responses: [
        [
          'IMAPImport/get',
          {
            accountId: 'acct1',
            state: 's1',
            list: [{ ...baseAccount }], // no excludedFolders key at all
            notFound: [],
          },
        ],
      ],
      sessionState: 'state-1',
    }));

    const handle = imapImportStore.forIdentity('id1', 'acct1');
    await handle.load();
    expect(handle.status).toBe('ready');
    expect(handle.account?.excludedFolders).toBeUndefined();
  });
});
