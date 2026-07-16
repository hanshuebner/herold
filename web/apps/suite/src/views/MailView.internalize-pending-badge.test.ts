/**
 * REQ-EXTIMG-BG-30 / REQ-EXTIMG-BG-64 — mailbox-row badge for messages
 * with `internalizePending = true`.
 *
 * The MailView row shows a small `<span class="internalize-pending-badge">`
 * carrying the locale-aware tooltip when the backend's per-message
 * `internalizePending` field is true. The badge clears when the push
 * handler refreshes the row.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';

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

  const EMAIL_PENDING = {
    id: 'e-pending',
    threadId: 'tid-pending',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.com' }],
    to: [{ name: 'Bob', email: 'bob@example.com' }],
    subject: 'Pending message',
    preview: 'External images still being processed.',
    receivedAt: '2026-05-09T10:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
    internalizePending: true,
  };

  const EMAIL_DONE = {
    id: 'e-done',
    threadId: 'tid-done',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Carol', email: 'carol@example.com' }],
    to: [{ name: 'Bob', email: 'bob@example.com' }],
    subject: 'Done message',
    preview: 'Body has been internalized.',
    receivedAt: '2026-05-09T11:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
    internalizePending: false,
  };

  const mbxMap = new Map([['mbx-inbox', INBOX_MBX]]);

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
    listEmailIds: [EMAIL_PENDING.id, EMAIL_DONE.id],
    get listEmails() {
      return [EMAIL_PENDING, EMAIL_DONE];
    },
    mailboxes: mbxMap,
    get customMailboxes() {
      return [];
    },
    emails: new Map([
      [EMAIL_PENDING.id, EMAIL_PENDING],
      [EMAIL_DONE.id, EMAIL_DONE],
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
    pruneSelectionToRendered: vi.fn(),
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
    threads: new Map<string, import('../lib/mail/types').Thread>(),
    threadEmails: vi.fn().mockReturnValue([]),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn().mockReturnValue(0),
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
      'mail.list.internalizePending.tooltip':
        'Images are being processed in the background. Refresh in a moment to see them.',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

describe('MailView internalize-pending badge (REQ-EXTIMG-BG-30)', () => {
  beforeEach(() => {
    routerState.folder = 'inbox';
    mailMock.listFolder = 'inbox';
  });

  it('renders a badge on the row whose email has internalizePending=true', () => {
    const { container } = render(MailView);
    const rows = container.querySelectorAll('.thread-row');
    expect(rows.length).toBeGreaterThanOrEqual(2);
    const pendingRow = rows[0]!;
    const badge = pendingRow.querySelector('.internalize-pending-badge');
    expect(badge).not.toBeNull();
    expect(badge?.getAttribute('title')).toBe(
      'Images are being processed in the background. Refresh in a moment to see them.',
    );
    expect(badge?.getAttribute('aria-label')).toBe(
      'Images are being processed in the background. Refresh in a moment to see them.',
    );
  });

  it('does not render a badge on a row whose email has internalizePending=false', () => {
    const { container } = render(MailView);
    const rows = container.querySelectorAll('.thread-row');
    const doneRow = rows[1]!;
    const badge = doneRow.querySelector('.internalize-pending-badge');
    expect(badge).toBeNull();
  });
});
