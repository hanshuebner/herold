/**
 * Issue #497: a collapsed thread row is unread when any of its members in
 * the current folder is unread -- not just the collapsed-thread
 * representative (the newest message by the list sort). Reading only the
 * representative's `$seen` left a thread whose sole unread message was an
 * older one showing as read in the list, and contributing nothing to the
 * category-tab badge, while the server-side `Mailbox.unreadThreads` (the
 * sidebar and title figure) still counted it -- the count pointed at a row
 * the user had no way to find.
 *
 * `t2` mirrors the production repro from the issue: two Inbox members,
 * `e-old` (older, unread) and `e-new` (newer, seen, the collapsed
 * representative the `Email/query` result names).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { emails, threads, mailMock, categorySettingsMock } = vi.hoisted(() => {
  const INBOX_MBX = {
    id: 'mbx-inbox',
    name: 'Inbox',
    role: 'inbox',
    parentId: null,
    sortOrder: 0,
    totalEmails: 2,
    unreadEmails: 1,
    totalThreads: 1,
    unreadThreads: 1,
  } as import('../lib/mail/types').Mailbox;

  const makeEmail = (id: string, seen: boolean, receivedAt: string) =>
    ({
      id,
      threadId: 'tid-497',
      mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
      keywords: (seen ? { $seen: true } : {}) as Record<string, true | undefined>,
      from: [{ name: 'Filip', email: 'filip@example.local' }],
      to: [{ name: 'Alice', email: 'alice@example.local' }],
      subject: id === 'e-old' ? 'Thread 497 repro' : 'Re: Thread 497 repro',
      preview: 'preview text',
      receivedAt,
      hasAttachment: false,
      snoozedUntil: null,
    }) as unknown as Email;

  const emails = new Map<string, Email>([
    ['e-old', makeEmail('e-old', false, '2026-09-28T07:00:00Z')],
    ['e-new', makeEmail('e-new', true, '2026-09-28T07:05:00Z')],
  ]);
  const threads = new Map([['tid-497', { id: 'tid-497', emailIds: ['e-old', 'e-new'] }]]);

  const mbxMap = new Map([['mbx-inbox', INBOX_MBX]]);

  const mailMock = {
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listFolder: 'inbox' as string,
    listMailboxId: 'mbx-inbox' as string | null,
    get listFolderLabel() {
      return 'Inbox';
    },
    listSelectedIds: new Set<string>(),
    listEmailIds: ['e-new'],
    get listEmails() {
      return [emails.get('e-new')];
    },
    mailboxes: mbxMap,
    get customMailboxes() {
      return [];
    },
    threads,
    emails,
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
    threadEmails: vi.fn((threadId: string) => {
      const t = threads.get(threadId);
      if (!t) return [];
      return t.emailIds.map((id) => emails.get(id)).filter(Boolean);
    }),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn((threadId: string) => {
      const t = threads.get(threadId);
      return t ? t.emailIds.length : 0;
    }),
  };

  const categorySettingsMock = {
    available: false,
    derivedCategories: [] as string[],
    loadStatus: 'idle' as const,
    load: vi.fn().mockResolvedValue(undefined),
  };

  return { emails, threads, mailMock, categorySettingsMock };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: { ask: vi.fn() } }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return ['mail', 'folder', 'inbox'];
    },
    matches(...prefix: string[]): boolean {
      const p = ['mail', 'folder', 'inbox'];
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

// Real emailMatchesTab/categoryKeyword/categoryLabel logic; only the
// categorySettings singleton is replaced, so each test controls
// `available`/`derivedCategories` directly.
vi.mock('../lib/settings/category-settings.svelte', async () => {
  const actual = await vi.importActual<
    typeof import('../lib/settings/category-settings.svelte')
  >('../lib/settings/category-settings.svelte');
  return { ...actual, categorySettings: categorySettingsMock };
});

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
      'bulk.markRead': 'Mark read',
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
      'mail.list.tabsAria': 'Inbox categories',
      'mail.list.tabUnreadAria': `${String(args?.count ?? 0)} unread`,
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

describe('MailView thread row is unread when a non-representative member is unread (re #497)', () => {
  beforeEach(() => {
    mailMock.listSelectedIds = new Set<string>();
    mailMock.bulkSetSeen.mockClear();
    categorySettingsMock.available = false;
    categorySettingsMock.derivedCategories = [];
  });

  it('renders the row unread when the representative is seen but an older thread member is not', () => {
    const { container } = render(MailView);
    const row = container.querySelector('.thread-row');
    expect(row).not.toBeNull();
    expect(row!.classList.contains('unread')).toBe(true);
  });

  it('counts the thread once in the category-tab badge when a non-representative member is unread', () => {
    categorySettingsMock.available = true;
    // "Primary" must be present for the Primary tab (tabKey null) to
    // render at all -- the tab strip iterates derivedCategories directly.
    categorySettingsMock.derivedCategories = ['Primary', 'Updates'];
    render(MailView);
    // Primary tab (no `?tab=` param): the thread carries no category
    // keyword, so it matches Primary and the badge must read 1.
    expect(screen.getByLabelText('1 unread')).toBeInTheDocument();
  });

  it('expands the list-toolbar mark-read action to every unread member of the thread', async () => {
    mailMock.listSelectedIds = new Set<string>(['e-new']);
    render(MailView);
    const button = screen.getByRole('button', { name: 'Mark read' });
    await fireEvent.click(button);
    expect(mailMock.bulkSetSeen).toHaveBeenCalledTimes(1);
    const idsArg = mailMock.bulkSetSeen.mock.calls[0]![0] as string[];
    expect(new Set(idsArg)).toEqual(new Set(['e-new', 'e-old']));
  });
});
