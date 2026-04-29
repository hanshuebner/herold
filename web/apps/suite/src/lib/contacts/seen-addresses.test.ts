/**
 * Tests for the SeenAddresses store.
 *
 * Covers:
 *   1. load() happy path -- populates entries and transitions to 'ready'
 *   2. load() idempotent -- second call when 'ready' is a no-op
 *   3. load() idempotent -- call during 'loading' is a no-op
 *   4. load() error path -- status transitions to 'error', entries empty
 *   5. destroy() -- removes by id; issues SeenAddress/set destroy
 *   6. clear() -- empties entries and resets status to 'idle'
 *   7. sync handler -- triggers a reload on SeenAddress state change
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { seenAddresses, type SeenAddress } from './seen-addresses.svelte';

// ── JMAP client mock ──────────────────────────────────────────────────────

vi.mock('../jmap/client', () => {
  let batchImpl:
    | (() => Promise<{ responses: unknown[]; sessionState: string }>)
    | null = null;

  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      const calls: unknown[] = [];
      builder({
        call: (_name: string, _args: unknown, _using: string[]) => {
          calls.push({ name: _name, args: _args });
          return { ref: (path: string) => ({ resultOf: `c${calls.length - 1}`, name: _name, path }) };
        },
      });
      if (batchImpl) return batchImpl();
      return { responses: [], sessionState: 'state-1' };
    }),
    hasCapability: vi.fn(() => true),
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
    __clearBatchImpl: () => {
      batchImpl = null;
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
      },
      apiUrl: '/jmap',
      downloadUrl: '/jmap/download/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'test@example.com',
      accounts: {},
      state: 'sess-1',
    },
    principalId: '42',
    errorMessage: null,
  },
}));

// ── Sync mock ─────────────────────────────────────────────────────────────
// Keep the handler registry module-level but initialise it in a way that
// doesn't cause a hoisting issue with vi.mock.

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(),
    start: vi.fn(),
    stop: vi.fn(),
  },
}));

// ── Helpers ───────────────────────────────────────────────────────────────

async function getJmapMock() {
  return (await import('../jmap/client')) as unknown as {
    jmap: { batch: ReturnType<typeof vi.fn> };
    __setBatchImpl: (impl: (() => Promise<{ responses: unknown[]; sessionState: string }>) | null) => void;
    __clearBatchImpl: () => void;
  };
}

/** Return all handlers registered with sync.on for the given type. */
async function getSyncHandlers(type: string): Promise<((newState: string) => void)[]> {
  const syncMod = await import('../jmap/sync.svelte');
  const onMock = vi.mocked(syncMod.sync.on);
  return onMock.mock.calls
    .filter(([t]) => t === type)
    .map(([, handler]) => handler as (newState: string) => void);
}

const ALICE: SeenAddress = {
  id: 'sa-1',
  email: 'alice@example.com',
  displayName: 'Alice',
  firstSeenAt: '2026-01-01T00:00:00Z',
  lastUsedAt: '2026-04-01T12:00:00Z',
  sendCount: 5,
  receivedCount: 3,
};

const BOB: SeenAddress = {
  id: 'sa-2',
  email: 'bob@example.com',
  displayName: 'Bob',
  firstSeenAt: '2026-02-01T00:00:00Z',
  lastUsedAt: '2026-04-10T08:00:00Z',
  sendCount: 1,
  receivedCount: 0,
};

function makeGetResponse(list: SeenAddress[]) {
  return {
    responses: [
      ['SeenAddress/get', { list, state: 'state-2' }, 'c0'],
    ] as unknown[],
    sessionState: 'sess-state-1',
  };
}

/** Reset the store to idle between tests. */
function resetStore() {
  (seenAddresses as unknown as { status: string }).status = 'idle';
  (seenAddresses as unknown as { entries: SeenAddress[] }).entries = [];
}

beforeEach(async () => {
  resetStore();
  const mock = await getJmapMock();
  mock.__clearBatchImpl();
  vi.mocked((await import('../jmap/client')).jmap.batch).mockClear();
});

// ── Tests ─────────────────────────────────────────────────────────────────

