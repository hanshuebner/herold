/**
 * Issue #148: after a bulk action (archive / delete / destroy) removes every
 * visible message from the list, the store must re-issue Email/query to
 * populate the next window rather than showing the false empty-state.
 *
 * These tests drive the actual MailStore singleton (not a mock) against a
 * mocked jmap client so we can verify that #refreshFolderInPlace is triggered
 * when listEmailIds becomes empty while listLoadStatus is still 'ready'.
 */

import { vi, describe, it, expect, beforeEach } from 'vitest';
import type { Email, Mailbox } from './types';

// ── Hoisted mock primitives ──────────────────────────────────────────────────
//
// vi.hoisted runs before module evaluation so these values are available in
// the vi.mock(...) factory closures below, which also run before imports.

const { batchMock, strictPassthrough, capturedFirstCallNames, AUTH_SESSION } =
  vi.hoisted(() => {
    // Track the first method name of each jmap.batch call so tests can assert
    // which JMAP methods were sent.
    const capturedFirstCallNames: string[] = [];

    const AUTH_SESSION = {
      username: 'alice@example.local',
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acct-test-1',
      },
    };

    // Fresh email objects returned by the mock Email/query + Email/get response.
    const freshEmails: Email[] = [
      {
        id: 'fresh-e1',
        threadId: 'fresh-t1',
        mailboxIds: {},
        keywords: {},
        from: null,
        to: null,
        subject: 'New message 1',
        preview: '',
        receivedAt: '2026-01-02T00:00:00Z',
        hasAttachment: false,
        blobId: 'fresh-b1',
      },
      {
        id: 'fresh-e2',
        threadId: 'fresh-t2',
        mailboxIds: {},
        keywords: {},
        from: null,
        to: null,
        subject: 'New message 2',
        preview: '',
        receivedAt: '2026-01-02T00:00:00Z',
        hasAttachment: false,
        blobId: 'fresh-b2',
      },
    ];

    const batchMock = vi.fn(
      async (
        builderFn: (b: {
          call: (
            name: string,
            args: unknown,
            using: unknown,
          ) => { ref: () => unknown };
        }) => void,
      ) => {
        const names: string[] = [];
        const mockB = {
          call(name: string) {
            names.push(name);
            return { ref: () => ({}) };
          },
        };
        builderFn(mockB);
        const first = names[0] ?? '';
        capturedFirstCallNames.push(first);

        if (first === 'Email/query') {
          // Folder refresh: return fresh email list.
          return {
            responses: [
              ['Email/query', { ids: ['fresh-e1', 'fresh-e2'] }, 'c0'],
              ['Email/get', { list: freshEmails, state: 's-fresh' }, 'c1'],
              ['Thread/get', { list: [] }, 'c2'],
              ['Email/get', { list: [] }, 'c3'],
            ],
            sessionState: 's-fresh',
          };
        }
        if (first === 'Email/set') {
          // Bulk mutation success.
          return {
            responses: [
              [
                'Email/set',
                { updated: {}, newState: 's2', notUpdated: null },
                'c0',
              ],
            ],
            sessionState: 's2',
          };
        }
        if (first === 'Mailbox/get') {
          // Mailbox count refresh — return the mailboxes so #refreshFolderInPlace
          // can find inbox/archive after loadMailboxes runs.
          return {
            responses: [
              [
                'Mailbox/get',
                {
                  list: [
                    {
                      id: 'mbx-inbox',
                      name: 'Inbox',
                      role: 'inbox',
                      parentId: null,
                      sortOrder: 1,
                      totalEmails: 80,
                      unreadEmails: 5,
                      totalThreads: 80,
                      unreadThreads: 5,
                    },
                    {
                      id: 'mbx-archive',
                      name: 'Archive',
                      role: 'archive',
                      parentId: null,
                      sortOrder: 2,
                      totalEmails: 150,
                      unreadEmails: 0,
                      totalThreads: 150,
                      unreadThreads: 0,
                    },
                    {
                      id: 'mbx-trash',
                      name: 'Trash',
                      role: 'trash',
                      parentId: null,
                      sortOrder: 3,
                      totalEmails: 10,
                      unreadEmails: 0,
                      totalThreads: 10,
                      unreadThreads: 0,
                    },
                  ],
                  state: 's1',
                },
                'c0',
              ],
            ],
            sessionState: 's1',
          };
        }
        return { responses: [], sessionState: 's0' };
      },
    );

    const strictPassthrough = vi.fn(
      (responses: unknown[]) => responses,
    );

    return {
      batchMock,
      strictPassthrough,
      capturedFirstCallNames,
      AUTH_SESSION,
    };
  });

// ── Module mocks ─────────────────────────────────────────────────────────────

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

// ── Import the store under test ───────────────────────────────────────────────

import { mail } from './store.svelte';

// ── Helpers ───────────────────────────────────────────────────────────────────

const INBOX_ID = 'mbx-inbox';
const ARCHIVE_ID = 'mbx-archive';
const TRASH_ID = 'mbx-trash';

function makeMailbox(
  id: string,
  name: string,
  role: string,
): Mailbox {
  return {
    id,
    name,
    role,
    parentId: null,
    sortOrder: 1,
    totalEmails: 50,
    unreadEmails: 10,
    totalThreads: 50,
    unreadThreads: 10,
  };
}

