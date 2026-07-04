/**
 * Unit tests for ThreadToolbar archive-action visibility (re #35).
 *
 * Covers three cases:
 *   (a) Inbox thread — Archive visible, bulkArchive called with inbox ids.
 *   (b) Own-reply thread — latest email is in Sent but earlier thread
 *       members are in the Inbox; Archive visible, bulkArchive called with
 *       the inbox-member ids (not the Sent email).
 *   (c) Fully-archived thread — no inbox members; Archive still visible
 *       but clicking it is a no-op (bulkArchive is NOT called, no
 *       navigation occurs).
 *   (d) Trash thread — Archive absent.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ThreadToolbar from './ThreadToolbar.svelte';
import type { Email, Mailbox } from './types';

// ── hoisted fixtures ───────────────────────────────────────────────────────────

const { mailMock, routerMock, INBOX_MBX, SENT_MBX, ARCHIVE_MBX, TRASH_MBX } = vi.hoisted(
  () => {
    const INBOX_MBX: Mailbox = {
      id: 'mbx-inbox',
      name: 'Inbox',
      role: 'inbox',
      parentId: null,
      sortOrder: 0,
      totalEmails: 0,
      unreadEmails: 0,
      totalThreads: 0,
      unreadThreads: 0,
    };

    const SENT_MBX: Mailbox = {
      id: 'mbx-sent',
      name: 'Sent',
      role: 'sentbox',
      parentId: null,
      sortOrder: 1,
      totalEmails: 0,
      unreadEmails: 0,
      totalThreads: 0,
      unreadThreads: 0,
    };

    const ARCHIVE_MBX: Mailbox = {
      id: 'mbx-archive',
      name: 'Archive',
      role: 'archive',
      parentId: null,
      sortOrder: 2,
      totalEmails: 0,
      unreadEmails: 0,
      totalThreads: 0,
      unreadThreads: 0,
    };

    const TRASH_MBX: Mailbox = {
      id: 'mbx-trash',
      name: 'Trash',
      role: 'trash',
      parentId: null,
      sortOrder: 3,
      totalEmails: 0,
      unreadEmails: 0,
      totalThreads: 0,
      unreadThreads: 0,
    };

    // mailMock is mutated per-test via mailMock.threadEmails and
    // mailMock.trash; the bulkArchive spy is shared across tests.
    const mailMock = {
      inbox: INBOX_MBX as Mailbox | null,
      trash: null as Mailbox | null,
      archive: ARCHIVE_MBX as Mailbox | null,
      listFolder: 'inbox' as string,
      threadEmails: (_tid: string): Email[] => [],
      bulkArchive: vi.fn().mockResolvedValue(undefined),
      bulkDelete: vi.fn().mockResolvedValue(undefined),
      restoreFromTrash: vi.fn().mockResolvedValue(undefined),
      markThreadSeen: vi.fn().mockResolvedValue(undefined),
      reportSpam: vi.fn().mockResolvedValue(undefined),
    };

    const routerMock = { parts: ['mail'] as readonly string[], navigate: vi.fn() };

    return { mailMock, routerMock, INBOX_MBX, SENT_MBX, ARCHIVE_MBX, TRASH_MBX };
  },
);

// ── module mocks ───────────────────────────────────────────────────────────────

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../router/router.svelte', () => ({ router: routerMock }));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));
vi.mock('./move-picker.svelte', () => ({ movePicker: { openBulk: vi.fn() } }));
vi.mock('./label-picker.svelte', () => ({ labelPicker: { openBulk: vi.fn() } }));
vi.mock('./snooze-picker.svelte', () => ({ snoozePicker: { open: vi.fn() } }));
vi.mock('../settings/managed-rules.svelte', () => ({
  managedRules: {
    isThreadMuted: () => false,
    muteThread: vi.fn().mockResolvedValue(undefined),
    unmuteThread: vi.fn().mockResolvedValue(undefined),
    blockSender: vi.fn().mockResolvedValue(undefined),
  },
}));

// Icon stubs — must be functions in Svelte 5.
vi.mock('../icons/ArchiveIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/TrashIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/RestoreIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/MarkUnreadIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/SnoozeIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/MoveIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/LabelIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/MuteIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/UnmuteIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/SpamIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/PhishingIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/BlockIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/PrintIcon.svelte', () => ({ default: () => null }));
vi.mock('./ActionOverflowMenu.svelte', () => ({ default: () => null }));

// ── helpers ────────────────────────────────────────────────────────────────────

function makeEmail(id: string, threadId: string, mailboxIds: Record<string, true>): Email {
  return {
    id,
    threadId,
    mailboxIds,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Sender', email: 'sender@example.test' }],
    to: null,
    cc: null,
    subject: 'Test',
    preview: 'preview',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    blobId: `blob-${id}`,
    'header:List-ID:asText': null,
  };
}

function renderToolbar(latest: Email, onPrint = vi.fn()) {
  return render(ThreadToolbar, {
    props: { threadId: latest.threadId, latest, onPrint },
  });
}

// ── tests ──────────────────────────────────────────────────────────────────────

describe('ThreadToolbar archive action visibility (re #35)', () => {
  beforeEach(() => {
    mailMock.inbox = INBOX_MBX;
    mailMock.trash = null;
    mailMock.archive = ARCHIVE_MBX;
    mailMock.listFolder = 'inbox';
    mailMock.bulkArchive.mockClear();
    routerMock.navigate.mockClear();
  });

  it('(a) shows Archive for an inbox thread and calls bulkArchive with inbox ids', async () => {
    const email = makeEmail('e-1', 'tid-1', { 'mbx-inbox': true });
    mailMock.threadEmails = (tid: string) => (tid === 'tid-1' ? [email] : []);

    renderToolbar(email);

    const archiveBtn = screen.getByRole('button', { name: 'thread.archive' });
    expect(archiveBtn).toBeInTheDocument();

    await fireEvent.click(archiveBtn);
    expect(mailMock.bulkArchive).toHaveBeenCalledWith(['e-1']);
  });

  it('(b) shows Archive for an own-reply thread and archives only inbox members', async () => {
    // Earlier message in Inbox; latest message (from user) is in Sent.
    const inboxMsg = makeEmail('e-inbox', 'tid-2', { 'mbx-inbox': true });
    const sentMsg = makeEmail('e-sent', 'tid-2', { 'mbx-sent': true });

    // threadEmails returns both; latest is the Sent message.
    mailMock.threadEmails = (tid: string) => (tid === 'tid-2' ? [inboxMsg, sentMsg] : []);

    renderToolbar(sentMsg);

    const archiveBtn = screen.getByRole('button', { name: 'thread.archive' });
    expect(archiveBtn).toBeInTheDocument();

    await fireEvent.click(archiveBtn);
    // Only the inbox member is passed to bulkArchive; the Sent message is excluded.
    expect(mailMock.bulkArchive).toHaveBeenCalledWith(['e-inbox']);
    expect(mailMock.bulkArchive).not.toHaveBeenCalledWith(expect.arrayContaining(['e-sent']));
  });

  it('(c) shows Archive for a fully-archived thread and performs a no-op on click', async () => {
    // Thread has no inbox members — it was already archived.
    const archivedMsg = makeEmail('e-arch', 'tid-3', { 'mbx-archive': true });
    mailMock.threadEmails = (tid: string) => (tid === 'tid-3' ? [archivedMsg] : []);

    renderToolbar(archivedMsg);

    const archiveBtn = screen.getByRole('button', { name: 'thread.archive' });
    expect(archiveBtn).toBeInTheDocument();

    await fireEvent.click(archiveBtn);
    // No network call and no navigation: the thread is already archived.
    expect(mailMock.bulkArchive).not.toHaveBeenCalled();
    expect(routerMock.navigate).not.toHaveBeenCalled();
  });

  it('(d) hides Archive when the thread is in Trash', () => {
    mailMock.trash = TRASH_MBX;
    const trashMsg = makeEmail('e-trash', 'tid-4', { 'mbx-trash': true });
    mailMock.threadEmails = (tid: string) => (tid === 'tid-4' ? [trashMsg] : []);

    renderToolbar(trashMsg);

    expect(screen.queryByRole('button', { name: 'thread.archive' })).not.toBeInTheDocument();
  });
});