describe('seenAddresses.load', () => {
  it('populates entries and transitions to ready on success', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => Promise.resolve(makeGetResponse([ALICE, BOB])));

    expect(seenAddresses.status).toBe('idle');
    await seenAddresses.load();

    expect(seenAddresses.status).toBe('ready');
    expect(seenAddresses.entries).toHaveLength(2);
    expect(seenAddresses.entries[0]!.email).toBe('alice@example.com');
    expect(seenAddresses.entries[1]!.email).toBe('bob@example.com');
  });

  it('maps optional fields with defaults when absent', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() =>
      Promise.resolve({
        responses: [
          ['SeenAddress/get', { list: [{ id: 'sa-3', email: 'carol@x.test' }], state: 's3' }, 'c0'],
        ] as unknown[],
        sessionState: 'ss',
      }),
    );

    await seenAddresses.load();

    const carol = seenAddresses.entries[0]!;
    expect(carol.displayName).toBe('');
    expect(carol.firstSeenAt).toBe('');
    expect(carol.lastUsedAt).toBe('');
    expect(carol.sendCount).toBe(0);
    expect(carol.receivedCount).toBe(0);
  });

  it('is idempotent when already ready', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => Promise.resolve(makeGetResponse([ALICE])));

    await seenAddresses.load();
    expect(seenAddresses.status).toBe('ready');

    mock.__clearBatchImpl();
    vi.mocked(mock.jmap.batch).mockClear();

    await seenAddresses.load();
    // batch should NOT have been called a second time
    expect(mock.jmap.batch).not.toHaveBeenCalled();
    expect(seenAddresses.entries).toHaveLength(1);
  });

  it('is idempotent when loading is in flight', async () => {
    let resolve!: (v: { responses: unknown[]; sessionState: string }) => void;
    const inflight = new Promise<{ responses: unknown[]; sessionState: string }>(
      (r) => { resolve = r; },
    );
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => inflight);

    const p1 = seenAddresses.load();
    const p2 = seenAddresses.load();

    resolve(makeGetResponse([ALICE]));
    await p1;
    await p2;

    expect(mock.jmap.batch).toHaveBeenCalledTimes(1);
  });

  it('transitions to error and clears entries on failure', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => Promise.reject(new Error('network error')));

    await seenAddresses.load();

    expect(seenAddresses.status).toBe('error');
    expect(seenAddresses.entries).toHaveLength(0);
  });
});

describe('seenAddresses.destroy', () => {
  it('removes the entry locally and issues SeenAddress/set destroy', async () => {
    // Seed the store with two entries.
    (seenAddresses as unknown as { entries: SeenAddress[] }).entries = [ALICE, BOB];
    (seenAddresses as unknown as { status: string }).status = 'ready';

    const mock = await getJmapMock();
    mock.__setBatchImpl(() =>
      Promise.resolve({
        responses: [
          ['SeenAddress/set', { destroyed: ['sa-1'] }, 'c0'],
        ] as unknown[],
        sessionState: 'ss',
      }),
    );

    await seenAddresses.destroy('sa-1');

    expect(seenAddresses.entries).toHaveLength(1);
    expect(seenAddresses.entries[0]!.id).toBe('sa-2');

    const batchCall = vi.mocked(mock.jmap.batch).mock.calls[0];
    expect(batchCall).toBeDefined();
  });
});

describe('seenAddresses.clear', () => {
  it('empties entries and resets status to idle', () => {
    (seenAddresses as unknown as { entries: SeenAddress[] }).entries = [ALICE, BOB];
    (seenAddresses as unknown as { status: string }).status = 'ready';

    seenAddresses.clear();

    expect(seenAddresses.entries).toHaveLength(0);
    expect(seenAddresses.status).toBe('idle');
  });
});

describe('SeenAddress sync handler', () => {
  it('triggers a reload when the SeenAddress state advances', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => Promise.resolve(makeGetResponse([ALICE])));

    // Simulate load() to get to ready state first.
    await seenAddresses.load();
    expect(seenAddresses.status).toBe('ready');

    // Simulate a state change arriving via EventSource.
    mock.__setBatchImpl(() => Promise.resolve(makeGetResponse([ALICE, BOB])));
    vi.mocked(mock.jmap.batch).mockClear();

    const handlers = await getSyncHandlers('SeenAddress');
    expect(handlers.length).toBeGreaterThan(0);
    for (const h of handlers) h('state-3');

    // Flush promises so the async reload runs.
    await new Promise<void>((r) => setTimeout(r, 0));

    expect(mock.jmap.batch).toHaveBeenCalled();
    expect(seenAddresses.entries).toHaveLength(2);
  });
});
