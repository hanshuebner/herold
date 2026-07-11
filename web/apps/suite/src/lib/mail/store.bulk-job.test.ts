/**
 * Coverage for the whole-mailbox async bulk-job path (issue #149/#161).
 *
 * When the server advertises `https://netzhansa.com/jmap/email-bulk-
 * mutation`, archive / delete / mark read / mark unread / label add-remove
 * (issue #178) no longer refuse while `listWholeMailboxSelected` is true.
 * They call `Email/setByQuery`
 * with the current folder's filter and an update-patch, populate
 * `mail.bulkJob` from the ack response, and poll `EmailBulkJob/get` (via
 * the `EmailBulkJob` EventSource push, or a fallback timer) until the job
 * reaches a terminal status.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Mailbox, Email } from './types';

// ── Module-level mocks (must be before any dynamic import) ────────────────────

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

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('whole-mailbox async bulk job (issue #149/#161)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  const INBOX_ID = 'mbox-inbox';
  const ARCHIVE_ID = 'mbox-archive';
  const TRASH_ID = 'mbox-trash';

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    syncHandlers.clear();

    jmapMod = await import('../jmap/client');
    mailMod = await import('./store.svelte');
    const { mail } = mailMod;

    mail.mailboxes = new Map([
      [INBOX_ID, makeMailbox({ id: INBOX_ID, name: 'Inbox', role: 'inbox', totalThreads: 200 })],
      [ARCHIVE_ID, makeMailbox({ id: ARCHIVE_ID, name: 'Archive', role: 'archive' })],
      [TRASH_ID, makeMailbox({ id: TRASH_ID, name: 'Trash', role: 'trash' })],
    ]);
    mail.emails.set('e1', makeEmail('e1', { [INBOX_ID]: true }));

    mail.listFolder = 'inbox';
    mail.listEmailIds = ['e1'];
    mail.listSelectedIds = new Set(['e1']);
    mail.listWholeMailboxSelected = true;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('bulkArchive calls Email/setByQuery with the inbox filter and archive patch, and clears selection', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '42',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkArchive(['e1']);

    expect(jmapMod.jmap.batch).toHaveBeenCalledTimes(1);
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
    expect(calls[0]?.[1]).toEqual({
      accountId: 'acct-1',
      filter: { inMailbox: INBOX_ID },
      patch: {
        [`mailboxIds/${INBOX_ID}`]: null,
        [`mailboxIds/${ARCHIVE_ID}`]: true,
      },
    });

    expect(mail.listWholeMailboxSelected).toBe(false);
    expect(mail.bulkJob).toEqual({
      id: '42',
      status: 'running',
      matchedEstimate: 200,
      processed: 0,
      total: -1,
      failedIds: [],
      errors: [],
    });
  });

  it('bulkDelete calls Email/setByQuery with a mailboxIds-replace-with-trash patch', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '43',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkDelete(['e1']);

    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentPatch: unknown;
    builder({
      call: (_name: string, args: { patch: unknown }) => {
        sentPatch = args.patch;
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(sentPatch).toEqual({ mailboxIds: { [TRASH_ID]: true } });
    expect(mail.bulkJob?.id).toBe('43');
  });

  it('bulkSetSeen(true) calls Email/setByQuery with a keywords/$seen patch', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '44',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkSetSeen(['e1'], true);

    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentPatch: unknown;
    builder({
      call: (_name: string, args: { patch: unknown }) => {
        sentPatch = args.patch;
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(sentPatch).toEqual({ 'keywords/$seen': true });
  });

  it('bulkSetSeen(false) sends a null keywords/$seen patch', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '45',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkSetSeen(['e1'], false);

    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentPatch: unknown;
    builder({
      call: (_name: string, args: { patch: unknown }) => {
        sentPatch = args.patch;
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(sentPatch).toEqual({ 'keywords/$seen': null });
  });

  it('bulkSetLabel(on=true) calls Email/setByQuery with a mailboxIds/<id> patch (issue #178)', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '49',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkSetLabel(['e1'], ARCHIVE_ID, true);

    expect(jmapMod.jmap.batch).toHaveBeenCalledTimes(1);
    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentPatch: unknown;
    builder({
      call: (_name: string, args: { patch: unknown }) => {
        sentPatch = args.patch;
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(sentPatch).toEqual({ [`mailboxIds/${ARCHIVE_ID}`]: true });
    expect(mail.listWholeMailboxSelected).toBe(false);
    expect(mail.bulkJob?.id).toBe('49');
  });

  it('bulkSetLabel(on=false) sends a null mailboxIds/<id> patch (issue #178)', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '50',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkSetLabel(['e1'], ARCHIVE_ID, false);

    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentPatch: unknown;
    builder({
      call: (_name: string, args: { patch: unknown }) => {
        sentPatch = args.patch;
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(sentPatch).toEqual({ [`mailboxIds/${ARCHIVE_ID}`]: null });
  });

  it('bulkDestroy calls Email/setByQuery with destroy: true and the inbox filter (issue #179)', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '51',
          matchedEstimate: 200,
        }),
      ],
    });

    await mail.bulkDestroy(['e1']);

    expect(jmapMod.jmap.batch).toHaveBeenCalledTimes(1);
    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    const calls: Array<[string, unknown]> = [];
    builder({
      call: (name: string, args: unknown) => {
        calls.push([name, args]);
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(calls[0]?.[0]).toBe('Email/setByQuery');
    expect(calls[0]?.[1]).toEqual({
      accountId: 'acct-1',
      filter: { inMailbox: INBOX_ID },
      destroy: true,
    });
    expect(mail.listWholeMailboxSelected).toBe(false);
    expect(mail.bulkJob?.id).toBe('51');
  });

  it('emptyTrash calls Email/setByQuery with destroy: true scoped to the Trash mailbox regardless of the current folder (issue #179)', async () => {
    const { mail } = mailMod;
    // The current folder slice is Inbox, not Trash -- emptyTrash must
    // still scope its filter to the Trash mailbox explicitly rather than
    // via #buildCurrentFolderFilter (which would resolve to Inbox here).
    mail.listFolder = 'inbox';
    mail.listWholeMailboxSelected = false;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '52',
          matchedEstimate: 1200,
        }),
      ],
    });

    const result = await mail.emptyTrash();

    expect(jmapMod.jmap.batch).toHaveBeenCalledTimes(1);
    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    const calls: Array<[string, unknown]> = [];
    builder({
      call: (name: string, args: unknown) => {
        calls.push([name, args]);
        return { ref: () => ({}) };
      },
      usingSet: () => new Set(),
    });
    expect(calls[0]?.[0]).toBe('Email/setByQuery');
    expect(calls[0]?.[1]).toEqual({
      accountId: 'acct-1',
      filter: { inMailbox: TRASH_ID },
      destroy: true,
    });
    expect(result).toBe(-1);
    expect(mail.bulkJob?.id).toBe('52');
    expect(mail.bulkJob?.matchedEstimate).toBe(1200);
  });

  it('the EmailBulkJob push handler advances mail.bulkJob to done and stops polling', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '46',
          matchedEstimate: 3,
        }),
      ],
    });
    await mail.bulkArchive(['e1']);
    expect(mail.bulkJob?.status).toBe('running');

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('EmailBulkJob/get', {
          accountId: 'acct-1',
          list: [
            {
              id: '46',
              status: 'done',
              matchedEstimate: 3,
              processed: 3,
              total: 3,
              failedIds: [],
              errors: [],
            },
          ],
        }),
      ],
    });

    const handler = syncHandlers.get('EmailBulkJob');
    expect(handler).toBeDefined();
    // The registered handler fires `#pollBulkJob()` fire-and-forget (it
    // mirrors the real `sync.on` callback signature, which is
    // synchronous), so awaiting the handler call itself does not wait for
    // the async EmailBulkJob/get round-trip. Wait for the state to land.
    handler?.('7', 'acct-1');
    await vi.waitFor(() => expect(mail.bulkJob?.status).toBe('done'));

    expect(mail.bulkJob).toEqual({
      id: '46',
      status: 'done',
      matchedEstimate: 3,
      processed: 3,
      total: 3,
      failedIds: [],
      errors: [],
    });
  });

  it('the EmailBulkJob push handler surfaces a partial failure', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '47',
          matchedEstimate: 2,
        }),
      ],
    });
    await mail.bulkDelete(['e1']);

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('EmailBulkJob/get', {
          accountId: 'acct-1',
          list: [
            {
              id: '47',
              status: 'partial',
              matchedEstimate: 2,
              processed: 2,
              total: 2,
              failedIds: ['e9'],
              errors: ['overQuota'],
            },
          ],
        }),
      ],
    });

    const handler = syncHandlers.get('EmailBulkJob');
    handler?.('9', 'acct-1');
    await vi.waitFor(() => expect(mail.bulkJob?.status).toBe('partial'));

    expect(mail.bulkJob?.failedIds).toEqual(['e9']);
    expect(mail.bulkJob?.errors).toEqual(['overQuota']);
  });

  it('dismissBulkJob clears the banner state', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/setByQuery', {
          accountId: 'acct-1',
          jobId: '48',
          matchedEstimate: 1,
        }),
      ],
    });
    await mail.bulkArchive(['e1']);
    expect(mail.bulkJob).not.toBeNull();

    mail.dismissBulkJob();
    expect(mail.bulkJob).toBeNull();
  });

  it('falls back to the unavailable toast when the capability is absent', async () => {
    vi.mocked(jmapMod.jmap.hasCapability).mockReturnValue(false);
    const toastMod = await import('../toast/toast.svelte');
    const { mail } = mailMod;

    await mail.bulkArchive(['e1']);

    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });
});
