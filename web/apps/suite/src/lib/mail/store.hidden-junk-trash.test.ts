/**
 * Issue #384: a label applied to a message while it sits in Junk is
 * excluded from the label's folder view by the REQ-SRC-06 Junk/Trash
 * exclusion (issue #310), and the view renders the empty state with no
 * explanation when every member is hidden this way.
 *
 * These tests drive the real `mail` singleton against a mocked jmap
 * client, the same harness shape as store.bulk-empty-refresh.test.ts, to
 * verify `loadFolder` computes `listHiddenJunkTrashCount` from its own
 * query (independent of the #313 sidebar count) and that
 * `loadFolder(id, { unfiltered: true })` surfaces the hidden members via
 * the plain `inMailbox` query.
 */

import { vi, describe, it, expect, beforeEach } from 'vitest';
import type { Email, Identity, Mailbox } from './types';

const { batchMock, strictPassthrough, capturedCalls, scenario, AUTH_SESSION } = vi.hoisted(() => {
  const capturedCalls: Array<{ name: string; args: Record<string, unknown> }[]> = [];

  // Mutated per-test to steer the mocked batch response.
  const scenario: {
    mainIds: string[];
    mainEmails: Email[];
    visibleTotal: number;
    rawTotal: number;
  } = { mainIds: [], mainEmails: [], visibleTotal: 0, rawTotal: 0 };

  const AUTH_SESSION = {
    username: 'alice@example.local',
    primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-test-1' },
  };

  const batchMock = vi.fn(
    async (
      builderFn: (b: {
        call: (name: string, args: Record<string, unknown>, using: unknown) => { ref: () => unknown };
      }) => void,
    ) => {
      const calls: { name: string; args: Record<string, unknown> }[] = [];
      const mockB = {
        call(name: string, args: Record<string, unknown>) {
          calls.push({ name, args });
          return { ref: () => ({}) };
        },
      };
      builderFn(mockB);
      capturedCalls.push(calls);

      const responses: [string, unknown, string][] = [
        ['Email/query', { ids: scenario.mainIds }, 'c0'],
        ['Email/get', { list: scenario.mainEmails, state: 's1' }, 'c1'],
        ['Thread/get', { list: [] }, 'c2'],
        ['Email/get', { list: [] }, 'c3'],
      ];
      // The optional 5th/6th calls are the hidden-members count queries
      // (re #384): the folder view's own filter's calculateTotal (visible),
      // then the label's raw, unfiltered membership calculateTotal (raw).
      const visibleCall = calls[4];
      const rawCall = calls[5];
      if (
        visibleCall &&
        visibleCall.name === 'Email/query' &&
        visibleCall.args.calculateTotal === true &&
        rawCall &&
        rawCall.name === 'Email/query' &&
        rawCall.args.calculateTotal === true
      ) {
        responses.push(['Email/query', { total: scenario.visibleTotal }, 'c4']);
        responses.push(['Email/query', { total: scenario.rawTotal }, 'c5']);
      }
      return { responses, sessionState: 's-fresh' };
    },
  );

  const strictPassthrough = vi.fn((responses: unknown[]) => responses);

  return { batchMock, strictPassthrough, capturedCalls, scenario, AUTH_SESSION };
});

