/**
 * Tests for the in-place folder-list refresh and the coalescing gate
 * introduced in issue #127.
 *
 * In-place refresh: refreshFolder() must not clear listEmailIds or flip
 * listLoadStatus while the replacement query is in flight when the list is
 * already 'ready'. The visible list stays stable; state is swapped
 * atomically only when the response arrives.
 *
 * Coalescer: a burst of Email state-change pushes (e.g. an IMAP import)
 * must collapse into a single Email/query round-trip per 300 ms quiet
 * window via #refreshFolderSoon().
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Email, Thread } from './types';

// ---------------------------------------------------------------------------
// Module-level mocks (same pattern as store.pending-arrivals.test.ts)
// ---------------------------------------------------------------------------

const syncHandlers = new Map<
  string,
  (newState: string, accountId: string) => Promise<void>
>();

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(
      (
        type: string,
        handler: (newState: string, accountId: string) => Promise<void>,
      ) => {
        syncHandlers.set(type, handler);
        return vi.fn();
      },
    ),
  },
}));

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch, session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc1',
        'urn:ietf:params:jmap:submission': 'acc1',
      },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: 'test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    ...overrides,
  };
}

type BatchResponse = [string, unknown, string];
type BatchResult = { responses: BatchResponse[]; using: Set<string> };

/**
 * Build standard per-call responses for a JMAP batch mock. Each call to
 * jmap.batch is handled independently; this helper dispatches on the JMAP
 * method names present in the batch builder's call list.
 */
function makeBatchImpl(opts: {
  emailQueryIds?: string[];
  emailGetEmails?: Email[];
  emailState?: string;
  threads?: Thread[];
  emailChangesCreated?: string[];
}): (builder: unknown) => Promise<BatchResult> {
  return async (builder: unknown) => {
    let counter = 0;
    const calls: { name: string }[] = [];
    const api = {
      call: (name: string, _args: unknown) => {
        calls.push({ name });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (builder as (b: typeof api) => void)(api);

    const responses: BatchResponse[] = [];
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i]!;
      switch (c.name) {
        case 'Email/changes':
          responses.push([
            c.name,
            {
              created: opts.emailChangesCreated ?? [],
              updated: [],
              destroyed: [],
            },
            `c${i}`,
          ]);
          break;
        case 'Email/query':
          responses.push([c.name, { ids: opts.emailQueryIds ?? [] }, `c${i}`]);
          break;
        case 'Email/get':
          responses.push([
            c.name,
            {
              list: opts.emailGetEmails ?? [],
              state: opts.emailState ?? 'state-x',
            },
            `c${i}`,
          ]);
          break;
        case 'Thread/get':
          responses.push([c.name, { list: opts.threads ?? [] }, `c${i}`]);
          break;
        case 'Mailbox/get':
          responses.push([c.name, { list: [], state: 'mbx-state-1' }, `c${i}`]);
          break;
        case 'Identity/get':
          responses.push([c.name, { list: [] }, `c${i}`]);
          break;
        default:
          responses.push([c.name, { list: [] }, `c${i}`]);
      }
    }
    return { responses, using: new Set<string>() };
  };
}

/** Drain microtask queue by repeatedly yielding. */
async function drainMicrotasks(depth = 8): Promise<void> {
  for (let i = 0; i < depth; i++) await Promise.resolve();
}

// ---------------------------------------------------------------------------
// Tests: in-place refresh
// ---------------------------------------------------------------------------

