/**
 * Coverage for issue #426: a whole-mailbox bulk action (select-all) taken
 * from the "all" virtual folder must scope its `Email/setByQuery` to the
 * same Junk/Trash exclusion the folder view itself queries with --
 * `#startWholeMailboxBulk` resolves its filter via
 * `#buildCurrentFolderFilter()`, which for `folder === 'all'` now routes
 * through `buildAllMailFilter` instead of returning `undefined` (no
 * filter, matching every message including Junk/Trash members).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Mailbox, Email } from './types';

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), hasCapability: vi.fn(() => true) },
  strict: (r: unknown[]) => r,
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
      apiUrl: '/jmap',
      downloadUrl: '/jmap/dl/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'alice@example.local',
      accounts: {},
      state: 'sess-1',
    },
    principalId: 'principal-alice',
    errorMessage: null,
    needsStepUp: false,
  },
  registerAccountResetCallback: vi.fn(),
}));

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const syncHandlers = new Map<string, (newState: string, accountId: string) => void>();

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn((type: string, handler: (newState: string, accountId: string) => void) => {
      syncHandlers.set(type, handler);
      return vi.fn();
    }),
    start: vi.fn(),
    stop: vi.fn(),
  },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../router/router.svelte', () => ({
  router: {
    parts: [],
    matches: vi.fn(() => false),
    navigate: vi.fn(),
    getParam: vi.fn(() => null),
    setParam: vi.fn(),
  },
}));

vi.mock('../notifications/sounds.svelte', () => ({
  sounds: { play: vi.fn() },
}));

vi.mock('../notifications/notifications.svelte', () => ({
  notifications: { add: vi.fn() },
}));

vi.mock('../compose/compose.svelte', () => ({
  compose: {
    openReply: vi.fn(),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
    openDraft: vi.fn(),
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>) => {
    if (args?.count !== undefined) return `${args.count} ${key}`;
    return key;
  },
  localeTag: () => 'en-US',
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

function makeEmail(id: string, mailboxIds: Record<string, true>): Email {
  return {
    id,
    threadId: `thread-${id}`,
    mailboxIds,
    keywords: {},
    receivedAt: '2026-01-01T12:00:00Z',
    blobId: `blob-${id}`,
    hasAttachment: false,
    preview: 'preview',
    subject: null,
    from: [],
    to: [],
  };
}

function invocation(name: string, args: unknown, callId = 'c0'): [string, unknown, string] {
  return [name, args, callId];
}

describe('whole-mailbox bulk action from the "all" view excludes Junk/Trash (re #426)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  const INBOX_ID = 'mbox-inbox';
  const ARCHIVE_ID = 'mbox-archive';
  const JUNK_ID = 'mbox-junk';
  const TRASH_ID = 'mbox-trash';

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    syncHandlers.clear();

    jmapMod = await import('../jmap/client');
    mailMod = await import('./store.svelte');
    const { mail } = mailMod;

    mail.mailboxes = new Map([
      [INBOX_ID, makeMailbox({ id: INBOX_ID, name: 'Inbox', role: 'inbox' })],
      [ARCHIVE_ID, makeMailbox({ id: ARCHIVE_ID, name: 'Archive', role: 'archive' })],
      // Named "Spam", but carries role: 'junk' -- exactly what
      // internal/imapimport/sync.go's ensureMailbox produces for an
      // upstream folder literally named "Spam" with no folder-map
      // override (re #426 analysis): the exclusion must key off role,
      // not the mailbox name.
      [JUNK_ID, makeMailbox({ id: JUNK_ID, name: 'Spam', role: 'junk' })],
      [TRASH_ID, makeMailbox({ id: TRASH_ID, name: 'Trash', role: 'trash' })],
    ]);
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true }));

    mail.listFolder = 'all';
    mail.listEmailIds = ['e1'];
    mail.listSelectedIds = new Set(['e1']);
    mail.listWholeMailboxSelected = true;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('bulkArchive scopes Email/setByQuery to inMailboxOtherThan: [junk, trash], never a bare match-everything filter', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '99',
          matchedEstimate: 57,
        }),
      ],
    });

    await mail.bulkArchive(['e1']);

    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    const calls: Array<[string, unknown, string]> = [];
    builder({
      call: (name: string, args: unknown, _using: string[]) => {
        calls.push([name, args, 'c0']);
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });

    expect(calls[0]?.[0]).toBe('Email/setByQuery');
    const sentArgs = calls[0]?.[1] as { filter: unknown };
    expect(sentArgs.filter).not.toBeNull();
    expect(sentArgs.filter).not.toHaveProperty('operator');
    expect(sentArgs.filter).toEqual({
      inMailboxOtherThan: expect.arrayContaining([JUNK_ID, TRASH_ID]),
    });
  });
});