function makeInboxEmail(id: string): Email {
  return {
    id,
    threadId: `t-${id}`,
    mailboxIds: { [INBOX_ID]: true },
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

function makeTrashEmail(id: string): Email {
  return {
    id,
    threadId: `t-${id}`,
    mailboxIds: { [TRASH_ID]: true },
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

/** Populate the store with 50 inbox emails in 'ready' state. */
function setupInboxWith50Emails(): void {
  const mailboxes = new Map<string, Mailbox>();
  mailboxes.set(INBOX_ID, makeMailbox(INBOX_ID, 'Inbox', 'inbox'));
  mailboxes.set(ARCHIVE_ID, makeMailbox(ARCHIVE_ID, 'Archive', 'archive'));
  mailboxes.set(TRASH_ID, makeMailbox(TRASH_ID, 'Trash', 'trash'));
  mail.mailboxes = mailboxes;

  const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);
  const emails = new Map<string, Email>();
  for (const id of ids) emails.set(id, makeInboxEmail(id));
  mail.emails = emails;
  mail.listEmailIds = ids;
  mail.listLoadStatus = 'ready';
  mail.listFolder = 'inbox';
}

/** Populate the store with 50 inbox emails that are also in trash. */
function setupTrashWith50Emails(): void {
  const mailboxes = new Map<string, Mailbox>();
  mailboxes.set(INBOX_ID, makeMailbox(INBOX_ID, 'Inbox', 'inbox'));
  mailboxes.set(ARCHIVE_ID, makeMailbox(ARCHIVE_ID, 'Archive', 'archive'));
  mailboxes.set(TRASH_ID, makeMailbox(TRASH_ID, 'Trash', 'trash'));
  mail.mailboxes = mailboxes;

  const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);
  const emails = new Map<string, Email>();
  for (const id of ids) emails.set(id, makeTrashEmail(id));
  mail.emails = emails;
  mail.listEmailIds = ids;
  mail.listLoadStatus = 'ready';
  mail.listFolder = 'inbox'; // viewing inbox; trash emails come from another folder
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('bulk-action empty-list refresh (re #148)', () => {
  beforeEach(() => {
    batchMock.mockClear();
    capturedFirstCallNames.length = 0;
    mail.reset();
  });

  it('bulkArchive: triggers Email/query when the whole 50-email window is archived', async () => {
    setupInboxWith50Emails();
    const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);

    await mail.bulkArchive(ids);

    // Wait for the fire-and-forget #refreshFolderInPlace to complete.
    await vi.waitFor(
      () => {
        expect(capturedFirstCallNames).toContain('Email/query');
      },
      { timeout: 2000 },
    );
  });

  it('bulkArchive: listEmailIds is populated with fresh results after the empty-window refresh', async () => {
    setupInboxWith50Emails();
    const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);

    await mail.bulkArchive(ids);

    await vi.waitFor(
      () => {
        expect(mail.listEmailIds).toEqual(['fresh-e1', 'fresh-e2']);
      },
      { timeout: 2000 },
    );
  });

  it('bulkArchive: no Email/query refresh when only a subset is archived (list not emptied)', async () => {
    setupInboxWith50Emails();
    // Archive only 10 of the 50 — list is not emptied.
    const ids = Array.from({ length: 10 }, (_, i) => `email-${i}`);
    batchMock.mockClear();
    capturedFirstCallNames.length = 0;

    await mail.bulkArchive(ids);
    // Flush microtasks.
    await new Promise<void>((resolve) => setTimeout(resolve, 50));

    // Only Email/set + Mailbox/get should have been called — no Email/query.
    expect(capturedFirstCallNames).not.toContain('Email/query');
  });

  it('bulkDestroy: triggers Email/query when the whole 50-email window is destroyed', async () => {
    setupInboxWith50Emails();
    const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);

    await mail.bulkDestroy(ids);

    await vi.waitFor(
      () => {
        expect(capturedFirstCallNames).toContain('Email/query');
      },
      { timeout: 2000 },
    );
  });

  it('bulkDestroy: listEmailIds is populated with fresh results after the empty-window refresh', async () => {
    setupInboxWith50Emails();
    const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);

    await mail.bulkDestroy(ids);

    await vi.waitFor(
      () => {
        expect(mail.listEmailIds).toEqual(['fresh-e1', 'fresh-e2']);
      },
      { timeout: 2000 },
    );
  });

  it('bulkDelete: triggers Email/query when the whole 50-email window is deleted (inbox → trash)', async () => {
    setupInboxWith50Emails();
    const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);

    await mail.bulkDelete(ids);

    await vi.waitFor(
      () => {
        expect(capturedFirstCallNames).toContain('Email/query');
      },
      { timeout: 2000 },
    );
  });

  it('bulkDelete: listEmailIds is populated with fresh results after the empty-window refresh', async () => {
    setupInboxWith50Emails();
    const ids = Array.from({ length: 50 }, (_, i) => `email-${i}`);

    await mail.bulkDelete(ids);

    await vi.waitFor(
      () => {
        expect(mail.listEmailIds).toEqual(['fresh-e1', 'fresh-e2']);
      },
      { timeout: 2000 },
    );
  });
});
