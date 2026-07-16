/**
 * Issue #246: a collapsed thread's list row must show the thread's stable
 * subject, not the subject of whichever message the server picked as the
 * representative. `Email/query` with `collapseThreads: true` keeps the
 * newest message per thread as the representative (RFC 8621 §4.4.3) -- when
 * that newest message is a delivery-failure bounce/DSN, its own subject
 * (and any earlier real subject) must not leak into the row; the row keeps
 * the base subject of the first thread member that has one, exactly like
 * `ThreadReader`'s header (`threadSubject()` over `mail.threadEmails()`).
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
    totalEmails: 1,
    unreadEmails: 0,
    totalThreads: 1,
    unreadThreads: 0,
  } as import('../lib/mail/types').Mailbox;

  // The representative email returned by the collapsed Email/query --
  // this is the bounce (newest message in the thread).
  const BOUNCE_EMAIL = {
    id: 'e-bounce',
    threadId: 'tid-1',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Mail Delivery Subsystem', email: 'mailer-daemon@example.com' }],
    to: [{ name: 'Me', email: 'me@example.com' }],
    subject: 'Mail delivery failed: returning message to sender',
    preview: 'This message was created automatically...',
    receivedAt: '2026-07-14T10:00:00Z',
    hasAttachment: true,
    snoozedUntil: null,
  };

  // Earlier thread members: the original message carries the real subject.
  const ORIGINAL_EMAIL = {
    ...BOUNCE_EMAIL,
    id: 'e-original',
    from: [{ name: 'Filip', email: 'filip@example.com' }],
    subject: 'Altos Chat',
    receivedAt: '2026-07-10T09:00:00Z',
    hasAttachment: false,
  };
  const REPLY_EMAIL = {
    ...BOUNCE_EMAIL,
    id: 'e-reply',
    from: [{ name: 'Me', email: 'me@example.com' }],
    subject: 'Re: Altos Chat',
    receivedAt: '2026-07-11T09:00:00Z',
    hasAttachment: false,
  };

  const mbxMap = new Map([['mbx-inbox', INBOX_MBX]]);
  const threadsMap = new Map([
    ['tid-1', { id: 'tid-1', emailIds: ['e-original', 'e-reply', 'e-bounce'] }],
  ]);
  const emailsById = new Map([
    ['e-original', ORIGINAL_EMAIL],
    ['e-reply', REPLY_EMAIL],
    ['e-bounce', BOUNCE_EMAIL],
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
    listEmailIds: [BOUNCE_EMAIL.id],
    get listEmails() {
      return [BOUNCE_EMAIL];
    },
    mailboxes: mbxMap,
    get customMailboxes() {
      return [];
    },
    threads: threadsMap,
    emails: emailsById,
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
    // Mirrors the store's real fallback: threadEmails() returns the live
    // thread's members (oldest first) when the thread has never been
    // cold-loaded (no committed snapshot yet).
    threadEmails: vi.fn((threadId: string) => {
      const t = threadsMap.get(threadId);
      if (!t) return [];
      return t.emailIds.map((id) => emailsById.get(id)).filter(Boolean);
    }),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn((threadId: string) => {
      const t = threadsMap.get(threadId);
      return t ? t.emailIds.length : 0;
    }),
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
      'list.loading': 'Loading...',
      'list.refresh': 'Refresh',
      'list.retry': 'Retry',
      'list.emptyTrash': 'Empty Trash',
      'list.couldNotLoad': 'Could not load',
      'mail.row.threadCountAria.other': `${String(args?.count ?? 0)} messages`,
      'mail.row.selectAria': 'Select message',
      'mail.row.starAria': 'Star',
      'mail.row.unstarAria': 'Unstar',
      'mail.list.actionsAria': 'List actions',
      'mail.list.threadsAria': `${String(args?.name ?? '')} threads`,
      'att.headerIcon.label': 'Has attachment',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

describe('MailView list row subject (re #246)', () => {
  beforeEach(() => {
    routerState.folder = 'inbox';
    mailMock.listFolder = 'inbox';
  });

  it('shows the thread base subject, not the collapsed representative bounce subject', () => {
    const { container } = render(MailView);
    const row = container.querySelector('.thread-row .subject');
    expect(row).not.toBeNull();
    expect(row!.textContent).toBe('Altos Chat');
    expect(row!.textContent).not.toContain('Mail delivery failed');
  });
});
