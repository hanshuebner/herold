/**
 * Issue #495: closing a thread opened from a non-Primary category tab must
 * return to that tab, not drop back to bare `/mail` (Primary).
 *
 * The thread reader's "back to folder" keyboard shortcuts (`Escape` and `u`)
 * used to call `router.navigate(folderHref(mail.listFolder))` directly --
 * `folderHref('inbox')` always returns the bare `/mail`, with no `tab`
 * query parameter, so pressing either key while viewing a thread opened
 * from `/mail?tab=forums` bounced the user to Primary. Both bindings now go
 * through `navigateBackFromThread()`, which pushes `router.lastListPath`
 * (the tab-qualified route the thread was opened from) instead.
 *
 * The auto-navigate-away `$effect`'s own fix (routing its fallback through
 * the same `navigateBackFromThread()` call instead of a bare
 * `folderHref(currentFolder)`) is not unit-testable here: `confirmedFolderKey`
 * is a plain (non-reactive) component variable and the mocked `mail` store
 * below carries no Svelte `$state`, so mutating it between assertions does
 * not re-trigger the effect -- see `MailView.navigate-away.test.ts`'s own
 * docstring for the same constraint. That transition is covered end-to-end
 * by `tests/e2e-live/category-tab-thread-close-return.spec.ts` against a
 * real backend.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { mailMock, routerMock, pushedLayers } = vi.hoisted(() => {
  const INBOX_MAILBOX_ID = 'mbx-inbox';

  const emailInInbox = {
    id: 'email-1',
    threadId: 'thread-1',
    mailboxIds: { [INBOX_MAILBOX_ID]: true } as Record<string, true>,
    keywords: { $seen: true },
    subject: 'Test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: [],
    snoozedUntil: null,
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    'header:List-ID:asText': null,
  };

  const mailMock = {
    listSelectedIds: new Set<string>(),
    listEmailIds: [] as string[],
    listLoadStatus: 'ready' as 'idle' | 'loading' | 'ready' | 'error',
    listError: null,
    listFocusedIndex: -1,
    listEmails: [] as unknown[],
    listFolder: 'inbox' as string,
    get listFolderLabel() {
      return 'Inbox';
    },
    mailboxes: new Map(),
    get customMailboxes() {
      return [];
    },
    emails: new Map([['email-1', emailInInbox]]),
    threads: new Map([['thread-1', { emailIds: ['email-1'] }]]),
    searchHistory: [] as string[],
    searchEmails: [] as unknown[],
    searchEmailIds: [] as string[],
    searchLoadStatus: 'idle' as const,
    searchError: null,
    searchFocusedIndex: -1,
    inbox: { id: INBOX_MAILBOX_ID, role: 'inbox', name: 'Inbox' },
    trash: null as null,
    sent: null as null,
    drafts: null as null,
    threadEmails: vi.fn((_tid: string): unknown[] => [emailInInbox]),
    threadStatus: vi.fn().mockReturnValue('ready'),
    threadError: vi.fn().mockReturnValue(null),
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
    loadThread: vi.fn().mockResolvedValue(undefined),
    threadDedupeCount: vi.fn().mockReturnValue(0),
  };

  const routerMock = {
    parts: ['mail', 'thread', 'thread-1'] as readonly string[],
    lastListPath: null as string | null,
    navigate: vi.fn(),
    getParam: vi.fn().mockReturnValue(null),
    setParam: vi.fn(),
    matches(...prefix: string[]): boolean {
      return prefix.every((seg: string, i: number) => routerMock.parts[i] === seg);
    },
  };

  const pushedLayers: Array<Array<{ key: string; description: string; action: () => void }>> = [];

  return { mailMock, routerMock, pushedLayers };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/router/router.svelte', () => ({ router: routerMock }));

vi.mock('../lib/keyboard/engine.svelte', () => ({
  keyboard: {
    pushLayer: vi.fn((layer: Array<{ key: string; description: string; action: () => void }>) => {
      pushedLayers.push(layer);
      return () => undefined;
    }),
  },
}));

vi.mock('../lib/compose/compose.svelte', () => ({
  compose: { openReply: vi.fn(), openReplyAll: vi.fn(), openForward: vi.fn(), openDraft: vi.fn() },
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

vi.mock('../lib/mail/dnd-thread.svelte', () => ({
  threadDnd: { current: null, begin: vi.fn(), end: vi.fn() },
  dragIdsForRow: vi.fn().mockReturnValue([]),
}));

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

vi.mock('../lib/mail/ThreadReader.svelte', () => ({
  default: vi.fn().mockReturnValue(null),
}));

import MailView from './MailView.svelte';

/** Find the "Back to <folder>" binding for `key` across every layer pushed
 *  onto the keyboard stack while the thread reader was mounted. */
function findBackBinding(key: string) {
  for (const layer of pushedLayers) {
    const binding = layer.find((b) => b.key === key && b.description.startsWith('Back to'));
    if (binding) return binding;
  }
  return undefined;
}

describe('MailView thread-reader keyboard shortcuts preserve the originating tab (re #495)', () => {
  beforeEach(() => {
    routerMock.navigate.mockClear();
    routerMock.parts = ['mail', 'thread', 'thread-1'];
    routerMock.lastListPath = '/mail?tab=forums';
    pushedLayers.length = 0;
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: { $seen: true },
      } as unknown as Email,
    ]);
  });

  it('Escape navigates to the tab-qualified lastListPath, not a bare folder path', () => {
    render(MailView);

    const escape = findBackBinding('Escape');
    expect(escape).toBeDefined();
    escape!.action();

    expect(routerMock.navigate).toHaveBeenCalledWith('/mail?tab=forums');
    expect(routerMock.navigate).not.toHaveBeenCalledWith('/mail');
  });

  it('"u" navigates to the tab-qualified lastListPath, not a bare folder path', () => {
    render(MailView);

    const u = findBackBinding('u');
    expect(u).toBeDefined();
    u!.action();

    expect(routerMock.navigate).toHaveBeenCalledWith('/mail?tab=forums');
    expect(routerMock.navigate).not.toHaveBeenCalledWith('/mail');
  });
});
