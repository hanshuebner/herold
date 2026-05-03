/**
 * Issue #64: multi-message threads should be decorated with message count.
 *
 * Thread list rows for threads with more than one message must show the
 * message count next to the sender name. Single-message threads must not
 * show a count.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// All fixtures must live inside vi.hoisted() so they are available when the
// vi.mock() factories run (which execute before the module-level code).
const { mailMock, routerState } = vi.hoisted(() => {
  const INBOX_MBX = {
    id: 'mbx-inbox',
    name: 'Inbox',
    role: 'inbox',
    parentId: null,
    sortOrder: 0,
    totalEmails: 2,
    unreadEmails: 0,
    totalThreads: 2,
    unreadThreads: 0,
  } as import('../lib/mail/types').Mailbox;

  const MULTI_MSG_EMAIL = {
    id: 'e-multi',
    threadId: 'tid-multi',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Olaf', email: 'olaf@example.com' }],
    to: [{ name: 'Me', email: 'me@example.com' }],
    subject: 'Multi-message thread',
    preview: 'Five messages in this thread.',
    receivedAt: '2024-01-15T10:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
  };

  const SINGLE_MSG_EMAIL = {
    id: 'e-single',
    threadId: 'tid-single',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.com' }],
    to: [{ name: 'Me', email: 'me@example.com' }],
    subject: 'Single-message thread',
    preview: 'Only one message here.',
    receivedAt: '2024-01-15T11:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
  };

  const mbxMap = new Map([['mbx-inbox', INBOX_MBX]]);

  const threadsMap = new Map([
    ['tid-multi', { id: 'tid-multi', emailIds: ['e-multi', 'e-multi-2', 'e-multi-3', 'e-multi-4', 'e-multi-5'] }],
    ['tid-single', { id: 'tid-single', emailIds: ['e-single'] }],
  ]);

  const routerState = { folder: 'inbox' as string };

  const mailMock = {
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listFolder: 'inbox' as string,
    get listFolderLabel() {
      return 'Inbox';
    },
    listSelectedIds: new Set<string>(),
    listEmailIds: [MULTI_MSG_EMAIL.id, SINGLE_MSG_EMAIL.id],
    get listEmails() {
      return [MULTI_MSG_EMAIL, SINGLE_MSG_EMAIL];
    },
    mailboxes: mbxMap,
    get customMailboxes() {
      return [];
    },
    threads: threadsMap,
    emails: new Map([
      [MULTI_MSG_EMAIL.id, MULTI_MSG_EMAIL],
      [SINGLE_MSG_EMAIL.id, SINGLE_MSG_EMAIL],
    ]),
    searchHistory: [] as string[],
    searchEmails: [] as unknown[],
    searchEmailIds: [] as string[],
    searchLoadStatus: 'idle' as const,
    searchError: null,
    searchFocusedIndex: -1,
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
    get searchQuery() {
      return '';
    },
    threadEmails: vi.fn().mockReturnValue([]),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
  };

  return { mailMock, routerState };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: { ask: vi.fn() } }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return ['mail', 'folder', routerState.folder];
    },
    matches(...prefix: string[]): boolean {
      const p = ['mail', 'folder', routerState.folder];
      return prefix.every((seg: string, i: number) => p[i] === seg);
    },
    navigate: vi.fn(),
    getParam: vi.fn().mockReturnValue(null),
    setParam: vi.fn(),
  },
}));

vi.mock('../lib/keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn().mockReturnValue(() => undefined) },
}));

vi.mock('../lib/compose/compose.svelte', () => ({
  compose: {
    openReply: vi.fn(),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
    openDraft: vi.fn(),
  },
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
  t: (key: string, args?: Record<string, unknown>): string => {
    const map: Record<string, string> = {
      'bulk.selected': `${String(args?.count ?? 0)} selected`,
      'bulk.archive': 'Archive',
      'bulk.markRead': 'Mark read',
      'bulk.markUnread': 'Mark unread',
      'bulk.move': 'Move...',
      'bulk.label': 'Label...',
      'bulk.category': 'Category...',
      'bulk.delete': 'Delete',
      'list.loading': 'Loading...',
      'list.refresh': 'Refresh',
      'list.retry': 'Retry',
      'list.emptyTrash': 'Empty Trash',
      'list.couldNotLoad': 'Could not load',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

// ── Tests ──────────────────────────────────────────────────────────────────────

describe('MailView thread count (re #64)', () => {
  beforeEach(() => {
    routerState.folder = 'inbox';
    mailMock.listFolder = 'inbox';
  });

  it('shows a count badge on the row for a multi-message thread', () => {
    const { container } = render(MailView);
    const rows = container.querySelectorAll('.thread-row');
    expect(rows.length).toBeGreaterThanOrEqual(2);

    // First row is the 5-message thread.
    const multiRow = rows[0]!;
    const countBadge = multiRow.querySelector('.thread-count');
    expect(countBadge).not.toBeNull();
    expect(countBadge!.textContent?.trim()).toBe('5');
  });

  it('does not show a count badge for a single-message thread', () => {
    const { container } = render(MailView);
    const rows = container.querySelectorAll('.thread-row');
    expect(rows.length).toBeGreaterThanOrEqual(2);

    // Second row is the single-message thread.
    const singleRow = rows[1]!;
    const countBadge = singleRow.querySelector('.thread-count');
    expect(countBadge).toBeNull();
  });

  it('count badge has an accessible aria-label', () => {
    const { container } = render(MailView);
    const countBadge = container.querySelector('.thread-count');
    expect(countBadge).not.toBeNull();
    expect(countBadge!.getAttribute('aria-label')).toBe('5 messages');
  });
});
