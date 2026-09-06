/**
 * Issue #29 (round 3) + issue #88 FIX B: thread reader auto-navigate-away.
 *
 * When the user is viewing a thread from a folder (e.g. Trash) and a
 * Restore-from-Trash or Move-To-Mailbox operation removes all thread emails
 * from that folder, MailView should navigate back to the folder list
 * automatically (re #29).
 *
 * FIX B (#88): the effect must NOT bounce a thread on initial render when
 * folder membership appears false. Navigation is only triggered when
 * membership transitions from confirmed-true to false while the user is
 * already reading the thread. On initial render (confirmedFolderKey not yet
 * set), the guard does not fire even if stillInFolder is false — this
 * prevents the bounce that plagued self-sent threads before the mailboxIds
 * union (FIX A).
 *
 * re #294: leaving the thread route (threadId becomes undefined) resets
 * confirmedFolderKey. Without the reset, the browser Back button re-reaching
 * an archived thread's reader found the guard still armed from the original
 * viewing and bounced away before the reader ever rendered -- Back appeared
 * to do nothing. The reset re-arms the FIX B cold-load guard on every fresh
 * entry to a thread route, whether that's a first visit or a Back/Forward
 * return to one left earlier in the same session.
 *
 * Because the component tracks `confirmedFolderKey` in a plain (non-reactive)
 * let variable, the transition case cannot be verified by mutating the mock
 * between renders in a unit test. The transition behaviour (both the
 * original re #29 bounce and the re #294 reset) is verified by the
 * puppeteer integration test. The unit tests here cover:
 *   - Initial-render guards that must NOT navigate (FIX B assertions).
 *   - Unchanged guards that correctly skip virtual / search / loading states.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

// State shared between mock factories and tests.
const { mailMock, routerParts, navigate, TRASH_MAILBOX_ID, INBOX_MAILBOX_ID } = vi.hoisted(() => {
  const TRASH_MAILBOX_ID = 'mbx-trash';
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
    listLoadStatus: 'ready' as ('idle' | 'loading' | 'ready' | 'error'),
    listError: null,
    listFocusedIndex: -1,
    listEmails: [] as unknown[],
    listFolder: 'trash' as string,
    get listFolderLabel() {
      return 'Trash';
    },
    mailboxes: new Map(),
    get customMailboxes() {
      return [];
    },
    emails: new Map([['email-1', emailInInbox]]),
    threads: new Map([
      ['thread-1', { emailIds: ['email-1'] }],
    ]),
    searchHistory: [] as string[],
    searchEmails: [] as unknown[],
    searchEmailIds: [] as string[],
    searchLoadStatus: 'idle' as const,
    searchError: null,
    searchFocusedIndex: -1,
    inbox: { id: INBOX_MAILBOX_ID, role: 'inbox', name: 'Inbox' },
    trash: { id: TRASH_MAILBOX_ID, role: 'trash', name: 'Trash' },
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

  const navigate = vi.fn();
  // Route: /mail/thread/thread-1 — thread reader is open.
  const routerParts = ['mail', 'thread', 'thread-1'];

  return { mailMock, routerParts, navigate, TRASH_MAILBOX_ID, INBOX_MAILBOX_ID };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return routerParts;
    },
    matches(...prefix: string[]): boolean {
      return prefix.every((seg: string, i: number) => routerParts[i] === seg);
    },
    navigate,
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

describe('MailView: auto-navigate away when thread email leaves current folder (re #29, re #88)', () => {
  beforeEach(() => {
    navigate.mockClear();
    mailMock.threadEmails.mockClear();
    mailMock.searchEmailIds = [];
    // Restore the default: folder list has been loaded.
    mailMock.listLoadStatus = 'ready';
  });

  // FIX B (#88): on initial render the guard must NOT fire even when the
  // email appears not to be in the current folder. `confirmedFolderKey` has
  // not been set yet (no prior "in-folder" confirmation), so the effect
  // skips the navigate call. This prevents the bounce that afflicted
  // self-sent threads where the Inbox copy was deduplicated away.
  it('does NOT navigate on initial render even when email appears to have left trash (FIX B re #88)', () => {
    mailMock.listFolder = 'trash';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [INBOX_MAILBOX_ID]: true }, // no longer in trash (as seen on first render)
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    // No navigation on first render — confirmedFolderKey was never set.
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does NOT navigate when the thread email is still in trash', () => {
    mailMock.listFolder = 'trash';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [TRASH_MAILBOX_ID]: true }, // still in trash
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  it('does NOT navigate when thread is still loading (no emails yet)', () => {
    mailMock.listFolder = 'trash';
    mailMock.threadEmails.mockReturnValue([]); // thread not loaded yet

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  // FIX B (#88): same as the trash case — initial render with email NOT in inbox
  // must not bounce.
  it('does NOT navigate on initial render when viewing inbox and email appears to be elsewhere (FIX B re #88)', () => {
    mailMock.listFolder = 'inbox';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [TRASH_MAILBOX_ID]: true }, // not in inbox on first render
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    // confirmedFolderKey was never set for 'thread-1:inbox' → no navigate.
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does NOT navigate for the "all" virtual folder even if email mailboxIds change', () => {
    mailMock.listFolder = 'all';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { 'some-other-mailbox': true },
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  // FIX B (#88): same as trash/inbox — no navigate on initial render for custom mailbox.
  it('does NOT navigate on initial render for a custom folder when email is not in it (FIX B re #88)', () => {
    mailMock.listFolder = 'mbx-work';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { [INBOX_MAILBOX_ID]: true }, // not in mbx-work
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    // confirmedFolderKey never set for 'thread-1:mbx-work' → no navigate.
    expect(navigate).not.toHaveBeenCalled();
  });

  // Regression: clicking a search-result whose thread lives outside
  // the listFolder (e.g. a Junk hit while listFolder is still 'inbox')
  // used to bounce the user back to the inbox before the reader
  // rendered, dropping the search query in the process.
  it('does NOT navigate when the thread was opened from search', () => {
    mailMock.listFolder = 'inbox';
    mailMock.searchEmailIds = ['email-1'];
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        mailboxIds: { 'mbx-junk': true }, // junk only, NOT in inbox
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  // Regression (issue #52): cold-loading a thread URL (e.g. pasting
  // #/mail/thread/<id> into a new tab) used to bounce the user to the
  // inbox list because listFolder defaults to 'inbox' before any folder
  // has been fetched and the thread lives in a different mailbox.
  // The guard must not fire until listLoadStatus reaches 'ready'.
  it('does NOT navigate on cold load when listLoadStatus is idle', () => {
    mailMock.listLoadStatus = 'idle';
    mailMock.listFolder = 'inbox';
    mailMock.threadEmails.mockReturnValue([
      {
        id: 'email-1',
        threadId: 'thread-1',
        // Thread is in archive, not inbox — without the fix this triggers
        // an immediate bounce back to /mail.
        mailboxIds: { 'mbx-archive': true },
        keywords: { $seen: true },
      } as unknown as Email,
    ]);

    render(MailView);

    expect(navigate).not.toHaveBeenCalled();
  });

  // FIX B transition: when the thread IS in the folder on first evaluation
  // (confirmedFolderKey is set), and then mailboxIds change to exclude the
  // folder, the navigate-away fires. This transition cannot be tested in a
  // unit test because `confirmedFolderKey` is a plain let variable inside
  // the component and the mocked store is not reactive (changing the mock
  // return value does not re-trigger the Svelte effect). The transition is
  // verified by the puppeteer integration test for re #88 / re #29.
});