describe('mail store: in-place refresh (issue #127)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    vi.resetModules();
  });

  it('refreshFolder() keeps listEmailIds and listLoadStatus unchanged while the query is in flight', async () => {
    const { mail } = await import('./store.svelte');

    // Seed: folder already loaded and showing two emails.
    const e1 = makeEmail({ id: 'e1', threadId: 't1' });
    const e2 = makeEmail({ id: 'e2', threadId: 't2' });
    mail.emails = new Map([['e1', e1], ['e2', e2]]);
    mail.listEmailIds = ['e1', 'e2'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';

    // Gate that holds the Email/query in flight until we release it.
    let releaseQuery!: (val: BatchResult) => void;
    const queryGate = new Promise<BatchResult>((resolve) => {
      releaseQuery = resolve;
    });

    batch.mockImplementation(async (builder: unknown) => {
      let counter = 0;
      const calls: { name: string }[] = [];
      const api = {
        call: (name: string, _args: unknown) => {
          calls.push({ name });
          return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
        },
      };
      (builder as (b: typeof api) => void)(api);

      if (calls.some((c) => c.name === 'Email/query')) {
        // This is the folder query batch — hold it.
        return queryGate;
      }
      // Other batches (Mailbox/get, Identity/get) resolve immediately.
      return makeBatchImpl({})(builder);
    });

    // Start the refresh without awaiting it.
    const refreshDone = mail.refreshFolder();

    // Flush async startup so the function reaches its first `await`.
    await drainMicrotasks();

    // ASSERT: while the query is in flight the visible list is NOT blanked.
    expect(mail.listEmailIds).toEqual(['e1', 'e2']);
    expect(mail.listLoadStatus).toBe('ready');

    // Release the held query with a fresh result.
    const e3 = makeEmail({ id: 'e3', threadId: 't3' });
    releaseQuery({
      responses: [
        ['Email/query', { ids: ['e3'] }, 'c0'],
        ['Email/get', { list: [e3], state: 'state-2' }, 'c1'],
        ['Thread/get', { list: [{ id: 't3', emailIds: ['e3'] }] }, 'c2'],
        ['Email/get', { list: [], state: 'state-2' }, 'c3'],
      ],
      using: new Set<string>(),
    });

    await refreshDone;

    // ASSERT: state is updated atomically once the response arrives.
    expect(mail.listEmailIds).toEqual(['e3']);
    expect(mail.listLoadStatus).toBe('ready');
    expect(mail.emails.has('e3')).toBe(true);
    // Pre-existing emails still in cache (merge, not replace).
    expect(mail.emails.has('e1')).toBe(true);
    expect(mail.emails.has('e2')).toBe(true);
  });

  it('refreshFolder() falls back to full reload when listLoadStatus is not ready', async () => {
    const { mail } = await import('./store.svelte');

    // Seed: folder is in 'error' state.
    mail.listEmailIds = ['stale-id'];
    mail.listLoadStatus = 'error';
    mail.listFolder = 'all';
    // mailboxes/identities not seeded — loadFolder will call loadMailboxes +
    // loadIdentities as part of the setup step.

    const e1 = makeEmail({ id: 'e1', threadId: 't1' });
    batch.mockImplementation(
      makeBatchImpl({
        emailQueryIds: ['e1'],
        emailGetEmails: [e1],
        emailState: 'state-2',
      }),
    );

    await mail.refreshFolder();

    // Full reload sets the list to the server's current state.
    expect(mail.listEmailIds).toEqual(['e1']);
    expect(mail.listLoadStatus).toBe('ready');
  });

  it('refreshFolder() in-place does not change listLoadStatus to loading or idle', async () => {
    const { mail } = await import('./store.svelte');

    mail.emails = new Map();
    mail.listEmailIds = ['e1'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';

    const statuses: string[] = [];

    // Spy: capture every listLoadStatus assignment by watching the property.
    // We use the batch timing: check status right after the query completes.
    let releaseQuery!: (val: BatchResult) => void;
    const queryGate = new Promise<BatchResult>((r) => { releaseQuery = r; });
    batch.mockImplementation(async (builder: unknown) => {
      let counter = 0;
      const calls: { name: string }[] = [];
      const api = {
        call: (name: string, _args: unknown) => {
          calls.push({ name });
          return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
        },
      };
      (builder as (b: typeof api) => void)(api);
      if (calls.some((c) => c.name === 'Email/query')) return queryGate;
      return makeBatchImpl({})(builder);
    });

    const refreshDone = mail.refreshFolder();

    // Poll status a few times during the in-flight period.
    for (let i = 0; i < 5; i++) {
      statuses.push(mail.listLoadStatus);
      await Promise.resolve();
    }

    // Release.
    releaseQuery({
      responses: [
        ['Email/query', { ids: ['e2'] }, 'c0'],
        ['Email/get', { list: [makeEmail({ id: 'e2', threadId: 't2' })], state: 'state-2' }, 'c1'],
        ['Thread/get', { list: [] }, 'c2'],
        ['Email/get', { list: [] }, 'c3'],
      ],
      using: new Set(),
    });
    await refreshDone;

    // listLoadStatus must never have left 'ready' during the in-flight period.
    expect(statuses.every((s) => s === 'ready')).toBe(true);
    expect(mail.listLoadStatus).toBe('ready');
  });
});

