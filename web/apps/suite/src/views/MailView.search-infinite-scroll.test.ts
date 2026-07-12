/**
 * Issue #219: the search result list must page past the first loaded
 * window via infinite scroll instead of capping at 50 matches, mirroring
 * the folder list's infinite scroll (issue #161, MailView.infinite-scroll.
 * test.ts).
 *
 * These tests cover the MailView-side wiring for the search route: the
 * bottom sentinel row renders only while mail.searchHasMore is true, the
 * loading-indicator text renders only while mail.searchLoadingMore is
 * true, and the IntersectionObserver attached to the sentinel calls
 * mail.loadMoreSearch() when the sentinel becomes visible. Store-side
 * paging logic (position math, append, dedupe) is covered by
 * store.search-load-more.test.ts.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { mailMock, observerInstances } = vi.hoisted(() => {
  const searchEmail = (id: string) =>
    ({
      id,
      threadId: `thread-${id}`,
      mailboxIds: { 'mbx-inbox': true },
      keywords: { $seen: true },
      subject: `Subject ${id}`,
      preview: 'preview text',
      receivedAt: '2026-01-01T00:00:00Z',
      hasAttachment: false,
      snoozedUntil: null,
      from: [{ name: 'Alice', email: 'alice@example.test' }],
      to: null,
    }) as unknown as Email;

  // Captured IntersectionObserver instances so tests can invoke the
  // callback manually (happy-dom's stub never fires real intersections).
  const observerInstances: {
    callback: IntersectionObserverCallback;
    observe: ReturnType<typeof vi.fn>;
    disconnect: ReturnType<typeof vi.fn>;
  }[] = [];

  const searchEmails = [searchEmail('e1'), searchEmail('e2')];

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
    listFolderTotal: null as number | null,
    listHasMore: false,
    listLoadingMore: false,
    listEmailIds: [] as string[],
    get listEmails() {
      return [];
    },
    mailboxes: new Map(),
    get customMailboxes() {
      return [];
    },
    threads: new Map(),
    emails: new Map(searchEmails.map((e) => [e.id, e])),
    searchHistory: [] as string[],
    searchEmails,
    searchEmailIds: searchEmails.map((e) => e.id),
    searchLoadStatus: 'ready' as const,
    searchError: null,
    searchFocusedIndex: -1,
    searchQuery: 'linus',
    searchTotal: null as number | null,
    searchHasMore: false,
    searchLoadingMore: false,
    loadFolder: vi.fn().mockResolvedValue(undefined),
    refreshFolder: vi.fn().mockResolvedValue(undefined),
    loadMoreFolder: vi.fn().mockResolvedValue(undefined),
    loadMoreSearch: vi.fn().mockResolvedValue(undefined),
    wholeMailboxActionUnavailable: vi.fn(),
    toggleSelected: vi.fn(),
    selectAllVisible: vi.fn(),
    selectWholeSearchResults: vi.fn(),
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
    threadEmails: vi.fn().mockReturnValue([]),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn().mockReturnValue(1),
  };

  return { mailMock, observerInstances };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: { ask: vi.fn() } }));

vi.mock('../lib/router/router.svelte', () => {
  const parts = ['mail', 'search', 'linus'];
  return {
    router: {
      get parts() {
        return parts;
      },
      matches(...prefix: string[]): boolean {
        return prefix.every((seg: string, i: number) => parts[i] === seg);
      },
      navigate: vi.fn(),
      getParam: vi.fn().mockReturnValue(null),
      setParam: vi.fn(),
    },
  };
});

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
      'mail.search.heading': 'Search:',
      'mail.search.empty': '(empty)',
      'mail.search.backToInbox': 'Back to inbox',
      'mail.search.recognisedQueryAria': 'Recognised query',
      'mail.search.recentSearchesAria': 'Recent searches',
      'mail.search.resultsAria': 'Search results',
      'mail.search.noMatches': 'No matches.',
      'mail.list.actionsAria': 'List actions',
      'mail.row.selectAria': 'Select message',
      'mail.row.starAria': 'Star',
      'mail.row.unstarAria': 'Unstar',
      'list.loadingMore': 'Loading more messages...',
      'att.headerIcon.label': 'Has attachment',
      'bulk.selected': `${String(args?.count ?? 0)} selected`,
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));
vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

describe('MailView search-result infinite scroll (issue #219)', () => {
  let originalIntersectionObserver: typeof IntersectionObserver;

  beforeEach(() => {
    mailMock.searchHasMore = false;
    mailMock.searchLoadingMore = false;
    mailMock.loadMoreSearch.mockClear();
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

  it('renders no sentinel row when searchHasMore is false', () => {
    mailMock.searchHasMore = false;
    const { container } = render(MailView);
    expect(container.querySelector('.load-more-sentinel')).toBeNull();
  });

  it('renders a sentinel row when searchHasMore is true', () => {
    mailMock.searchHasMore = true;
    const { container } = render(MailView);
    expect(container.querySelector('.load-more-sentinel')).not.toBeNull();
  });

  it('shows the loading-indicator text only while searchLoadingMore is true', () => {
    mailMock.searchHasMore = true;
    mailMock.searchLoadingMore = true;
    const { container } = render(MailView);
    const indicator = container.querySelector('.load-more-indicator');
    expect(indicator).not.toBeNull();
    expect(indicator!.textContent).toContain('Loading more messages');
  });

  it('does not show loading-indicator text when not loading', () => {
    mailMock.searchHasMore = true;
    mailMock.searchLoadingMore = false;
    const { container } = render(MailView);
    expect(container.querySelector('.load-more-indicator')).toBeNull();
  });

  it('calls mail.loadMoreSearch() when the sentinel intersects', async () => {
    mailMock.searchHasMore = true;
    render(MailView);

    expect(observerInstances.length).toBeGreaterThan(0);
    const observer = observerInstances[observerInstances.length - 1]!;
    observer.callback(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      observer as unknown as IntersectionObserver,
    );

    expect(mailMock.loadMoreSearch).toHaveBeenCalledTimes(1);
  });

  it('does not call mail.loadMoreSearch() when the sentinel is not intersecting', () => {
    mailMock.searchHasMore = true;
    render(MailView);

    const observer = observerInstances[observerInstances.length - 1]!;
    observer.callback(
      [{ isIntersecting: false } as IntersectionObserverEntry],
      observer as unknown as IntersectionObserver,
    );

    expect(mailMock.loadMoreSearch).not.toHaveBeenCalled();
  });

  it('does not call mail.loadMoreFolder() from the search-route sentinel', () => {
    mailMock.searchHasMore = true;
    render(MailView);

    const observer = observerInstances[observerInstances.length - 1]!;
    observer.callback(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      observer as unknown as IntersectionObserver,
    );

    expect(mailMock.loadMoreFolder).not.toHaveBeenCalled();
  });
});
