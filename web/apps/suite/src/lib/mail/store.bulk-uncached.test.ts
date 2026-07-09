/**
 * Regression coverage for issue #149's whole-mailbox bulk-action boundary.
 *
 * A prior fix attempt made bulkArchive/bulkDelete/bulkSetSeen/bulkMoveToMailbox
 * fetch the full folder ID list (fetchAllIds) and apply a single Email/set to
 * all of it when `listWholeMailboxSelected` is true. Production evidence
 * (issue #149 comment, ~666-message label) showed that single unchunked
 * Email/set blows REQ-PERF-DEADLINE-01's 1 s method deadline well before
 * mailbox scale -- there is no client-choosable cap that is both useful and
 * safe. The fix removes that path entirely: triggering a bulk action while
 * `listWholeMailboxSelected` is true now refuses with an explanatory toast
 * and issues no JMAP call at all, pending the server-side async job defined
 * in the issue #161 design comment.
 *
 * These tests assert the refusal: no Email/set (or any jmap.batch call) is
 * made, the toast fires, and the loaded-window ids passed in are left
 * untouched.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox, Email } from './types';

// ── Module-level mocks (must be before any dynamic import) ────────────────────

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn() },
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

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('whole-mailbox bulk actions refuse to execute (issue #149)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let toastMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  const INBOX_ID = 'mbox-inbox';
  const ARCHIVE_ID = 'mbox-archive';
  const TRASH_ID = 'mbox-trash';
  const LOADED_IDS = ['e1', 'e2', 'e3'];

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    jmapMod = await import('../jmap/client');
    toastMod = await import('../toast/toast.svelte');
    mailMod = await import('./store.svelte');
    const { mail } = mailMod;

    mail.mailboxes = new Map([
      [INBOX_ID, makeMailbox({ id: INBOX_ID, name: 'Inbox', role: 'inbox', totalThreads: 5 })],
      [ARCHIVE_ID, makeMailbox({ id: ARCHIVE_ID, name: 'Archive', role: 'archive' })],
      [TRASH_ID, makeMailbox({ id: TRASH_ID, name: 'Trash', role: 'trash' })],
    ]);

    for (const id of LOADED_IDS) {
      mail.emails.set(id, makeEmail(id, { [INBOX_ID]: true }));
    }

    mail.listFolder = 'inbox';
    mail.listEmailIds = [...LOADED_IDS];
    mail.listSelectedIds = new Set(LOADED_IDS);
    mail.listWholeMailboxSelected = true;
  });

  it('bulkArchive: issues no jmap call and shows the unavailable toast', async () => {
    const { mail } = mailMod;
    await mail.bulkArchive([...LOADED_IDS]);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });

  it('bulkDelete: issues no jmap call and shows the unavailable toast', async () => {
    const { mail } = mailMod;
    await mail.bulkDelete([...LOADED_IDS]);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });

  it('bulkDestroy: issues no jmap call and shows the unavailable toast', async () => {
    const { mail } = mailMod;
    await mail.bulkDestroy([...LOADED_IDS]);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });

  it('bulkSetSeen(true): issues no jmap call and shows the unavailable toast', async () => {
    const { mail } = mailMod;
    await mail.bulkSetSeen([...LOADED_IDS], true);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });

  it('bulkSetSeen(false): issues no jmap call and shows the unavailable toast', async () => {
    const { mail } = mailMod;
    await mail.bulkSetSeen([...LOADED_IDS], false);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });

  it('bulkSetLabel: issues no jmap call and shows the unavailable toast', async () => {
    const { mail } = mailMod;
    await mail.bulkSetLabel([...LOADED_IDS], ARCHIVE_ID, true);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });

  it('the loaded window is left untouched by a refused whole-mailbox action', async () => {
    const { mail } = mailMod;
    await mail.bulkArchive([...LOADED_IDS]);
    expect(mail.listEmailIds).toEqual(LOADED_IDS);
    for (const id of LOADED_IDS) {
      expect(mail.emails.get(id)?.mailboxIds).toEqual({ [INBOX_ID]: true });
    }
  });

  it('selectAllVisible clears whole-mailbox mode, after which bulkArchive executes normally', async () => {
    const { mail } = mailMod;
    mail.selectAllVisible(LOADED_IDS);
    expect(mail.listWholeMailboxSelected).toBe(false);

    vi.mocked(jmapMod.jmap.batch).mockResolvedValue({
      responses: [
        [
          'Email/set',
          { updated: Object.fromEntries(LOADED_IDS.map((id) => [id, {}])), notUpdated: null, newState: 's2' },
          'c0',
        ],
      ],
    });

    await mail.bulkArchive([...LOADED_IDS]);
    expect(jmapMod.jmap.batch).toHaveBeenCalled();
  });
});
