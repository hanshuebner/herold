/**
 * Coverage for issue #207: "select all" in mail search only covered the
 * first 50 loaded results, not the full server-side match set.
 *
 * `runSearch` now requests `calculateTotal: true` so `mail.searchTotal`
 * carries the true match count independent of the loaded window, and
 * `selectWholeSearchResults()` (search's counterpart to the folder list's
 * `selectWholeMailbox()` from issue #149) engages `listWholeMailboxSelected`
 * scoped to the search's own filter -- not the currently open folder's
 * filter -- so a bulk action taken from search acts on every match.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email, Mailbox } from './types';

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch, hasCapability: vi.fn(() => true) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()) },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: {
        'urn:ietf:params:jmap:mail': {},
        'https://netzhansa.com/jmap/email-bulk-mutation': {},
      },
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-1' },
    },
    principalId: 'principal-alice',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({
  router: { parts: [], matches: vi.fn(() => false), getParam: () => null },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (k: string) => k,
  localeTag: () => 'en',
}));

function makeMailbox(
  overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'name' | 'role'>,
): Mailbox {
  return {
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...overrides,
  };
}

function makeEmail(id: string): Email {
  return {
    id,
    threadId: `thread-${id}`,
    mailboxIds: {},
    keywords: {},
    receivedAt: '2026-01-01T12:00:00Z',
    blobId: `blob-${id}`,
    hasAttachment: false,
    preview: 'preview',
    subject: 'test',
    from: [],
    to: [],
  };
}

/** Builds a jmap.batch mock for runSearch's 4-call batch (Email/query,
 * Email/get, Thread/get, Email/get), capturing the Email/query args. */
function makeSearchBatch(opts: {
  ids: string[];
  total?: number;
  onEmailQuery?: (args: Record<string, unknown>) => void;
}): (builder: unknown) => Promise<{ responses: unknown[] }> {
  return async (builder: unknown) => {
    let counter = 0;
    const calls: { name: string; args: Record<string, unknown> }[] = [];
    const api = {
      call: (name: string, args: Record<string, unknown>) => {
        calls.push({ name, args });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (builder as (b: typeof api) => void)(api);

    const responses: [string, unknown, string][] = [];
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i]!;
      if (c.name === 'Email/query') {
        opts.onEmailQuery?.(c.args);
        responses.push([
          c.name,
          { ids: opts.ids, ...(opts.total !== undefined ? { total: opts.total } : {}) },
          `c${i}`,
        ]);
      } else {
        responses.push([c.name, { list: [] }, `c${i}`]);
      }
    }
    return { responses };
  };
}

describe('search whole-result-set selection (issue #207)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
  });

  it('runSearch requests calculateTotal: true and stores the total match count', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
    ]);

    let capturedArgs: Record<string, unknown> | undefined;
    batch.mockImplementation(
      makeSearchBatch({
        ids: Array.from({ length: 50 }, (_, i) => `e${i}`),
        total: 137,
        onEmailQuery: (args) => {
          capturedArgs = args;
        },
      }),
    );

    await mail.runSearch('spam');

    expect(capturedArgs?.calculateTotal).toBe(true);
    expect(mail.searchTotal).toBe(137);
    expect(mail.searchEmailIds).toHaveLength(50);
  });

  it('a new runSearch resets searchTotal until the new result lands', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
    ]);
    batch.mockImplementation(makeSearchBatch({ ids: ['e1'], total: 200 }));
    await mail.runSearch('first');
    expect(mail.searchTotal).toBe(200);

    batch.mockImplementation(makeSearchBatch({ ids: ['e2'], total: 3 }));
    await mail.runSearch('second');
    expect(mail.searchTotal).toBe(3);
  });

  it('clearSearch resets searchTotal to null', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
    ]);
    batch.mockImplementation(makeSearchBatch({ ids: ['e1'], total: 90 }));
    await mail.runSearch('spam');
    expect(mail.searchTotal).toBe(90);

    mail.clearSearch();
    expect(mail.searchTotal).toBeNull();
  });

  it('selectWholeSearchResults engages listWholeMailboxSelected', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
    ]);
    batch.mockImplementation(makeSearchBatch({ ids: ['e1', 'e2'], total: 120 }));
    await mail.runSearch('spam');

    mail.listSelectedIds = new Set(mail.searchEmailIds);
    mail.selectWholeSearchResults();

    expect(mail.listWholeMailboxSelected).toBe(true);
  });

  it('bulkArchive from a whole-search selection scopes Email/setByQuery to the search filter, not the loaded folder', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
      ['mbx-archive', makeMailbox({ id: 'mbx-archive', name: 'Archive', role: 'archive' })],
    ]);
    // A folder is loaded behind the search view -- listFolder is 'inbox',
    // which would resolve to a completely different filter than the
    // search's own filter if the bulk path fell back to
    // #buildCurrentFolderFilter().
    mail.listFolder = 'inbox';

    batch.mockImplementation(makeSearchBatch({ ids: ['e1', 'e2'], total: 137 }));
    await mail.runSearch('spam');

    mail.listSelectedIds = new Set(mail.searchEmailIds);
    mail.selectWholeSearchResults();
    expect(mail.listWholeMailboxSelected).toBe(true);

    batch.mockReset();
    batch.mockResolvedValueOnce({
      responses: [
        ['Email/setByQuery', { accountId: 'acct-1', jobId: 'job-1', matchedEstimate: 137 }, 'c0'],
      ],
    });

    await mail.bulkArchive(mail.searchEmailIds);

    expect(batch).toHaveBeenCalledTimes(1);
    const builder = batch.mock.calls[0]![0] as (b: unknown) => void;
    let sentFilter: unknown;
    builder({
      call: (_name: string, args: { filter: unknown }) => {
        sentFilter = args.filter;
        return { ref: () => ({}) };
      },
    });
    // The search filter for a plain "spam" query is `{ text: 'spam' }`
    // (no trash/junk mailboxes configured here, so
    // applyTrashJunkExclusion leaves it unwrapped) -- crucially NOT
    // `{ inMailbox: 'mbx-inbox' }`, which is what #buildCurrentFolderFilter
    // would have produced from the folder loaded behind the search view.
    expect(sentFilter).toEqual({ text: 'spam' });
    expect(mail.bulkJob?.id).toBe('job-1');
  });

  it('selectWholeMailbox (folder path) leaves the filter override unset so #buildCurrentFolderFilter still applies', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
      ['mbx-archive', makeMailbox({ id: 'mbx-archive', name: 'Archive', role: 'archive' })],
    ]);
    mail.listFolder = 'inbox';
    mail.listEmailIds = ['e1'];
    mail.listSelectedIds = new Set(['e1']);
    mail.emails.set('e1', makeEmail('e1'));
    mail.selectWholeMailbox();

    batch.mockResolvedValueOnce({
      responses: [
        ['Email/setByQuery', { accountId: 'acct-1', jobId: 'job-2', matchedEstimate: 5 }, 'c0'],
      ],
    });

    await mail.bulkArchive(['e1']);

    const builder = batch.mock.calls[0]![0] as (b: unknown) => void;
    let sentFilter: unknown;
    builder({
      call: (_name: string, args: { filter: unknown }) => {
        sentFilter = args.filter;
        return { ref: () => ({}) };
      },
    });
    expect(sentFilter).toEqual({ inMailbox: 'mbx-inbox' });
  });
});
