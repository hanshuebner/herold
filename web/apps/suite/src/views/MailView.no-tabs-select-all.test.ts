/**
 * Regression coverage for the resolved-row-aware gap in the "clear whole
 * mailbox" control with no category tabs active (re #202, follow-up after
 * independent verification).
 *
 * `mail.selectAllVisible()` -- called from the whole-mailbox-selection
 * banner's "Clear whole mailbox" button -- must receive only ids with a
 * resolved `Email` (`effectiveListEmailIds`), not the raw `listEmailIds`:
 * an id can be present in `mail.listEmailIds` (from the `Email/query`
 * result) before its `Email/get` companion lands, and such an id renders
 * no row. Handing it to `selectAllVisible` would desync the toolbar's
 * selection count from the rendered checked rows (re #202).
 *
 * `MailView.tab-aware-select-all.test.ts` covers the companion case where
 * a category tab is active -- the whole banner is withheld there (re
 * #255), so this file exercises only the no-category-tabs configuration
 * where the banner (and this button) remain reachable.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { emails, selectAllVisible } = vi.hoisted(() => {
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
  // e1, e2 are resolved; e3 is listed by the folder query but its
  // Email/get companion has not landed, so it is absent from `emails`.
  const emails = new Map<string, Email>([
    ['e1', makeEmail('e1')],
    ['e2', makeEmail('e2')],
  ]);
  return { emails, selectAllVisible: vi.fn() };
});

vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    listSelectedIds: new Set<string>(['e1', 'e2', 'e3']),
    listEmailIds: ['e1', 'e2', 'e3'],
    listLoadStatus: 'ready',
    listError: null,
    listFocusedIndex: -1,
    listEmails: [emails.get('e1'), emails.get('e2')],
    listFolder: 'inbox',
    listFolderTotal: 10,
    listFolderConversationTotal: 10,
    listHasMore: true,
    listWholeMailboxSelected: true,
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
    selectAllVisible,
    pruneSelectionToRendered: vi.fn(),
    selectWholeMailbox: vi.fn(),
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
    threadEmails: vi.fn().mockReturnValue([]),
    customMailboxes: [],
  },
}));

vi.mock('../lib/router/router.svelte', () => {
  const parts = ['mail'];
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

// Category tabs disabled -- the no-tabs configuration this file targets.
vi.mock('../lib/settings/category-settings.svelte', async () => {
  const actual = await vi.importActual<
    typeof import('../lib/settings/category-settings.svelte')
  >('../lib/settings/category-settings.svelte');
  return {
    ...actual,
    categorySettings: {
      available: false,
      derivedCategories: [],
      loadStatus: 'idle',
      load: vi.fn().mockResolvedValue(undefined),
    },
  };
});

vi.mock('../lib/mail/label-picker.svelte', () => ({
  labelPicker: { open: vi.fn(), openBulk: vi.fn() },
}));

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>): string => {
    const map: Record<string, string> = {
      'select.allPageSelected': `Alle ${args?.count ?? 0} ausgewaehlt-page`,
      'select.selectAllInFolder': `Alle ${args?.total ?? 0} auswaehlen`,
      'select.wholeMailboxActive': `Alle ${args?.total ?? 0} ausgewaehlt-whole`,
      'select.clearWholeMailbox': 'Clear whole mailbox',
      'list.loading': 'Loading...',
      'list.refresh': 'Refresh',
      'list.retry': 'Retry',
      'list.emptyTrash': 'Empty Trash',
      'list.couldNotLoad': 'Could not load',
      'mail.list.tabPrimary': 'Primary',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

describe('MailView "clear whole mailbox" is resolved-row-aware (re #202)', () => {
  it("passes only ids with a resolved Email to selectAllVisible, excluding an id not yet resolved", () => {
    render(MailView);

    const btn = screen.getByRole('button', { name: 'Clear whole mailbox' });
    fireEvent.click(btn);

    expect(selectAllVisible).toHaveBeenCalledTimes(1);
    const [visibleIds] = selectAllVisible.mock.calls[0] as [string[]];
    expect(visibleIds).toEqual(['e1', 'e2']);
    expect(visibleIds).not.toContain('e3');
  });
});
