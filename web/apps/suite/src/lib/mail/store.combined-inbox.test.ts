/**
 * Tests for the combined-inbox merge (issue #212, REQ-MAIL-SUB-03):
 * the primary /mail Inbox is a unified, receivedAt-sorted merge across
 * this principal's own account and every separated sub-account's own
 * Inbox, with paging that tops up whichever source is running low
 * without skipping or duplicating a message, and account-aware bulk
 * actions (REQ-MAIL-SUB-04) on the merged rows.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email } from './types';

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch },
  strict: (r: unknown[]) => r,
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()) },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-primary' },
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

let mockSubAccountList: Array<{
  accountId: string;
  identity: { id: string } | null;
  mailboxes: Array<{ id: string; role: string | null }>;
}> = [];
vi.mock('./sub-accounts.svelte', () => ({
  subAccounts: {
    get list() {
      return mockSubAccountList;
    },
  },
}));

function makeEmail(id: string, receivedAt: string, mailboxId = 'mb-primary-inbox'): Email {
  return {
    id,
    threadId: `t-${id}`,
    mailboxIds: { [mailboxId]: true },
    keywords: {},
    from: null,
    to: null,
    subject: `subject-${id}`,
    preview: '',
    receivedAt,
    hasAttachment: false,
    blobId: `blob-${id}`,
  };
}

/**
 * Dispatches jmap.batch calls by name, keying Email/query and Email/get
 * results by the call's accountId so a test can hand back different pages
 * for the primary account vs. each sub-account.
 */
function makeMultiAccountBatch(pages: Record<string, Email[][]>): {
  builder: (b: unknown) => Promise<{ responses: unknown[] }>;
  callLog: Array<{ name: string; accountId: string; args: Record<string, unknown> }>;
} {
  const cursor: Record<string, number> = {};
  const callLog: Array<{ name: string; accountId: string; args: Record<string, unknown> }> = [];
  const builder = async (b: unknown) => {
    let counter = 0;
    const calls: { name: string; args: Record<string, unknown> }[] = [];
    const api = {
      call: (name: string, args: Record<string, unknown>) => {
        calls.push({ name, args });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (b as (bb: typeof api) => void)(api);

    const responses: [string, unknown, string][] = [];
    // Track which accountId each Email/query call belongs to so the
    // paired Email/get in the same batch can resolve the same page.
    const queryAccountByCallIndex: Record<number, string> = {};
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i]!;
      const accountId = String(c.args.accountId ?? '');
      callLog.push({ name: c.name, accountId, args: c.args });
      if (c.name === 'Email/query') {
        queryAccountByCallIndex[i] = accountId;
        const acctPages = pages[accountId] ?? [];
        const idx = cursor[accountId] ?? 0;
        const page = acctPages[idx] ?? [];
        cursor[accountId] = idx + 1;
        responses.push([c.name, { ids: page.map((e) => e.id) }, `c${i}`]);
      } else if (c.name === 'Email/get') {
        // Find the most recent Email/query in this same batch for the
        // account this Email/get targets.
        let page: Email[] = [];
        for (let j = i - 1; j >= 0; j--) {
          if (queryAccountByCallIndex[j] === accountId) {
            const acctPages = pages[accountId] ?? [];
            const idx = (cursor[accountId] ?? 1) - 1;
            page = acctPages[idx] ?? [];
            break;
          }
        }
        responses.push([c.name, { list: page, state: `state-${accountId}` }, `c${i}`]);
      } else if (c.name === 'Thread/get') {
        responses.push([c.name, { list: [] }, `c${i}`]);
      } else {
        responses.push([c.name, { list: [] }, `c${i}`]);
      }
    }
    return { responses };
  };
  return { builder, callLog };
}

describe('mergeByReceivedAt', () => {
  it('sorts descending by receivedAt across accounts', async () => {
    const { mergeByReceivedAt } = await import('./store.svelte');
    const a = makeEmail('a', '2026-01-01T10:00:00Z');
    const b = makeEmail('b', '2026-01-02T10:00:00Z');
    const c = makeEmail('c', '2026-01-01T15:00:00Z');
    expect(mergeByReceivedAt([a, b, c]).map((e) => e.id)).toEqual(['b', 'c', 'a']);
  });

  it('is a stable, deterministic tiebreak on equal receivedAt (by id descending)', async () => {
    const { mergeByReceivedAt } = await import('./store.svelte');
    const a = makeEmail('a', '2026-01-01T10:00:00Z');
    const b = makeEmail('b', '2026-01-01T10:00:00Z');
    expect(mergeByReceivedAt([a, b]).map((e) => e.id)).toEqual(['b', 'a']);
  });

  it('does not mutate the input array', async () => {
    const { mergeByReceivedAt } = await import('./store.svelte');
    const a = makeEmail('a', '2026-01-01T10:00:00Z');
    const b = makeEmail('b', '2026-01-02T10:00:00Z');
    const input = [a, b];
    mergeByReceivedAt(input);
    expect(input).toEqual([a, b]);
  });
});

