/**
 * Issue #53: label badges shown in front of subject in thread list rows.
 *
 * Emails that belong to one or more user-created ("custom") mailboxes should
 * display a small badge for each label in front of the subject text.  System
 * mailboxes (inbox, sent, trash, …) must NOT produce a badge.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// All fixtures must live inside vi.hoisted() so they are available when the
// vi.mock() factories run (which execute before the module-level code).
const { mailMock, routerState } = vi.hoisted(() => {
  const LABEL_MBX = { id: 'mbx-work', name: 'Work', role: null, parentId: null, sortOrder: 0, totalEmails: 1, unreadEmails: 0, totalThreads: 1, unreadThreads: 0 } as import('../lib/mail/types').Mailbox;
  const INBOX_MBX = { id: 'mbx-inbox', name: 'Inbox', role: 'inbox', parentId: null, sortOrder: 0, totalEmails: 1, unreadEmails: 0, totalThreads: 1, unreadThreads: 0 } as import('../lib/mail/types').Mailbox;

  const EMAIL_WITH_LABEL = {
    id: 'e-label',
    threadId: 'tid-label',
    mailboxIds: { 'mbx-inbox': true, 'mbx-work': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.com' }],
    to: [{ name: 'Bob', email: 'bob@example.com' }],
    subject: 'Labelled message',
    preview: 'This email has a Work label.',
    receivedAt: '2024-01-15T10:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
  };

  const EMAIL_SYSTEM_ONLY = {
    id: 'e-system',
    threadId: 'tid-system',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Bob', email: 'bob@example.com' }],
    to: [{ name: 'Alice', email: 'alice@example.com' }],
    subject: 'System only',
    preview: 'No custom label.',
    receivedAt: '2024-01-15T11:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
  };

  const mbxMap = new Map([
    ['mbx-inbox', INBOX_MBX],
    ['mbx-work', LABEL_MBX],
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
    listEmailIds: [EMAIL_WITH_LABEL.id, EMAIL_SYSTEM_ONLY.id],
    get listEmails() {
      return [EMAIL_WITH_LABEL, EMAIL_SYSTEM_ONLY];
    },
    mailboxes: mbxMap,
    get customMailboxes() {
      return [LABEL_MBX];
    },
    emails: new Map([
      [EMAIL_WITH_LABEL.id, EMAIL_WITH_LABEL],
      [EMAIL_SYSTEM_ONLY.id, EMAIL_SYSTEM_ONLY],
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
    get searchQuery() { return ''; },
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
  compose: { openReply: vi.fn(), openReplyAll: vi.fn(), openForward: vi.fn(), openDraft: vi.fn() },
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

describe('MailView label badges (re #53)', () => {
  beforeEach(() => {
    routerState.folder = 'inbox';
    mailMock.listFolder = 'inbox';
  });

  it('shows a label badge for a custom mailbox the email belongs to', () => {
    render(MailView);
    // "Work" is a custom (non-system) label — should appear as a badge.
    const badges = screen.getAllByText('Work');
    expect(badges.length).toBeGreaterThan(0);
  });

  it('does not show a badge for the inbox system mailbox', () => {
    render(MailView);
    // "Inbox" is a system-role mailbox and also the current folder — no badge.
    const inboxBadges = document.querySelectorAll('.label-badge');
    const inboxBadge = Array.from(inboxBadges).find((el) => el.textContent === 'Inbox');
    expect(inboxBadge).toBeUndefined();
  });

  it('renders the label badge before the subject text in the DOM', () => {
    const { container } = render(MailView);
    const subjectAndPreview = container.querySelector('.subject-and-preview');
    expect(subjectAndPreview).not.toBeNull();
    const children = Array.from(subjectAndPreview!.children);
    const badgeIdx = children.findIndex((el) => el.classList.contains('label-badge'));
    const subjectIdx = children.findIndex((el) => el.classList.contains('subject'));
    expect(badgeIdx).toBeGreaterThanOrEqual(0);
    expect(badgeIdx).toBeLessThan(subjectIdx);
  });

  it('does not show any label badge for an email without custom mailboxes', () => {
    // Only EMAIL_SYSTEM_ONLY has no custom label — check its row has no badge.
    const { container } = render(MailView);
    // Find all rows.  The second row is EMAIL_SYSTEM_ONLY.
    const rows = container.querySelectorAll('.thread-row');
    expect(rows.length).toBeGreaterThanOrEqual(2);
    const systemRow = rows[1]!;
    const badge = systemRow.querySelector('.label-badge');
    expect(badge).toBeNull();
  });
});
