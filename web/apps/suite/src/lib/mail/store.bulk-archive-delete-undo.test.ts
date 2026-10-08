/**
 * Coverage for issue #515: bulkArchive/bulkDelete pass an undo callback to
 * `#summarizeBulk` that replays the inverse `Email/set` for every
 * successfully-mutated id, restoring each row's previous mailbox
 * memberships. A partially-failed bulk only undoes the ids that actually
 * succeeded. The summary toast text also renders through the i18n table
 * instead of a hardcoded English sentence.
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

describe('bulkArchive / bulkDelete undo (issue #515)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  const INBOX_ID = 'mbox-inbox';
  const ARCHIVE_ID = 'mbox-archive';
  const TRASH_ID = 'mbox-trash';
  const LABEL_ID = 'mbox-label-work';

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    mailMod = await import('./store.svelte');
    const { mail } = mailMod;

    mail.mailboxes = new Map([
      [INBOX_ID, makeMailbox({ id: INBOX_ID, name: 'Inbox', role: 'inbox' })],
      [ARCHIVE_ID, makeMailbox({ id: ARCHIVE_ID, name: 'Archive', role: 'archive' })],
      [TRASH_ID, makeMailbox({ id: TRASH_ID, name: 'Trash', role: 'trash' })],
      [LABEL_ID, makeMailbox({ id: LABEL_ID, name: 'Work', role: '' })],
    ]);
    mail.listFolder = 'inbox';
    mail.listWholeMailboxSelected = false;
    // Stray async Mailbox/get refresh (issue #24) fires on a queued
    // microtask after every bulk mutation; give it a harmless fallback
    // response so it does not reject into an unhandled console.warn noise
    // beyond what the targeted mocks below already expect.
    batch.mockResolvedValue({ responses: [invocation('Mailbox/get', { list: [] })] });
  });

  it('bulkArchive: full success undo restores Inbox membership for every id', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true }));
    mail.emails.set('e2', makeEmail('e2', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1', 'e2'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', {
          updated: { e1: {}, e2: {} },
          notUpdated: null,
          newState: 's2',
        }),
      ],
    });

    await mail.bulkArchive(['e1', 'e2']);

    // Archive applied optimistically and removed from the Inbox list.
    expect(mail.emails.get('e1')?.mailboxIds).toEqual({ [ARCHIVE_ID]: true });
    expect(mail.emails.get('e2')?.mailboxIds).toEqual({ [ARCHIVE_ID]: true });
    expect(mail.listEmailIds).toEqual([]);

    const spec = toastShow.mock.calls.at(-1)?.[0];
    expect(spec.message).toBe('mail.toast.archived.other({"count":2})');
    expect(typeof spec.undo).toBe('function');

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', {
          updated: { e1: {}, e2: {} },
          notUpdated: null,
          newState: 's3',
        }),
      ],
    });

    const callsBeforeUndo = batch.mock.calls.length;
    await spec.undo();

    // The undo call replayed a full mailboxIds replace restoring the
    // pre-archive membership for each id -- not just a toggle of the
    // Archive/Inbox keys.
    expect(emailSetUpdateSince(callsBeforeUndo)).toEqual({
      e1: { mailboxIds: { [INBOX_ID]: true } },
      e2: { mailboxIds: { [INBOX_ID]: true } },
    });

    expect(mail.emails.get('e1')?.mailboxIds).toEqual({ [INBOX_ID]: true });
    expect(mail.emails.get('e2')?.mailboxIds).toEqual({ [INBOX_ID]: true });
    expect(mail.listEmailIds).toEqual(['e1', 'e2']);
  });

  it('bulkArchive: a mixed success/failure bulk undoes only the ids that succeeded', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true, [LABEL_ID]: true }));
    mail.emails.set('e2', makeEmail('e2', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1', 'e2'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', {
          updated: { e1: {} },
          notUpdated: { e2: { type: 'notFound' } },
          newState: 's2',
        }),
      ],
    });

    await mail.bulkArchive(['e1', 'e2']);

    const spec = toastShow.mock.calls.at(-1)?.[0];
    expect(spec.message).toBe('mail.toast.archivedPartial({"ok":1,"failed":1})');
    expect(spec.kind).toBe('error');
    expect(typeof spec.undo).toBe('function');

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', { updated: { e1: {} }, notUpdated: null, newState: 's3' }),
      ],
    });

    const callsBeforeUndo = batch.mock.calls.length;
    await spec.undo();

    // e2 never succeeded -- its original mailboxIds were never captured as
    // an "ok" id, so it must not appear in the undo replay at all.
    expect(emailSetUpdateSince(callsBeforeUndo)).toEqual({
      e1: { mailboxIds: { [INBOX_ID]: true, [LABEL_ID]: true } },
    });
    expect(mail.emails.get('e1')?.mailboxIds).toEqual({ [INBOX_ID]: true, [LABEL_ID]: true });
  });

  it('bulkDelete: full success undo restores each id\'s pre-delete mailboxIds (including a label membership)', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true, [LABEL_ID]: true }));
    mail.listEmailIds = ['e1'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', { updated: { e1: {} }, notUpdated: null, newState: 's2' }),
      ],
    });

    await mail.bulkDelete(['e1']);

    expect(mail.emails.get('e1')?.mailboxIds).toEqual({ [TRASH_ID]: true });
    const spec = toastShow.mock.calls.at(-1)?.[0];
    expect(spec.message).toBe('mail.toast.deleted({"count":1})');
    expect(typeof spec.undo).toBe('function');

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', { updated: { e1: {} }, notUpdated: null, newState: 's3' }),
      ],
    });

    await spec.undo();

    expect(mail.emails.get('e1')?.mailboxIds).toEqual({ [INBOX_ID]: true, [LABEL_ID]: true });
    expect(mail.listEmailIds).toEqual(['e1']);
  });

  it('bulkDelete: a mixed success/failure bulk undoes only the ids that succeeded', async () => {
    const { mail } = mailMod;
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true }));
    mail.emails.set('e2', makeEmail('e2', { [INBOX_ID]: true }));
    mail.listEmailIds = ['e1', 'e2'];

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', {
          updated: { e1: {} },
          notUpdated: { e2: { type: 'notFound' } },
          newState: 's2',
        }),
      ],
    });

    await mail.bulkDelete(['e1', 'e2']);

    const spec = toastShow.mock.calls.at(-1)?.[0];
    expect(spec.message).toBe('mail.toast.deletedPartial({"ok":1,"failed":1})');

    batch.mockResolvedValueOnce({
      responses: [
        invocation('Email/set', { updated: { e1: {} }, notUpdated: null, newState: 's3' }),
      ],
    });

    const callsBeforeUndo = batch.mock.calls.length;
    await spec.undo();

    // e2 never succeeded -- its original mailboxIds were never captured as
    // an "ok" id, so it must not appear in the undo replay at all.
    expect(emailSetUpdateSince(callsBeforeUndo)).toEqual({
      e1: { mailboxIds: { [INBOX_ID]: true } },
    });
    expect(mail.emails.get('e1')?.mailboxIds).toEqual({ [INBOX_ID]: true });
  });
});