vi.mock('../jmap/client', () => ({
  jmap: { batch: batchMock },
  strict: strictPassthrough,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: { session: AUTH_SESSION },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn().mockReturnValue(() => undefined) },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

vi.mock('../notifications/sounds.svelte', () => ({
  sounds: { play: vi.fn() },
}));

vi.mock('../notifications/cue-gates', () => ({
  shouldPlayMailCue: vi.fn().mockReturnValue(false),
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: { desktopNotifEnabled: false },
}));

vi.mock('../router/router.svelte', () => ({
  router: {
    parts: [],
    navigate: vi.fn(),
    matches: vi.fn().mockReturnValue(false),
  },
}));

vi.mock('../debug-ring/debug-ring', () => ({
  appendEvent: vi.fn(),
}));

vi.mock('../storage/account-scoped', () => ({
  accountKey: vi.fn().mockReturnValue('test-key'),
}));

vi.mock('./identity-match', () => ({
  buildSelfEmailSet: vi.fn().mockReturnValue(new Set()),
  isFromSelf: vi.fn().mockReturnValue(false),
}));

import { mail } from './store.svelte';

const INBOX_ID = 'mbx-inbox';
const TRASH_ID = 'mbx-trash';
const JUNK_ID = 'mbx-junk';
const LABEL_ID = 'mbx-label';

function makeMailbox(id: string, name: string, role: string | null): Mailbox {
  return {
    id,
    name,
    role,
    parentId: null,
    sortOrder: 1,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  };
}

function makeIdentity(): Identity {
  return {
    id: 'ident-1',
    name: 'Alice',
    email: 'alice@example.local',
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
  };
}

function makeEmail(id: string, mailboxIds: Record<string, true>): Email {
  return {
    id,
    threadId: `t-${id}`,
    mailboxIds,
    keywords: {},
    from: null,
    to: null,
    subject: `Subject ${id}`,
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: `b-${id}`,
  };
}

function setupLabelAccount(): void {
  const mailboxes = new Map<string, Mailbox>();
  mailboxes.set(INBOX_ID, makeMailbox(INBOX_ID, 'Inbox', 'inbox'));
  mailboxes.set(TRASH_ID, makeMailbox(TRASH_ID, 'Trash', 'trash'));
  mailboxes.set(JUNK_ID, makeMailbox(JUNK_ID, 'Junk', 'junk'));
  mailboxes.set(LABEL_ID, makeMailbox(LABEL_ID, 'not-spam', null));
  mail.mailboxes = mailboxes;
  mail.identities = new Map([['ident-1', makeIdentity()]]);
}

describe('loadFolder hidden-members count (re #384)', () => {
  beforeEach(() => {
    batchMock.mockClear();
    capturedCalls.length = 0;
    scenario.mainIds = [];
    scenario.mainEmails = [];
    scenario.visibleTotal = 0;
    scenario.rawTotal = 0;
    mail.reset();
  });

  it('a label whose only members sit in Junk renders the empty list but reports the hidden count', async () => {
    setupLabelAccount();
    // Every one of the label's 23 raw members is also in Junk, so the
    // filtered (default) query returns nothing -- the exact symptom the
    // ticket reports -- and its own calculateTotal is 0, while the raw
    // (unfiltered) membership total is 23. hidden = 23 - 0 = 23.
    scenario.mainIds = [];
    scenario.mainEmails = [];
    scenario.visibleTotal = 0;
    scenario.rawTotal = 23;

    await mail.loadFolder(LABEL_ID);

    expect(mail.listLoadStatus).toBe('ready');
    expect(mail.listEmailIds).toEqual([]);
    expect(mail.listUnfiltered).toBe(false);
    expect(mail.listHiddenJunkTrashCount).toBe(23);

    // The main list query carried the Junk/Trash exclusion; the extra
    // 5th/6th calls are the dedicated calculateTotal count queries: the
    // same exclusion-applied filter, then the label's raw membership.
    const calls = capturedCalls[0]!;
    expect(calls).toHaveLength(6);
    expect(calls[0]!.args.filter).toMatchObject({ inMailboxOtherThan: expect.arrayContaining([TRASH_ID, JUNK_ID]) });
    expect(calls[4]!.name).toBe('Email/query');
    expect(calls[4]!.args.calculateTotal).toBe(true);
    expect(calls[4]!.args.limit).toBe(0);
    expect(calls[4]!.args.filter).toMatchObject({ inMailboxOtherThan: expect.arrayContaining([TRASH_ID, JUNK_ID]) });
    expect(calls[5]!.name).toBe('Email/query');
    expect(calls[5]!.args.calculateTotal).toBe(true);
    expect(calls[5]!.args.limit).toBe(0);
    expect(calls[5]!.args.filter).toEqual({ inMailbox: LABEL_ID });
  });

  it('the unfiltered link target lists the same label without the exclusion', async () => {
    setupLabelAccount();
    const memberIds = Array.from({ length: 23 }, (_, i) => `m${i}`);
    scenario.mainIds = memberIds;
    scenario.mainEmails = memberIds.map((id) => makeEmail(id, { [LABEL_ID]: true, [JUNK_ID]: true }));

    await mail.loadFolder(LABEL_ID, { unfiltered: true });

    expect(mail.listLoadStatus).toBe('ready');
    expect(mail.listUnfiltered).toBe(true);
    expect(mail.listEmailIds).toEqual(memberIds);
    // No hidden-count query is needed on the unfiltered view itself.
    expect(mail.listHiddenJunkTrashCount).toBeNull();

    const calls = capturedCalls[0]!;
    expect(calls).toHaveLength(4);
    expect(calls[0]!.args.filter).toEqual({ inMailbox: LABEL_ID });
  });

  it('a label with visible members and no hidden ones reports zero hidden', async () => {
    setupLabelAccount();
    const memberIds = ['m1', 'm2', 'm3'];
    scenario.mainIds = memberIds;
    scenario.mainEmails = memberIds.map((id) => makeEmail(id, { [LABEL_ID]: true }));
    scenario.visibleTotal = 3;
    scenario.rawTotal = 3;

    await mail.loadFolder(LABEL_ID);

    expect(mail.listEmailIds).toEqual(memberIds);
    expect(mail.listHiddenJunkTrashCount).toBe(0);
  });
});
