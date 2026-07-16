/**
 * Search-list counterpart to MailView.selection-render-parity.test.ts
 * (re #202): `mail.searchEmailIds` can likewise contain an id with no
 * resolved `Email` in `mail.searchEmails`, and the search result rows
 * render from `mail.searchEmails` only. The search list's shift-click,
 * select-all, and SelectChooser wiring must all be scoped to
 * `mail.searchEmails`'s ids, never the raw `searchEmailIds`.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { makeEmail, selectRowClick, selectAllVisible, toggleSelectAllVisible, pruneSelectionToRendered } = vi.hoisted(() => {
  const makeEmail = (id: string) =>
    ({
      id,
      threadId: `thread-${id}`,
      mailboxIds: { 'mbx-inbox': true },
      keywords: { $seen: true },
      subject: `Subject ${id}`,
      preview: 'preview text',
      receivedAt: '2026-01-01T00:00:00Z',
      hasAttachment: false,
      attachments: [],
      reactions: [],
      snoozedUntil: null,
      from: [{ name: 'Alice', email: 'alice@example.test' }],
      to: null,
      cc: null,
      'header:List-ID:asText': null,
    }) as unknown as Email;
  return {
    makeEmail,
    selectRowClick: vi.fn(),
    selectAllVisible: vi.fn(),
    pruneSelectionToRendered: vi.fn(),
    toggleSelectAllVisible: vi.fn(),
  };
});

// searchEmailIds holds one id ('ghost') absent from searchEmails, mirroring
// a page-load / notFound race; that id's row never renders.
vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    listSelectedIds: new Set<string>(),
    listWholeMailboxSelected: false,
    listHasMore: false,
    listLoadingMore: false,
    listEmailIds: [],
    listLoadStatus: 'ready',
    listError: null,
    listFocusedIndex: -1,
    listEmails: [],
    listFolder: 'inbox',
    get listFolderLabel() {
      return 'Inbox';
    },
    mailboxes: new Map(),
    emails: new Map(),
    threads: new Map(),
    searchHistory: [],
    searchEmails: [makeEmail('s1'), makeEmail('s2')],
    searchEmailIds: ['s1', 'ghost', 's2'],
    searchLoadStatus: 'ready',
    searchError: null,
    searchFocusedIndex: -1,
    searchQuery: 'linus',
    searchTotal: null,
    loadFolder: vi.fn().mockResolvedValue(undefined),
    refreshFolder: vi.fn().mockResolvedValue(undefined),
    toggleSelected: vi.fn(),
    toggleSelectAllVisible,
    selectAllVisible,
    pruneSelectionToRendered,
    selectRowClick,
    selectWholeSearchResults: vi.fn(),
    clearSelection: vi.fn(),
    selectVisibleWhere: vi.fn(),
    bulkArchive: vi.fn().mockResolvedValue(undefined),
    bulkDelete: vi.fn().mockResolvedValue(undefined),
    bulkDestroy: vi.fn().mockResolvedValue(undefined),
    bulkSetSeen: vi.fn().mockResolvedValue(undefined),
    wholeMailboxActionUnavailable: vi.fn(),
    loadMoreFolder: vi.fn().mockResolvedValue(undefined),
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
    focusedSearchThreadId: vi.fn().mockReturnValue(null),
    markThreadSeen: vi.fn().mockResolvedValue(undefined),
    loadDraftBody: vi.fn().mockResolvedValue(undefined),
    emptyTrash: vi.fn().mockResolvedValue(undefined),
    restoreFromTrash: vi.fn().mockResolvedValue(undefined),
    clearSearchHistory: vi.fn(),
    runSearch: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn().mockReturnValue(1),
    threadEmails: vi.fn().mockReturnValue([]),
    customMailboxes: [],
  },
}));

vi.mock('../lib/router/router.svelte', () => {
  const parts = ['mail', 'search', 'linus'];
  return {
    router: {
      get parts() {
        return parts;
      },
      matches(...prefix: string[]): boolean {
        return prefix.every((seg, i) => parts[i] === seg);
      },
      navigate: vi.fn(),
      getParam: vi.fn().mockReturnValue(null),
      setParam: vi.fn(),
    },
  };
});

vi.mock('../lib/keyboard/engine.svelte', () => ({
  keyboard: {
    pushLayer: vi.fn().mockReturnValue(() => undefined),
  },
}));

vi.mock('../lib/compose/compose.svelte', () => ({
  compose: {
    openReply: vi.fn(),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
    openDraft: vi.fn(),
  },
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
      'bulk.selected': `${args?.count ?? 0} selected`,
      'select.selectAll': 'Select all',
      'select.deselectAll': 'Deselect all',
      'select.clearSelection': 'Clear selection',
      'select.options': 'Select options',
      'select.openMenu': 'Select...',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

function dispatchShiftClick(el: Element): void {
  const ev = new MouseEvent('click', { bubbles: true, cancelable: true, shiftKey: true });
  el.dispatchEvent(ev);
}

describe('MailView search results: selection wiring never outruns the rendered rows (re #202)', () => {
  it('renders exactly one row per resolved search result, skipping the unresolved id', () => {
    render(MailView);
    const checkboxes = screen.getAllByLabelText('Select message');
    expect(checkboxes).toHaveLength(2);
  });

  it('shift-click on a search result passes selectRowClick only rendered ids', () => {
    render(MailView);
    const checkboxes = screen.getAllByLabelText('Select message');
    dispatchShiftClick(checkboxes[checkboxes.length - 1]!);

    expect(selectRowClick).toHaveBeenCalledTimes(1);
    const [, , visibleIds] = selectRowClick.mock.calls[0] as [string, boolean, string[]];
    expect(visibleIds).toEqual(['s1', 's2']);
    expect(visibleIds).not.toContain('ghost');
  });
});
