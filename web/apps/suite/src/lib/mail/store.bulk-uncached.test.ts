/**
 * Regression test for issue #149: whole-mailbox bulk actions must include
 * UNCACHED emails (those not in the 50-row loaded window) in the JMAP payload.
 *
 * Root cause: bulkArchive, bulkDelete, and bulkSetSeen all had
 *   const e = this.emails.get(id); if (!e) continue;
 * which silently dropped uncached IDs from the Email/set update map.  Only the
 * JMAP IDs that happened to be in this.emails (the first 50 rows) were
 * archived/deleted/marked.
 *
 * Fix (issue #149 pass 2): restructure each method to build the JMAP payload
 * for ALL ids; for uncached ids skip the local optimistic patch but still add
 * to updates[id].  For cached ids retain the existing filter logic.
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

describe('whole-mailbox bulk actions include uncached emails (issue #149 regression)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  const INBOX_ID = 'mbox-inbox';
  const ARCHIVE_ID = 'mbox-archive';
  const TRASH_ID = 'mbox-trash';

  // Cached email IDs (in this.emails) — first 50-row window.
  const CACHED_IDS = ['c1', 'c2', 'c3'];
  // Uncached email IDs — beyond the loaded window (whole-mailbox fetch returns them).
  const UNCACHED_IDS = ['u1', 'u2'];
  const ALL_IDS = [...CACHED_IDS, ...UNCACHED_IDS];

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    jmapMod = await import('../jmap/client');
    mailMod = await import('./store.svelte');
    const { mail } = mailMod;

    // Seed mailboxes.
    mail.mailboxes = new Map([
      [INBOX_ID, makeMailbox({ id: INBOX_ID, name: 'Inbox', role: 'inbox', totalThreads: 5 })],
      [ARCHIVE_ID, makeMailbox({ id: ARCHIVE_ID, name: 'Archive', role: 'archive' })],
      [TRASH_ID, makeMailbox({ id: TRASH_ID, name: 'Trash', role: 'trash' })],
    ]);

    // Seed cached emails (in the loaded window).
    for (const id of CACHED_IDS) {
      mail.emails.set(id, makeEmail(id, { [INBOX_ID]: true }));
    }

    // Simulate whole-mailbox mode: all visible rows selected, then
    // listWholeMailboxSelected activated.
    mail.listFolder = 'inbox';
    mail.listEmailIds = [...CACHED_IDS];
    mail.listSelectedIds = new Set(CACHED_IDS);
    mail.listWholeMailboxSelected = true;
  });

  /** Helper: set up jmap.batch to:
   *   1st call (Email/query from fetchAllIds) → return ALL_IDS
   *   2nd call (Email/set) → capture the update/destroy payload and return success
   */
  function mockJmapForBulkUpdate(): { capturedUpdate: () => Record<string, unknown> } {
    const state = { update: {} as Record<string, unknown> };
    let callCount = 0;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    vi.mocked(jmapMod.jmap.batch).mockImplementation(async (builder: any) => {
      callCount++;
      if (callCount === 1) {
        // fetchAllIds → Email/query
        builder({
          call: (_name: string, _args: unknown) => ({ ref: () => null }),
        });
        return {
          responses: [['Email/query', { ids: ALL_IDS, total: ALL_IDS.length }, 'c0']],
          sessionState: 's1',
        };
      }
      // bulkAction → Email/set
      builder({
        call: (_name: string, args: Record<string, unknown>) => {
          if (args['update']) state.update = args['update'] as Record<string, unknown>;
          return { ref: () => null };
        },
      });
      const updated: Record<string, Record<string, unknown>> = {};
      for (const id of Object.keys(state.update)) updated[id] = {};
      return {
        responses: [['Email/set', { updated, notUpdated: null, newState: 's2' }, 'c1']],
        sessionState: 's2',
      };
    });
    return { capturedUpdate: () => state.update };
  }

  it('bulkArchive: includes all cached AND uncached IDs in the JMAP update payload', async () => {
    const { mail } = mailMod;
    const { capturedUpdate } = mockJmapForBulkUpdate();

    await mail.bulkArchive([...CACHED_IDS]);

    const updatedIds = Object.keys(capturedUpdate());
    expect(updatedIds.sort()).toEqual(ALL_IDS.sort());
    // Each entry must patch both inbox removal and archive addition.
    for (const id of ALL_IDS) {
      const patch = capturedUpdate()[id] as Record<string, unknown>;
      expect(patch[`mailboxIds/${INBOX_ID}`]).toBeNull();
      expect(patch[`mailboxIds/${ARCHIVE_ID}`]).toBe(true);
    }
  });

  it('bulkDelete: includes all cached AND uncached IDs in the JMAP update payload', async () => {
    const { mail } = mailMod;
    const { capturedUpdate } = mockJmapForBulkUpdate();

    await mail.bulkDelete([...CACHED_IDS]);

    const updatedIds = Object.keys(capturedUpdate());
    expect(updatedIds.sort()).toEqual(ALL_IDS.sort());
    for (const id of ALL_IDS) {
      const patch = capturedUpdate()[id] as Record<string, unknown>;
      expect((patch['mailboxIds'] as Record<string, unknown>)[TRASH_ID]).toBe(true);
    }
  });

  it('bulkSetSeen(true): includes all cached AND uncached IDs in the JMAP update payload', async () => {
    const { mail } = mailMod;
    const { capturedUpdate } = mockJmapForBulkUpdate();

    await mail.bulkSetSeen([...CACHED_IDS], true);

    const updatedIds = Object.keys(capturedUpdate());
    expect(updatedIds.sort()).toEqual(ALL_IDS.sort());
    for (const id of ALL_IDS) {
      const patch = capturedUpdate()[id] as Record<string, unknown>;
      expect(patch['keywords/$seen']).toBe(true);
    }
  });

  it('bulkSetSeen(false): includes all cached AND uncached IDs in the JMAP update payload', async () => {
    const { mail } = mailMod;
    // Mark all cached emails as seen so none are skipped by the wasSeen check.
    for (const id of CACHED_IDS) {
      mail.emails.get(id)!.keywords.$seen = true;
    }
    const { capturedUpdate } = mockJmapForBulkUpdate();

    await mail.bulkSetSeen([...CACHED_IDS], false);

    const updatedIds = Object.keys(capturedUpdate());
    expect(updatedIds.sort()).toEqual(ALL_IDS.sort());
    for (const id of ALL_IDS) {
      const patch = capturedUpdate()[id] as Record<string, unknown>;
      expect(patch['keywords/$seen']).toBeNull();
    }
  });
});
