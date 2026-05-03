/**
 * REQ-UI-51: bulk-action toolbar uses icon-only buttons.
 *
 * Verifies that when messages are selected each bulk-action button:
 *   - is rendered as a <button> with role="button"
 *   - has no visible text label (icon-only)
 *   - carries an accessible name via aria-label that matches the
 *     previous text label (so getByRole('button', { name }) still works)
 *   - carries a matching title attribute (tooltip on hover)
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// ── Store mocks ──────────────────────────────────────────────────────────────

vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    listSelectedIds: new Set(['e1', 'e2']),
    listEmailIds: ['e1', 'e2'],
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
    searchEmails: [],
    searchEmailIds: [],
    searchLoadStatus: 'idle',
    searchError: null,
    searchFocusedIndex: -1,
    loadFolder: vi.fn().mockResolvedValue(undefined),
    refreshFolder: vi.fn().mockResolvedValue(undefined),
    toggleSelected: vi.fn(),
    selectAllVisible: vi.fn(),
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
    focusedSearchThreadId: vi.fn().mockReturnValue(null),
    markThreadSeen: vi.fn().mockResolvedValue(undefined),
    loadDraftBody: vi.fn().mockResolvedValue(undefined),
    emptyTrash: vi.fn().mockResolvedValue(undefined),
    restoreFromTrash: vi.fn().mockResolvedValue(undefined),
    clearSearchHistory: vi.fn(),
    runSearch: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock('../lib/router/router.svelte', () => {
  const parts = ['mail', 'folder', 'inbox'];
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
      'bulk.selected': `${args?.count ?? 0} selected`,
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

describe('MailView bulk-action toolbar (REQ-UI-51): icon-only buttons', () => {
  it('renders Archive as an icon-only button with aria-label and title', () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Archive' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-label', 'Archive');
    expect(btn).toHaveAttribute('title', 'Archive');
    // Icon-only: the button itself must have no plain text content.
    expect(btn.textContent?.trim()).toBe('');
  });

  it('renders Mark read as an icon-only button with aria-label and title', () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Mark read' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-label', 'Mark read');
    expect(btn).toHaveAttribute('title', 'Mark read');
    expect(btn.textContent?.trim()).toBe('');
  });

  it('renders Mark unread as an icon-only button with aria-label and title', () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Mark unread' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-label', 'Mark unread');
    expect(btn).toHaveAttribute('title', 'Mark unread');
    expect(btn.textContent?.trim()).toBe('');
  });

  it('renders Move as an icon-only button with aria-label and title', () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Move...' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-label', 'Move...');
    expect(btn).toHaveAttribute('title', 'Move...');
    expect(btn.textContent?.trim()).toBe('');
  });

  it('renders Label as an icon-only button with aria-label and title', () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Label...' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-label', 'Label...');
    expect(btn).toHaveAttribute('title', 'Label...');
    expect(btn.textContent?.trim()).toBe('');
  });

  it('renders Delete as an icon-only button with aria-label and title', () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Delete' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-label', 'Delete');
    expect(btn).toHaveAttribute('title', 'Delete');
    expect(btn.textContent?.trim()).toBe('');
  });
});