// ---------------------------------------------------------------------------
// Tests: #refreshFolderSoon coalescer
// ---------------------------------------------------------------------------

describe('mail store: #refreshFolderSoon coalescer (issue #127)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    vi.resetModules();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('a burst of Email state-change pushes collapses into one Email/query', async () => {
    const { mail } = await import('./store.svelte');

    // Seed list as ready so #refreshFolderSoon fires.
    mail.listEmailIds = ['e1'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';
    mail.emailState = 'state-1';

    let emailQueryCount = 0;
    batch.mockImplementation(async (builder: unknown) => {
      let counter = 0;
      const calls: { name: string }[] = [];
      const api = {
        call: (name: string, _args: unknown) => {
          calls.push({ name });
          return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
        },
      };
      (builder as (b: typeof api) => void)(api);
      if (calls.some((c) => c.name === 'Email/query')) emailQueryCount++;

      const responses: BatchResponse[] = [];
      for (let i = 0; i < calls.length; i++) {
        const c = calls[i]!;
        if (c.name === 'Email/changes') {
          responses.push([c.name, { created: ['e2'], updated: [], destroyed: [] }, `c${i}`]);
        } else if (c.name === 'Email/query') {
          responses.push([c.name, { ids: ['e2', 'e1'] }, `c${i}`]);
        } else if (c.name === 'Email/get') {
          responses.push([
            c.name,
            { list: [makeEmail({ id: 'e2', threadId: 't2' })], state: 'state-2' },
            `c${i}`,
          ]);
        } else if (c.name === 'Thread/get') {
          responses.push([c.name, { list: [] }, `c${i}`]);
        } else {
          responses.push([c.name, { list: [] }, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    // Fire three Email state changes rapidly within the 300 ms window.
    // Each handler invocation calls #refreshFolderSoon(); only the first
    // should schedule a timer — the others see #refreshFolderPending=true.
    await handler!('state-2', 'acc1');
    await handler!('state-3', 'acc1');
    await handler!('state-4', 'acc1');

    // Before the 300 ms quiet window expires: no Email/query yet.
    expect(emailQueryCount).toBe(0);

    // Advance the fake clock to fire the coalescer timer.
    vi.advanceTimersByTime(300);

    // Drain the async chain that #refreshFolderInPlace() creates.
    // #refreshFolderInPlace awaits one jmap.batch call (immediate mock),
    // then updates state. Each await step is one microtask flush.
    await drainMicrotasks(16);

    // Exactly ONE Email/query must have been issued for all three pushes.
    expect(emailQueryCount).toBe(1);
  });

  it('an Email/changes push with updated IDs in listEmailIds triggers an Email/query (issue #152)', async () => {
    const { mail } = await import('./store.svelte');

    // Seed: two emails visible in the inbox.
    mail.listEmailIds = ['e1', 'e2'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';
    mail.emailState = 'state-1';

    let emailQueryCount = 0;
    batch.mockImplementation(async (builder: unknown) => {
      let counter = 0;
      const calls: { name: string }[] = [];
      const api = {
        call: (name: string, _args: unknown) => {
          calls.push({ name });
          return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
        },
      };
      (builder as (b: typeof api) => void)(api);
      if (calls.some((c) => c.name === 'Email/query')) emailQueryCount++;

      const responses: BatchResponse[] = [];
      for (let i = 0; i < calls.length; i++) {
        const c = calls[i]!;
        if (c.name === 'Email/changes') {
          // e1 was updated (mailboxIds changed — moved out of inbox).
          // Nothing in created or destroyed.
          responses.push([c.name, { created: [], updated: ['e1'], destroyed: [] }, `c${i}`]);
        } else if (c.name === 'Email/query') {
          // After the move, inbox only has e2.
          responses.push([c.name, { ids: ['e2'] }, `c${i}`]);
        } else if (c.name === 'Email/get') {
          responses.push([c.name, { list: [], state: 'state-2' }, `c${i}`]);
        } else if (c.name === 'Thread/get') {
          responses.push([c.name, { list: [] }, `c${i}`]);
        } else {
          responses.push([c.name, { list: [] }, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    // Push a state change where e1 was updated (not created/destroyed).
    await handler!('state-2', 'acc1');

    // Before the coalescer window: no Email/query yet.
    expect(emailQueryCount).toBe(0);

    // Drain microtasks before advancing the clock. #onEmailStateChange
    // suspends at await Promise.all([]) after calling #refreshFolderSoon,
    // so its microtask continuation has not yet set the timer when the
    // test continuation runs. One drain pass is enough to guarantee the
    // timer exists before vi.advanceTimersByTime fires it.
    await drainMicrotasks(8);
    // Advance the fake clock past the 300 ms coalescer window.
    vi.advanceTimersByTime(300);
    await drainMicrotasks(16);

    // The updated ID is in listEmailIds; a folder refresh must have been issued.
    expect(emailQueryCount).toBe(1);
  });

  it('an Email/changes push with updated IDs NOT in listEmailIds does NOT trigger an Email/query', async () => {
    const { mail } = await import('./store.svelte');

    // Seed: inbox has e1 only; e2 is in a different folder.
    mail.listEmailIds = ['e1'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';
    mail.emailState = 'state-1';

    let emailQueryCount = 0;
    batch.mockImplementation(async (builder: unknown) => {
      let counter = 0;
      const calls: { name: string }[] = [];
      const api = {
        call: (name: string, _args: unknown) => {
          calls.push({ name });
          return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
        },
      };
      (builder as (b: typeof api) => void)(api);
      if (calls.some((c) => c.name === 'Email/query')) emailQueryCount++;

      const responses: BatchResponse[] = [];
      for (let i = 0; i < calls.length; i++) {
        const c = calls[i]!;
        if (c.name === 'Email/changes') {
          // e2 was updated (e.g. read in the archive folder), but e2 is not in
          // listEmailIds, so the inbox list is unaffected.
          responses.push([c.name, { created: [], updated: ['e2'], destroyed: [] }, `c${i}`]);
        } else {
          responses.push([c.name, { list: [] }, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    await handler!('state-2', 'acc1');
    vi.advanceTimersByTime(300);
    await drainMicrotasks(16);

    // The updated ID is not in listEmailIds; no folder refresh needed.
    expect(emailQueryCount).toBe(0);
  });

  it('a second burst after the quiet window gets its own Email/query', async () => {
    const { mail } = await import('./store.svelte');

    mail.listEmailIds = ['e1'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';
    mail.emailState = 'state-1';

    let emailQueryCount = 0;
    batch.mockImplementation(async (builder: unknown) => {
      let counter = 0;
      const calls: { name: string }[] = [];
      const api = {
        call: (name: string, _args: unknown) => {
          calls.push({ name });
          return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
        },
      };
      (builder as (b: typeof api) => void)(api);
      if (calls.some((c) => c.name === 'Email/query')) emailQueryCount++;

      const responses: BatchResponse[] = [];
      for (let i = 0; i < calls.length; i++) {
        const c = calls[i]!;
        if (c.name === 'Email/changes') {
          responses.push([c.name, { created: ['e2'], updated: [], destroyed: [] }, `c${i}`]);
        } else if (c.name === 'Email/query') {
          responses.push([c.name, { ids: ['e2', 'e1'] }, `c${i}`]);
        } else if (c.name === 'Email/get') {
          responses.push([c.name, { list: [], state: 'state-x' }, `c${i}`]);
        } else if (c.name === 'Thread/get') {
          responses.push([c.name, { list: [] }, `c${i}`]);
        } else {
          responses.push([c.name, { list: [] }, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email')!;

    // First burst.
    await handler('state-2', 'acc1');
    await handler('state-3', 'acc1');
    expect(emailQueryCount).toBe(0);

    // Fire first coalescer window — one Email/query.
    vi.advanceTimersByTime(300);
    await drainMicrotasks(16);
    expect(emailQueryCount).toBe(1);

    // Second burst; the pending flag has been cleared by the first timer.
    await handler('state-4', 'acc1');
    await handler('state-5', 'acc1');
    expect(emailQueryCount).toBe(1); // still 1; timer not yet fired

    // Fire second coalescer window — second Email/query.
    vi.advanceTimersByTime(300);
    await drainMicrotasks(16);
    expect(emailQueryCount).toBe(2);
  });
});
