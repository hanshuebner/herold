/**
 * Unit tests for ThreadToolbar (re #35, re #117).
 *
 * Archive-action visibility (re #35):
 *   (a) Inbox thread — Archive visible, bulkArchive called with inbox ids.
 *   (b) Own-reply thread — latest email is in Sent but earlier thread
 *       members are in the Inbox; Archive visible, bulkArchive called with
 *       the inbox-member ids (not the Sent email).
 *   (c) Fully-archived thread — no inbox members; Archive still visible;
 *       clicking it skips the network call (bulkArchive NOT called) but
 *       still performs post-action navigation (idempotent leave).
 *   (d) Trash thread — Archive absent.
 *
 * Formerly-overflow actions now shown as icon buttons (re #117):
 *   Verifies that Move, Labels, Mute, Spam, Phishing, Block sender, and
 *   Print each render as an accessible icon button in the toolbar and
 *   trigger their respective handlers.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
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

// Import the mocked singletons so we can assert on them.
import { movePicker } from './move-picker.svelte';
import { labelPicker } from './label-picker.svelte';
import { managedRules } from '../settings/managed-rules.svelte';

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

  it('(c) shows Archive for a fully-archived thread, skips network call, and navigates back', async () => {
    // Thread has no inbox members — it was already archived.
    const archivedMsg = makeEmail('e-arch', 'tid-3', { 'mbx-archive': true });
    mailMock.threadEmails = (tid: string) => (tid === 'tid-3' ? [archivedMsg] : []);

    renderToolbar(archivedMsg);

    const archiveBtn = screen.getByRole('button', { name: 'thread.archive' });
    expect(archiveBtn).toBeInTheDocument();

    await fireEvent.click(archiveBtn);
    // No network call: the thread is already archived (idempotent).
    expect(mailMock.bulkArchive).not.toHaveBeenCalled();
    // Navigation still happens: archive is idempotent but always leaves the thread.
    // routerMock carries no lastListPath, so navigateBackFromThread() falls
    // back to router.navigate('/mail').
    expect(routerMock.navigate).toHaveBeenCalledWith('/mail');
  });

  it('(d) hides Archive when the thread is in Trash', () => {
    mailMock.trash = TRASH_MBX;
    const trashMsg = makeEmail('e-trash', 'tid-4', { 'mbx-trash': true });
    mailMock.threadEmails = (tid: string) => (tid === 'tid-4' ? [trashMsg] : []);

    renderToolbar(trashMsg);

    expect(screen.queryByRole('button', { name: 'thread.archive' })).not.toBeInTheDocument();
  });
});

// ── Formerly-overflow actions rendered as icon buttons (re #117) ───────────────

describe('ThreadToolbar formerly-overflow actions (re #117)', () => {
  const email = makeEmail('e-1', 'tid-x', { 'mbx-inbox': true });

  beforeEach(() => {
    mailMock.inbox = INBOX_MBX;
    mailMock.trash = null;
    mailMock.threadEmails = (tid: string) => (tid === 'tid-x' ? [email] : []);
    vi.mocked(movePicker.openBulk).mockClear();
    vi.mocked(labelPicker.openBulk).mockClear();
    vi.mocked(managedRules.muteThread).mockClear();
    vi.mocked(managedRules.blockSender).mockClear();
    mailMock.reportSpam.mockClear();
  });

  it('renders Move as an icon button and calls movePicker.openBulk on click', async () => {
    renderToolbar(email);
    const btn = screen.getByRole('button', { name: 'thread.move' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    expect(vi.mocked(movePicker.openBulk)).toHaveBeenCalledWith([email.id]);
  });

  it('renders Labels as an icon button and calls labelPicker.openBulk on click', async () => {
    renderToolbar(email);
    const btn = screen.getByRole('button', { name: 'thread.label' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    expect(vi.mocked(labelPicker.openBulk)).toHaveBeenCalledWith([email.id]);
  });

  it('renders Mute as an icon button and calls managedRules.muteThread on click', async () => {
    renderToolbar(email);
    // isThreadMuted returns false, so label is msg.muteThread.
    const btn = screen.getByRole('button', { name: 'msg.muteThread' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    expect(vi.mocked(managedRules.muteThread)).toHaveBeenCalled();
  });

  it('renders Spam as an icon button and calls mail.reportSpam on click', async () => {
    renderToolbar(email);
    const btn = screen.getByRole('button', { name: 'msg.reportSpam' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    expect(mailMock.reportSpam).toHaveBeenCalledWith(email.id, 'spam');
  });

  it('renders Phishing as an icon button and calls mail.reportSpam with phishing on click', async () => {
    renderToolbar(email);
    const btn = screen.getByRole('button', { name: 'msg.reportPhishing' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    expect(mailMock.reportSpam).toHaveBeenCalledWith(email.id, 'phishing');
  });

  it('renders Block sender as an icon button and opens confirmation dialog on click', async () => {
    renderToolbar(email);
    const btn = screen.getByRole('button', { name: 'msg.blockSender' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    // Confirmation dialog should now be visible.
    expect(screen.getByRole('dialog', { name: 'Block sender' })).toBeInTheDocument();
  });

  it('renders Print as an icon button and calls onPrint on click', async () => {
    const onPrint = vi.fn();
    render(ThreadToolbar, { props: { threadId: email.threadId, latest: email, onPrint } });
    const btn = screen.getByRole('button', { name: 'thread.print' });
    expect(btn).toBeInTheDocument();
    await fireEvent.click(btn);
    expect(onPrint).toHaveBeenCalledOnce();
  });

  it('no ActionOverflowMenu ("Weitere Aktionen") trigger is present', () => {
    renderToolbar(email);
    // The overflow trigger had aria-label from t('actions.moreActions') = 'actions.moreActions'.
    expect(
      screen.queryByRole('button', { name: /actions\.moreActions/i }),
    ).not.toBeInTheDocument();
  });
});

// ── Standalone mode (thread-window popup, Bug C) ───────────────────────────────

describe('ThreadToolbar standalone mode', () => {
  let closeSpy: ReturnType<typeof vi.spyOn>;

  function renderStandaloneToolbar(latest: Email, onPrint = vi.fn()) {
    return render(ThreadToolbar, {
      props: { threadId: latest.threadId, latest, onPrint, standalone: true },
    });
  }

  beforeEach(() => {
    mailMock.inbox = INBOX_MBX;
    mailMock.trash = null;
    mailMock.archive = ARCHIVE_MBX;
    mailMock.listFolder = 'inbox';
    mailMock.bulkArchive.mockClear();
    routerMock.navigate.mockClear();
    closeSpy = vi.spyOn(window, 'close').mockImplementation(() => {});
  });

  afterEach(() => {
    closeSpy.mockRestore();
  });

  it('hides the back button in standalone mode', () => {
    const inboxMsg = makeEmail('e-1', 'tid-s1', { 'mbx-inbox': true });
    mailMock.threadEmails = () => [inboxMsg];
    renderStandaloneToolbar(inboxMsg);
    expect(screen.queryByRole('button', { name: 'thread.back' })).not.toBeInTheDocument();
  });

  it('shows the back button in non-standalone (default) mode', () => {
    const inboxMsg = makeEmail('e-1', 'tid-s2', { 'mbx-inbox': true });
    mailMock.threadEmails = () => [inboxMsg];
    renderToolbar(inboxMsg);
    expect(screen.getByRole('button', { name: 'thread.back' })).toBeInTheDocument();
  });

  it('archive on an inbox thread calls window.close() and not router.navigate()', async () => {
    const inboxMsg = makeEmail('e-1', 'tid-s3', { 'mbx-inbox': true });
    mailMock.threadEmails = () => [inboxMsg];
    renderStandaloneToolbar(inboxMsg);

    await fireEvent.click(screen.getByRole('button', { name: 'thread.archive' }));

    expect(mailMock.bulkArchive).toHaveBeenCalledWith(['e-1']);
    expect(closeSpy).toHaveBeenCalledOnce();
    expect(routerMock.navigate).not.toHaveBeenCalled();
  });

  it('archive on an already-archived thread skips network call but calls window.close()', async () => {
    // Thread is fully archived — inboxEmailIds will be empty.
    const archivedMsg = makeEmail('e-arch', 'tid-s4', { 'mbx-archive': true });
    mailMock.threadEmails = () => [archivedMsg];
    renderStandaloneToolbar(archivedMsg);

    await fireEvent.click(screen.getByRole('button', { name: 'thread.archive' }));

    expect(mailMock.bulkArchive).not.toHaveBeenCalled();
    expect(closeSpy).toHaveBeenCalledOnce();
    expect(routerMock.navigate).not.toHaveBeenCalled();
  });
});
