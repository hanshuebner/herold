/**
 * Issue #29: delete-in-trash UX.
 *
 * Outside Trash: the Delete toolbar button should call bulkDelete (move to
 * trash) without showing a confirm dialog.
 *
 * Inside Trash: the Delete toolbar button should show a permanence-wording
 * confirm dialog, and only call bulkDestroy when the user confirms.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// Use vi.hoisted so the objects are available when vi.mock factories run.
const { mailMock, confirmMock, routerState } = vi.hoisted(() => {
  const mailMock = {
    listSelectedIds: new Set(['e1', 'e2']),
    listEmailIds: ['e1', 'e2'],
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listEmails: [] as unknown[],
    listFolder: 'inbox' as string,
    get listFolderLabel() {
      return this.listFolder === 'trash' ? 'Trash' : 'Inbox';
    },
    mailboxes: new Map(),
    emails: new Map(),
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
  };
  const confirmMock = { ask: vi.fn() };
  const routerState = { folder: 'inbox' as string };
  return { mailMock, confirmMock, routerState };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: confirmMock }));

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

/** Flush all queued microtasks so async click handlers finish. */
async function flushMicrotasks(): Promise<void> {
  await new Promise<void>((r) => setTimeout(r, 0));
}

// ── Tests ──────────────────────────────────────────────────────────────────────

describe('MailView delete-button: outside Trash (re #29)', () => {
  beforeEach(() => {
    mailMock.listFolder = 'inbox';
    routerState.folder = 'inbox';
    mailMock.bulkDelete.mockClear();
    mailMock.bulkDestroy.mockClear();
    confirmMock.ask.mockClear();
  });

  it('clicking Delete calls bulkDelete without showing a confirm dialog', async () => {
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Delete' });
    await fireEvent.click(btn);
    await flushMicrotasks();
    // No confirm prompt for move-to-trash outside Trash.
    expect(confirmMock.ask).not.toHaveBeenCalled();
    expect(mailMock.bulkDelete).toHaveBeenCalled();
    expect(mailMock.bulkDestroy).not.toHaveBeenCalled();
  });
});

describe('MailView delete-button: inside Trash (re #29)', () => {
  beforeEach(() => {
    mailMock.listFolder = 'trash';
    routerState.folder = 'trash';
    mailMock.bulkDelete.mockClear();
    mailMock.bulkDestroy.mockClear();
    confirmMock.ask.mockClear();
  });

  it('clicking Delete shows a danger confirm dialog with permanence wording', async () => {
    confirmMock.ask.mockResolvedValue(false); // user cancels
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Delete' });
    await fireEvent.click(btn);
    await flushMicrotasks();
    expect(confirmMock.ask).toHaveBeenCalledOnce();
    const req = confirmMock.ask.mock.calls[0]?.[0] as {
      message?: string;
      kind?: string;
    };
    expect(req.message).toMatch(/permanently/i);
    expect(req.message).toMatch(/can't be recovered/i);
    expect(req.kind).toBe('danger');
  });

  it('calls bulkDestroy (not bulkDelete) when user confirms the dialog', async () => {
    confirmMock.ask.mockResolvedValue(true); // user confirms
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Delete' });
    await fireEvent.click(btn);
    await flushMicrotasks();
    expect(mailMock.bulkDestroy).toHaveBeenCalled();
    expect(mailMock.bulkDelete).not.toHaveBeenCalled();
  });

  it('does NOT call bulkDestroy when user cancels the dialog', async () => {
    confirmMock.ask.mockResolvedValue(false); // user cancels
    render(MailView);
    const btn = screen.getByRole('button', { name: 'Delete' });
    await fireEvent.click(btn);
    await flushMicrotasks();
    expect(mailMock.bulkDestroy).not.toHaveBeenCalled();
    expect(mailMock.bulkDelete).not.toHaveBeenCalled();
  });
});
