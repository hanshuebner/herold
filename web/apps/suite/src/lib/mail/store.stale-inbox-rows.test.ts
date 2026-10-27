/**
 * Coverage for issues #542 and #543: an Inbox list row whose cached Email
 * no longer belongs in the Inbox view (the message was filed to a junk-role
 * mailbox, or left the Inbox some other way, while the list was not
 * refreshed) is dropped from the list and the list is re-queried; Archive
 * on such a row reports why instead of returning silently.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox, Email } from './types';

// ── Module-level mocks (must be before any dynamic import) ────────────────────

const batch = vi.fn();

vi.mock('../jmap/client', () => ({
  jmap: { batch, hasCapability: vi.fn(() => false) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
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

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()), start: vi.fn(), stop: vi.fn() },
}));

const toastShow = vi.fn();
vi.mock('../toast/toast.svelte', () => ({
  toast: { show: toastShow },
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

// Renders a deterministic, inspectable string per key/params so tests can
// assert exactly which i18n key (and which count/ok/failed values)
// `#summarizeBulk` resolved, without depending on the real German/English
// catalogue text.
vi.mock('../i18n/i18n.svelte', () => {
  const t = (key: string, args?: Record<string, unknown>) =>
    args ? `${key}(${JSON.stringify(args)})` : key;
  return { i18n: { t }, t, localeTag: () => 'en-US' };
});

// ── Helpers ───────────────────────────────────────────────────────────────────

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

/**
 * Extracts the `update` payload of the first `Email/set` call made since
 * `sinceCallCount` (the `batch.mock.calls.length` recorded right before
 * invoking the action under test). Scoping to calls made after a recorded
 * marker -- rather than searching the whole call history -- avoids
 * matching the bulk action's own initial `Email/set` call when a test
 * later inspects the undo replay's `Email/set` call.
 */
function emailSetUpdateSince(
  sinceCallCount: number,
): Record<string, unknown> | undefined {
  for (const call of batch.mock.calls.slice(sinceCallCount)) {
    let update: Record<string, unknown> | undefined;
    call[0]({
      call: (name: string, args: { update?: Record<string, unknown> }) => {
        if (name === 'Email/set' && args.update) update = args.update;
        return { ref: () => ({}) };
      },
    });
    if (update) return update;
  }
  return undefined;
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('Inbox rows that no longer belong in the Inbox (issues #542, #543)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  const INBOX_ID = 'mbox-inbox';
  const ARCHIVE_ID = 'mbox-archive';
  const JUNK_ID = 'mbox-junk';
  const LABEL_ID = 'mbox-label-vorsitz';

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    mailMod = await import('./store.svelte');
    const { mail } = mailMod;

    mail.mailboxes = new Map([
      [INBOX_ID, makeMailbox({ id: INBOX_ID, name: 'Inbox', role: 'inbox' })],
      [ARCHIVE_ID, makeMailbox({ id: ARCHIVE_ID, name: 'Archive', role: 'archive' })],
      [JUNK_ID, makeMailbox({ id: JUNK_ID, name: 'Junk', role: 'junk' })],
      [LABEL_ID, makeMailbox({ id: LABEL_ID, name: 'vorsitz@example.test', role: null })],
    ]);
    mail.listFolder = 'inbox';
    mail.listLoadStatus = 'ready';
    mail.listWholeMailboxSelected = false;
    batch.mockResolvedValue({ responses: [invocation('Mailbox/get', { list: [] })] });
  });

  it('bulkArchive on a row the Inbox list shows but whose Email is in Junk reports why and drops the row (#543)', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [JUNK_ID]: true, [LABEL_ID]: true }));
    mail.emails.set('e2', makeEmail('e2', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1', 'e2'];
    mail.listSelectedIds = new Set(['e1']);

    const callsBefore = batch.mock.calls.length;
    await mail.bulkArchive(['e1']);

    // No Email/set for a message that is not in the Inbox.
    expect(emailSetUpdateSince(callsBefore)).toBeUndefined();
    // The user is told why nothing was archived.
    expect(toastShow).toHaveBeenCalledTimes(1);
    expect(toastShow.mock.calls[0]?.[0].message).toBe('mail.toast.archiveNotInInbox({"count":1})');
    // Selection is released and the stale row leaves the list; the genuine row stays.
    expect(mail.listSelectedIds.size).toBe(0);
    expect(mail.listEmailIds).toEqual(['e2']);
  });

  it('bulkArchive: a stale row alongside a real Inbox row archives the real one and drops the stale one', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [JUNK_ID]: true, [LABEL_ID]: true }));
    mail.emails.set('e2', makeEmail('e2', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1', 'e2'];

    batch.mockResolvedValueOnce({
      responses: [invocation('Email/set', { updated: { e2: {} }, notUpdated: null, newState: 's2' })],
    });
    await mail.bulkArchive(['e1', 'e2']);

    expect(mail.emails.get('e2')?.mailboxIds).toEqual({ [ARCHIVE_ID]: true });
    expect(mail.listEmailIds).toEqual([]);
    expect(toastShow.mock.calls.at(-1)?.[0].message).toBe('mail.toast.archived({"count":1})');
  });

  it('refreshThread: an Email fetched into Junk removes its row from the Inbox list (#542)', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true, [LABEL_ID]: true }));
    mail.listEmailIds = ['e1'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Thread/get', { list: [{ id: 'thread-e1', emailIds: ['e1'] }] }),
        invocation('Email/get', {
          list: [makeEmail('e1', { [JUNK_ID]: true, [LABEL_ID]: true })],
          state: 's9',
        }),
      ],
    });
    await mail.refreshThread('thread-e1');

    expect(mail.listEmailIds).toEqual([]);
  });

  it('a row whose Email is a member of both Inbox and Junk is not kept in the Inbox list (#542)', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Thread/get', { list: [{ id: 'thread-e1', emailIds: ['e1'] }] }),
        invocation('Email/get', {
          list: [makeEmail('e1', { [INBOX_ID]: true, [JUNK_ID]: true })],
          state: 's9',
        }),
      ],
    });
    await mail.refreshThread('thread-e1');

    expect(mail.listEmailIds).toEqual([]);
  });

  it('rows still in the Inbox are untouched by the reconcile', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Thread/get', { list: [{ id: 'thread-e1', emailIds: ['e1'] }] }),
        invocation('Email/get', {
          list: [makeEmail('e1', { [INBOX_ID]: true, [LABEL_ID]: true })],
          state: 's9',
        }),
      ],
    });
    await mail.refreshThread('thread-e1');

    expect(mail.listEmailIds).toEqual(['e1']);
  });
});
