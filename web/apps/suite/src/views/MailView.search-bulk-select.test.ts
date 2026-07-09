/**
 * Issue #159: the search result list lacked bulk-select, drag-and-drop, and
 * bulk actions that the folder list has always had.
 *
 * Verifies that when the search route is active:
 *   - each result row renders the per-row checkbox and is draggable
 *   - the SelectChooser toolbar is mounted above the results
 *   - the bulk-action buttons appear once a result is selected and act on
 *     the search-result selection
 *   - the search keyboard layer registers `x` (toggle selection) and `*`
 *     (select/deselect all visible)
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { searchEmail, pushLayerSpy } = vi.hoisted(() => {
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
      attachments: [],
      reactions: [],
      snoozedUntil: null,
      from: [{ name: 'Alice', email: 'alice@example.test' }],
      to: null,
      cc: null,
      'header:List-ID:asText': null,
    }) as unknown as Email;
  return { searchEmail, pushLayerSpy: vi.fn().mockReturnValue(() => undefined) };
});

vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    listSelectedIds: new Set<string>(['e1']),
    listWholeMailboxSelected: false,
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
    searchEmails: [searchEmail('e1'), searchEmail('e2')],
    searchEmailIds: ['e1', 'e2'],
    searchLoadStatus: 'ready',
    searchError: null,
    searchFocusedIndex: -1,
    searchQuery: 'linus',
    loadFolder: vi.fn().mockResolvedValue(undefined),
    refreshFolder: vi.fn().mockResolvedValue(undefined),
    toggleSelected: vi.fn(),
    toggleSelectAllVisible: vi.fn(),
    selectAllVisible: vi.fn(),
    clearSelection: vi.fn(),
    selectVisibleWhere: vi.fn(),
    bulkArchive: vi.fn().mockResolvedValue(undefined),
    bulkDelete: vi.fn().mockResolvedValue(undefined),
    bulkDestroy: vi.fn().mockResolvedValue(undefined),
    bulkSetSeen: vi.fn().mockResolvedValue(undefined),
    fetchAllIds: vi.fn().mockResolvedValue([]),
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
    pushLayer: pushLayerSpy,
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
      'bulk.archive': 'Archive',
      'bulk.markRead': 'Mark read',
      'bulk.markUnread': 'Mark unread',
      'bulk.move': 'Move...',
      'bulk.label': 'Label...',
      'bulk.category': 'Category...',
      'bulk.delete': 'Delete',
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

describe('MailView search results share the folder list template (re #159)', () => {
  it('renders a per-row checkbox for each search result', () => {
    render(MailView);
    const checkboxes = screen.getAllByLabelText('Select message');
    expect(checkboxes).toHaveLength(2);
    expect(checkboxes[0]).toHaveAttribute('type', 'checkbox');
  });

  it('renders search result rows as draggable', () => {
    render(MailView);
    const rows = document.querySelectorAll('li.thread-row');
    expect(rows.length).toBe(2);
    for (const row of rows) {
      expect(row).toHaveAttribute('draggable', 'true');
    }
  });

  it('mounts the SelectChooser toolbar above the search results', () => {
    render(MailView);
    // The chevron menu button's accessible name is stable regardless of
    // the current selection state (unlike the check button, whose label
    // toggles between select-all / clear-selection).
    expect(screen.getByRole('button', { name: 'Select options' })).toBeInTheDocument();
  });

  it('renders the full bulk-action button set for selected search results', () => {
    render(MailView);
    expect(screen.getByRole('button', { name: 'Archive' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Mark read' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Mark unread' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Move...' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Label...' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument();
  });

  it('registers x (toggle selection) and * (select all visible) in the search keyboard layer', () => {
    render(MailView);
    const layers = pushLayerSpy.mock.calls.map((call) => call[0]);
    const searchLayer = layers.find((layer) =>
      layer.some((b: { key: string }) => b.key === 'j' && layer.some((c: { key: string }) => c.key === 'x')),
    );
    expect(searchLayer).toBeDefined();
    const keys = searchLayer!.map((b: { key: string }) => b.key);
    expect(keys).toContain('x');
    expect(keys).toContain('*');
  });
});