describe('combined Inbox (issue #212, REQ-MAIL-SUB-03)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
    mockSubAccountList = [];
  });

  it('loadFolder("inbox") stays single-account when there are no separated identities', async () => {
    const { builder } = makeMultiAccountBatch({
      'acct-primary': [[makeEmail('p1', '2026-01-03T00:00:00Z')]],
    });
    batch.mockImplementation(builder);
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      [
        'mb-primary-inbox',
        {
          id: 'mb-primary-inbox',
          name: 'Inbox',
          role: 'inbox',
          parentId: null,
          sortOrder: 0,
          totalEmails: 1,
          unreadEmails: 0,
          totalThreads: 1,
          unreadThreads: 0,
        },
      ],
    ]);

    await mail.loadFolder('inbox');

    expect(mail.listEmailIds).toEqual(['p1']);
    expect(mail.emailAccountId.size).toBe(0);
  });

  it('merges a separated identity\'s own Inbox into the primary Inbox, sorted by receivedAt', async () => {
    mockSubAccountList = [
      { accountId: 'acct-sub', identity: { id: '99' }, mailboxes: [{ id: 'mb-sub-inbox', role: 'inbox' }] },
    ];
    const { builder } = makeMultiAccountBatch({
      'acct-primary': [[makeEmail('p1', '2026-01-01T00:00:00Z')]],
      'acct-sub': [[makeEmail('s1', '2026-01-05T00:00:00Z', 'mb-sub-inbox')]],
    });
    batch.mockImplementation(builder);
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      [
        'mb-primary-inbox',
        {
          id: 'mb-primary-inbox',
          name: 'Inbox',
          role: 'inbox',
          parentId: null,
          sortOrder: 0,
          totalEmails: 1,
          unreadEmails: 0,
          totalThreads: 1,
          unreadThreads: 0,
        },
      ],
    ]);

    await mail.loadFolder('inbox');

    // Newer sub-account message sorts first.
    expect(mail.listEmailIds).toEqual(['s1', 'p1']);
    expect(mail.emailAccountId.get('s1')).toBe('acct-sub');
    // The primary account's own row is never tagged (default = own account).
    expect(mail.emailAccountId.has('p1')).toBe(false);
  });

  it('load more tops up whichever source is running low and keeps the merge correct', async () => {
    // FOLDER_PAGE_SIZE is 50 (store.svelte.ts, unexported). The primary
    // account's first page is a FULL page (not yet exhausted -- there
    // may be more), so more than one page's worth of messages exists
    // across the two sources combined; only the top 50 by receivedAt are
    // shown on page 1, and "load more" must top up primary (still has
    // more) while leaving the already-exhausted sub-account alone.
    const primaryPage1: Email[] = [];
    for (let i = 0; i < 50; i++) {
      // Oldest dates -- every sub-account row below is newer, so the
      // first 2 merged slots go to the sub-account and the last 2
      // primary rows spill into the pending buffer for "load more".
      primaryPage1.push(makeEmail(`p${i}`, `2020-01-01T00:00:${String(i).padStart(2, '0')}Z`));
    }
    const primaryPage2 = [makeEmail('p-more', '2020-01-01T00:01:00Z')];
    const subPage1 = [
      makeEmail('s0', '2026-01-01T00:00:00Z', 'mb-sub-inbox'),
      makeEmail('s1', '2026-01-01T00:00:01Z', 'mb-sub-inbox'),
    ];

    mockSubAccountList = [
      { accountId: 'acct-sub', identity: { id: '99' }, mailboxes: [{ id: 'mb-sub-inbox', role: 'inbox' }] },
    ];
    const { builder } = makeMultiAccountBatch({
      'acct-primary': [primaryPage1, primaryPage2],
      'acct-sub': [subPage1],
    });
    batch.mockImplementation(builder);
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      [
        'mb-primary-inbox',
        {
          id: 'mb-primary-inbox',
          name: 'Inbox',
          role: 'inbox',
          parentId: null,
          sortOrder: 0,
          totalEmails: 50,
          unreadEmails: 0,
          totalThreads: 50,
          unreadThreads: 0,
        },
      ],
    ]);

    await mail.loadFolder('inbox');
    // Page 1: the two newest (sub-account) rows lead, then the 48
    // newest primary rows (p49 down to p2 -- p0/p1 are the oldest two
    // and spill into the pending buffer).
    expect(mail.listEmailIds.slice(0, 2)).toEqual(['s1', 's0']);
    expect(mail.listEmailIds).toHaveLength(50);
    expect(mail.listHasMore).toBe(true);

    await mail.loadMoreFolder();
    // Page 2 = the 2 primary rows left over from page 1's merge, plus
    // primary's own next page (fetched because primary was not yet
    // exhausted; the sub-account, already short-paged, is left alone).
    expect(mail.listEmailIds).toHaveLength(53);
    expect(new Set(mail.listEmailIds).size).toBe(53);
    expect(mail.listEmailIds.slice(50)).toEqual(
      expect.arrayContaining(['p0', 'p1', 'p-more']),
    );
  });

  it('bulk actions on a merged combined-inbox row target the row\'s own sub-account', async () => {
    mockSubAccountList = [
      { accountId: 'acct-sub', identity: { id: '99' }, mailboxes: [{ id: 'mb-sub-inbox', role: 'inbox' }] },
    ];
    const { builder, callLog } = makeMultiAccountBatch({
      'acct-primary': [[makeEmail('p1', '2026-01-01T00:00:00Z')]],
      'acct-sub': [[makeEmail('s1', '2026-01-05T00:00:00Z', 'mb-sub-inbox')]],
    });
    batch.mockImplementation(builder);
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([
      [
        'mb-primary-inbox',
        {
          id: 'mb-primary-inbox',
          name: 'Inbox',
          role: 'inbox',
          parentId: null,
          sortOrder: 0,
          totalEmails: 1,
          unreadEmails: 0,
          totalThreads: 1,
          unreadThreads: 0,
        },
      ],
    ]);
    await mail.loadFolder('inbox');
    callLog.length = 0;

    await mail.bulkSetSeen(['s1'], true);

    const emailSetCall = callLog.find((c) => c.name === 'Email/set');
    expect(emailSetCall?.accountId).toBe('acct-sub');
    expect(emailSetCall?.args.update).toEqual({ s1: { 'keywords/$seen': true } });
  });
});
