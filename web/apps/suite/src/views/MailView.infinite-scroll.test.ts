/**
 * Issue #161: folder views must page past the first loaded window via
 * infinite scroll instead of capping at FOLDER_PAGE_SIZE (50) messages.
 *
 * These tests cover the MailView-side wiring: the bottom sentinel row
 * renders only while mail.listHasMore is true, the loading-indicator text
 * renders only while mail.listLoadingMore is true, and the
 * IntersectionObserver attached to the sentinel calls mail.loadMoreFolder()
 * when the sentinel becomes visible. Store-side paging logic (position
 * math, append, dedupe) is covered by store.load-more.test.ts.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';

const { mailMock, routerState, observerInstances } = vi.hoisted(() => {
  const INBOX_MBX = {
    id: 'mbx-inbox',
    name: 'Inbox',
    role: 'inbox',
    parentId: null,
    sortOrder: 0,
    totalEmails: 100,
    unreadEmails: 0,
    totalThreads: 100,
    unreadThreads: 0,
  } as import('../lib/mail/types').Mailbox;

  const EMAIL_1 = {
    id: 'e1',
    threadId: 't1',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.com' }],
    to: [{ name: 'Me', email: 'me@example.com' }],
    subject: 'First message',
    preview: 'preview',
    receivedAt: '2024-01-15T10:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
  };

  const mbxMap = new Map([['mbx-inbox', INBOX_MBX]]);
  const routerState = { folder: 'inbox' as string };

  // Captured IntersectionObserver instances so tests can invoke the
  // callback manually (happy-dom's stub never fires real intersections).
  const observerInstances: {
    callback: IntersectionObserverCallback;
    observe: ReturnType<typeof vi.fn>;
    disconnect: ReturnType<typeof vi.fn>;
  }[] = [];

  const mailMock = {
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listFolder: 'inbox' as string,
    get listFolderLabel() {
      return 'Inbox';
    },
    listSelectedIds: new Set<string>(),
    listWholeMailboxSelected: false,
    listFolderTotal: 100,
    listHasMore: false,
    listLoadingMore: false,
    listEmailIds: [EMAIL_1.id],
    get listEmails() {
      return [EMAIL_1];
    },
    mailboxes: mbxMap,
    get customMailboxes() {
      return [];
    },
    threads: new Map([['t1', { id: 't1', emailIds: [EMAIL_1.id] }]]),
    emails: new Map([[EMAIL_1.id, EMAIL_1]]),
    searchHistory: [] as string[],
    searchEmails: [] as unknown[],
    searchEmailIds: [] as string[],
    searchLoadStatus: 'idle' as const,
    searchError: null,
    searchFocusedIndex: -1,
    loadFolder: vi.fn().mockResolvedValue(undefined),
    refreshFolder: vi.fn().mockResolvedValue(undefined),
    loadMoreFolder: vi.fn().mockResolvedValue(undefined),
    wholeMailboxActionUnavailable: vi.fn(),
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
    threadEmails: vi.fn().mockReturnValue([]),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn().mockReturnValue(1),
  };

  return { mailMock, routerState, observerInstances };
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
      'list.loading': 'Loading...',
      'list.loadingMore': 'Loading more messages...',
      'list.refresh': 'Refresh',
      'list.retry': 'Retry',
      'list.emptyTrash': 'Empty Trash',
      'list.couldNotLoad': 'Could not load',
      'mail.list.actionsAria': 'List actions',
      'mail.list.threadsAria': `${String(args?.name ?? '')} threads`,
      'mail.row.selectAria': 'Select message',
      'mail.row.starAria': 'Star',
      'mail.row.unstarAria': 'Unstar',
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

describe('MailView infinite scroll (issue #161)', () => {
  let originalIntersectionObserver: typeof IntersectionObserver;

  beforeEach(() => {
    routerState.folder = 'inbox';
    mailMock.listFolder = 'inbox';
    mailMock.listHasMore = false;
    mailMock.listLoadingMore = false;
    mailMock.loadMoreFolder.mockClear();
    observerInstances.length = 0;

    originalIntersectionObserver = globalThis.IntersectionObserver;
    class FakeIntersectionObserver {
      callback: IntersectionObserverCallback;
      observe = vi.fn();
      disconnect = vi.fn();
      unobserve = vi.fn();
      constructor(callback: IntersectionObserverCallback) {
        this.callback = callback;
        observerInstances.push(this);
      }
    }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    globalThis.IntersectionObserver = FakeIntersectionObserver as any;
  });

  afterEach(() => {
    globalThis.IntersectionObserver = originalIntersectionObserver;
  });

  it('renders no sentinel row when listHasMore is false', () => {
    mailMock.listHasMore = false;
    const { container } = render(MailView);
    expect(container.querySelector('.load-more-sentinel')).toBeNull();
  });

  it('renders a sentinel row when listHasMore is true', () => {
    mailMock.listHasMore = true;
    const { container } = render(MailView);
    expect(container.querySelector('.load-more-sentinel')).not.toBeNull();
  });

  it('shows the loading-indicator text only while listLoadingMore is true', () => {
    mailMock.listHasMore = true;
    mailMock.listLoadingMore = true;
    const { container } = render(MailView);
    const indicator = container.querySelector('.load-more-indicator');
    expect(indicator).not.toBeNull();
    expect(indicator!.textContent).toContain('Loading more messages');
  });

  it('does not show loading-indicator text when not loading', () => {
    mailMock.listHasMore = true;
    mailMock.listLoadingMore = false;
    const { container } = render(MailView);
    expect(container.querySelector('.load-more-indicator')).toBeNull();
  });

  it('calls mail.loadMoreFolder() when the sentinel intersects', async () => {
    mailMock.listHasMore = true;
    render(MailView);

    expect(observerInstances.length).toBeGreaterThan(0);
    const observer = observerInstances[observerInstances.length - 1]!;
    observer.callback(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      observer as unknown as IntersectionObserver,
    );

    expect(mailMock.loadMoreFolder).toHaveBeenCalledTimes(1);
  });

  it('does not call mail.loadMoreFolder() when the sentinel is not intersecting', () => {
    mailMock.listHasMore = true;
    render(MailView);

    const observer = observerInstances[observerInstances.length - 1]!;
    observer.callback(
      [{ isIntersecting: false } as IntersectionObserverEntry],
      observer as unknown as IntersectionObserver,
    );

    expect(mailMock.loadMoreFolder).not.toHaveBeenCalled();
  });
});
