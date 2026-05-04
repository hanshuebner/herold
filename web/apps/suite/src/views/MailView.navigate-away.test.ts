/**
 * Issue #29 (round 3): thread reader auto-navigate-away.
 *
 * When the user is viewing a thread from a folder (e.g. Trash) and a
 * Restore-from-Trash or Move-To-Mailbox operation removes all thread emails
 * from that folder, MailView should navigate back to the folder list
 * automatically.
 *
 * The effect runs whenever mail.threadEmails() or the folder mailbox id
 * changes. These tests render MailView in the thread-reader route with
 * the thread emails already reflecting the post-move state (i.e. no
 * email belongs to the current folder's mailbox) and assert that
 * router.navigate is called with the correct folder URL.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

// State shared between mock factories and tests.
const { mailMock, routerParts, navigate, TRASH_MAILBOX_ID, INBOX_MAILBOX_ID } = vi.hoisted(() => {
  const TRASH_MAILBOX_ID = 'mbx-trash';
  const INBOX_MAILBOX_ID = 'mbx-inbox';

  const emailInInbox = {
    id: 'email-1',
    threadId: 'thread-1',
    mailboxIds: { [INBOX_MAILBOX_ID]: true } as Record<string, true>,
    keywords: { $seen: true },
    subject: 'Test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: [],
    snoozedUntil: null,
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    'header:List-ID:asText': null,
  };

  const mailMock = {
    listSelectedIds: new Set<string>(),
    listEmailIds: [] as string[],
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listEmails: [] as unknown[],
    listFolder: 'trash' as string,
    get listFolderLabel() {
      return 'Trash';
    },
    mailboxes: new Map(),
    get customMailboxes() {
      return [];
    },
    emails: new Map([['email-1', emailInInbox]]),
    threads: new Map([
      ['thread-1', { emailIds: ['email-1'] }],
    ]),
    searchHistory: [] as string[],
    searchEmails: [] as unknown[],
    searchEmailIds: [] as string[],
    searchLoadStatus: 'idle' as const,
    searchError: null,
    searchFocusedIndex: -1,
    inbox: { id: INBOX_MAILBOX_ID, role: 'inbox', name: 'Inbox' },
    trash: { id: TRASH_MAILBOX_ID, role: 'trash', name: 'Trash' },
    sent: null as null,
    drafts: null as null,
    threadEmails: vi.fn((_tid: string): unknown[] => [emailInInbox]),
    threadStatus: vi.fn().mockReturnValue('ready'),
    threadError: vi.fn().mockReturnValue(null),
    loadFolder: vi.fn().mockResolvedValue(undefined),
    refreshFolder: vi.fn().mockResolvedValue(undefined),
    toggleSelected: vi.fn(),
    selectAllVisible: vi.fn(),
    toggleSelectAllVisible: vi.fn(),
    bulkArchive: vi.fn().mockResolvedValue(undefined),
    bulkDelete: vi.fn().mockResolvedValue(undefined),
    bulkDestroy: vi.fn().mockResolvedValue(undefined),
    bulkSetSeen: vi.fn().mockResolvedValue(undefined),
    archiveEmail: vi.fn().mockResolvedValue(undefined),
    deleteEmail: vi.fn().mockResolvedValue(undefined),
    destroyEmail: vi.fn().mockResolvedValue(undefined),
    setSeen: vi.fn().mockResolvedValue(undefined),
    toggleFlagged: vi.fn().mockResolvedValue(undefined),
    toggleImportant: vi.fn().mockResolvedValue(undefined),
    focusListNext: vi.fn(),
    focusListPrev: vi.fn(),
    focusSearchNext: vi.fn(),
    focusSearchPrev: vi.fn(),
    focusedListThreadId: vi.fn().mockReturnValue(null),
    focusedSearchThreadId: vi.fn().mockReturnValue(null),
    markThreadSeen: vi.fn().mockResolvedValue(undefined),
    loadDraftBody: vi.fn().mockResolvedValue(undefined),
    emptyTrash: vi.fn().mockResolvedValue(0),
    restoreFromTrash: vi.fn().mockResolvedValue(undefined),
    clearSearchHistory: vi.fn(),
    runSearch: vi.fn().mockResolvedValue(undefined),
    snoozeEmail: vi.fn().mockResolvedValue(undefined),
    unsnoozeEmail: vi.fn().mockResolvedValue(undefined),
    setCategoryKeyword: vi.fn().mockResolvedValue(undefined),
    bulkMoveToMailbox: vi.fn().mockResolvedValue(undefined),
    bulkSetLabel: vi.fn().mockResolvedValue(undefined),
    loadThread: vi.fn().mockResolvedValue(undefined),
  };

  const navigate = vi.fn();
  // Route: /mail/thread/thread-1 — thread reader is open.
  const routerParts = ['mail', 'thread', 'thread-1'];

  return { mailMock, routerParts, navigate, TRASH_MAILBOX_ID, INBOX_MAILBOX_ID };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return routerParts;
    },
    matches(...prefix: string[]): boolean {
      return prefix.every((seg: string, i: number) => routerParts[i] === seg);
    },
    navigate,
    getParam: vi.fn().mockReturnValue(null),
    setParam: vi.fn(),
  },
}));

vi.mock('../lib/keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn().mockReturnValue(() => undefined) },
}));

vi.mock('../lib/compose/compose.svelte', () => ({
  compose: { openReply: vi.fn(), openReplyAll: vi.fn(), openForward: vi.fn(), openDraft: vi.fn() },
}));

vi.mock('../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn().mockResolvedValue(false) },
}));

vi.mock('../lib/mail/move-picker.svelte', () => ({
  movePicker: { open: vi.fn(), openBulk: vi.fn() },
}));

vi.mock('../lib/mail/snooze-picker.svelte', () => ({
  snoozePicker: { open: vi.fn() },
}));

vi.mock('../lib/mail/category-picker.svelte', () => ({
  categoryPicker: { open: vi.fn() },
}));

vi.mock('../lib/settings/category-settings.svelte', () => ({
  categorySettings: {
    available: false,
    derivedCategories: [],
    loadStatus: 'idle',
    load: vi.fn().mockResolvedValue(undefined),
  },
  emailMatchesTab: vi.fn().mockReturnValue(true),
  categoryKeyword: vi.fn().mockReturnValue(null),
}));

vi.mock('../lib/mail/label-picker.svelte', () => ({
  labelPicker: { open: vi.fn(), openBulk: vi.fn() },
}));

vi.mock('../lib/mail/dnd-thread.svelte', () => ({
  threadDnd: { current: null, begin: vi.fn(), end: vi.fn() },
  dragIdsForRow: vi.fn().mockReturnValue([]),
}));

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

vi.mock('../lib/mail/ThreadReader.svelte', () => ({
  default: vi.fn().mockReturnValue(null),
}));

import MailView from './MailView.svelte';

describe('MailView: auto-navigate away when thread email leaves current folder (re #29)', () => {
  beforeEach(() => {
    navigate.mockClear();
    mailMock.threadEmails.mockClear();
  });

  it('navigates to the trash folder when no thread email is in trash anymore', () => {
    mailMock.listFolder = 'trash';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [INBOX_MAILBOX_ID]: true }, // moved to inbox, no longer in trash
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).toHaveBeenCalledWith('/mail/folder/trash');
  });

  it('does NOT navigate when the thread email is still in trash', () => {
    mailMock.listFolder = 'trash';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [TRASH_MAILBOX_ID]: true }, // still in trash
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  it('does NOT navigate when thread is still loading (no emails yet)', () => {
    mailMock.listFolder = 'trash';
    mailMock.threadEmails.mockReturnValue([]); // thread not loaded yet

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  it('navigates to inbox (at /mail) when viewing inbox and email leaves', () => {
    mailMock.listFolder = 'inbox';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [TRASH_MAILBOX_ID]: true }, // moved to trash, not in inbox
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).toHaveBeenCalledWith('/mail');
  });

  it('does NOT navigate for the "all" virtual folder even if email mailboxIds change', () => {
    mailMock.listFolder = 'all';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { 'some-other-mailbox': true },
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  it('navigates to a custom folder URL when viewing a custom mailbox', () => {
    mailMock.listFolder = 'mbx-work';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [INBOX_MAILBOX_ID]: true }, // not in 'mbx-work'
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).toHaveBeenCalledWith('/mail/folder/mbx-work');
  });
});
