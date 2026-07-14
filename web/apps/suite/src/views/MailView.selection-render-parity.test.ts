/**
 * Regression coverage for issue #202 (the desync reported after the first
 * shift-click fix landed): the toolbar's selection count and the rendered
 * per-row checkboxes must never disagree.
 *
 * Root cause: `mail.listEmailIds` (the `Email/query` result) can contain an
 * id with no resolved `Email` yet in `mail.emails` (an in-flight page load,
 * a JMAP `notFound` for a since-deleted id, ...). Such an id renders no
 * `<li class="thread-row">` / checkbox. The folder list's shift-click and
 * select-all wiring used to pass the raw, unfiltered id list to
 * `mail.selectRowClick` / `mail.toggleSelectAllVisible`, so a range or a
 * "select all" could include ids the UI has no checkbox for -- the toolbar
 * then counts more ids than are checked on screen. Same defect, same fix,
 * for the search-result list's `mail.searchEmailIds` vs `mail.searchEmails`.
 *
 * These tests assert the *call-site wiring*: MailView must only ever pass
 * `selectRowClick` (and friends) an id list that is exactly the ids of the
 * rows actually rendered -- never a superset that includes an unresolved
 * id. Store-level range math is covered separately in
 * `lib/mail/store.selection.test.ts`.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { emails, selectRowClick, toggleSelectAllVisible, selectAllVisible } = vi.hoisted(() => {
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
  // `listEmailIds` deliberately holds one id ('ghost') with no matching
  // entry in `emails`, mirroring a page-load / notFound race -- the row it
  // would have produced never renders.
  const emails = new Map<string, Email>([
    ['e1', makeEmail('e1')],
    ['e2', makeEmail('e2')],
    ['e3', makeEmail('e3')],
  ]);
  return {
    emails,
    selectRowClick: vi.fn(),
    toggleSelectAllVisible: vi.fn(),
    selectAllVisible: vi.fn(),
  };
});

vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    listSelectedIds: new Set<string>(),
    listEmailIds: ['e1', 'e2', 'ghost', 'e3'],
    listLoadStatus: 'ready',
    listError: null,
    listFocusedIndex: -1,
    listEmails: [],
    listFolder: 'inbox',
    get listFolderLabel() {
      return 'Inbox';
    },
    mailboxes: new Map(),
    emails,
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
    selectRowClick,
    selectAllVisible,
    toggleSelectAllVisible,
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
    threadDedupeCount: vi.fn().mockReturnValue(1),
    customMailboxes: [],
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
      'mail.row.selectAria': 'Select message',
      'mail.row.starAria': 'Star',
      'mail.row.unstarAria': 'Unstar',
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

function dispatchShiftClick(el: Element): void {
  const ev = new MouseEvent('click', { bubbles: true, cancelable: true, shiftKey: true });
  el.dispatchEvent(ev);
}

describe('MailView folder list: selection wiring never outruns the rendered rows (re #202)', () => {
  it('renders exactly one row per resolved email, skipping the unresolved id', () => {
    render(MailView);
    const checkboxes = screen.getAllByLabelText('Select message');
    expect(checkboxes).toHaveLength(3);
  });

  it('shift-click passes selectRowClick only ids that have a rendered row', () => {
    render(MailView);
    const checkboxes = screen.getAllByLabelText('Select message');
    dispatchShiftClick(checkboxes[checkboxes.length - 1]!);

    expect(selectRowClick).toHaveBeenCalledTimes(1);
    const [, , visibleIds] = selectRowClick.mock.calls[0] as [string, boolean, string[]];
    expect(visibleIds).toEqual(['e1', 'e2', 'e3']);
    expect(visibleIds).not.toContain('ghost');
    // The invariant the bug violated: every passed id has a rendered
    // checkbox, so a range computed over it can never outrun what is on
    // screen.
    expect(visibleIds).toHaveLength(checkboxes.length);
  });

  it('the "*" select-all-visible keyboard action also excludes the unresolved id', async () => {
    const { keyboard } = await import('../lib/keyboard/engine.svelte');
    render(MailView);
    const layers = (keyboard.pushLayer as ReturnType<typeof vi.fn>).mock.calls.map(
      (call) => call[0],
    );
    const folderLayer = layers.find((layer) =>
      layer.some((b: { key: string }) => b.key === '*'),
    );
    const binding = folderLayer.find((b: { key: string }) => b.key === '*');
    binding.action();

    expect(toggleSelectAllVisible).toHaveBeenCalledWith(['e1', 'e2', 'e3']);
  });
});
