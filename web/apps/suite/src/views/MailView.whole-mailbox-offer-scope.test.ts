/**
 * Regression coverage for the whole-mailbox-selection banner's view
 * scoping (re #255, second follow-up).
 *
 * Reported: with the Inbox's folder query already fully loaded (no next
 * page), selecting the single rendered conversation still offered "Select
 * all N messages in the mailbox" -- a raw message total larger than the
 * one conversation on screen, with no further page to reveal it. The
 * offer compared a thread-collapsed row count against a raw-message
 * total under one shared unit, and a completed page was offered a
 * "select more" affordance with nothing more to select.
 *
 * Fixed contract:
 * - The offer appears only when `mail.listHasMore` is true -- a page that
 *   already lists every conversation of the view never offers a larger
 *   number.
 * - The offered/active total counts conversations
 *   (`listFolderConversationTotal`, backed by `Mailbox.totalThreads`), the
 *   unit the list itself renders, not raw messages
 *   (`listFolderTotal`/`totalEmails`).
 * - The "N ausgewählt" chip in whole-view mode reports that same
 *   conversation total.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { Email } from '../lib/mail/types';

const { email, mailMock } = vi.hoisted(() => {
  const email = {
    id: 'e1',
    threadId: 't1',
    mailboxIds: { 'mbx-inbox': true },
    keywords: { $seen: true },
    subject: 'Only conversation',
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
  } as unknown as Email;

  const mailMock = {
    listSelectedIds: new Set<string>(['e1']),
    listEmailIds: ['e1'],
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listEmails: [email],
    listFolder: 'inbox' as string,
    // Raw message total (3) exceeds the one loaded/rendered conversation
    // -- the exact divergence #255 reported (a thread folds more than one
    // inbox-resident message). The conversation total (1) matches what is
    // actually on screen: nothing further to select.
    listFolderTotal: 3,
    listFolderConversationTotal: 1,
    listHasMore: false,
    listWholeMailboxSelected: false,
    get listFolderLabel() {
      return 'Inbox';
    },
    mailboxes: new Map(),
    emails: new Map([['e1', email]]),
    threads: new Map([['t1', { id: 't1', emailIds: ['e1'] }]]),
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
    pruneSelectionToRendered: vi.fn(),
    toggleSelectAllVisible: vi.fn(),
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
    threadDedupeCount: vi.fn().mockReturnValue(1),
    threadEmails: vi.fn().mockReturnValue([]),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
    customMailboxes: [],
  };

  return { email, mailMock };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: { ask: vi.fn() } }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return ['mail'];
    },
    matches(...prefix: string[]): boolean {
      return prefix.every((seg: string, i: number) => ['mail'][i] === seg);
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
// No category tabs -- this file exercises the base folder view.
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
      'bulk.selected': `${String(args?.count ?? 0)} ausgewählt`,
      'select.allPageSelected': `Alle ${String(args?.count ?? 0)} auf dieser Seite`,
      'select.selectAllInFolder': `Alle ${String(args?.total ?? 0)} Konversationen im Postfach auswählen`,
      'select.wholeMailboxActive': `Alle ${String(args?.total ?? 0)} Konversationen im Postfach sind ausgewählt`,
      'select.clearWholeMailbox': 'Nur diese Seite auswählen',
      'list.loading': 'Loading...',
      'list.refresh': 'Refresh',
      'list.retry': 'Retry',
      'list.emptyTrash': 'Empty Trash',
      'list.couldNotLoad': 'Could not load',
      'mail.list.actionsAria': 'List actions',
      'mail.row.selectAria': 'Select message',
      'mail.row.starAria': 'Star',
      'mail.row.unstarAria': 'Unstar',
      'att.headerIcon.label': 'Has attachment',
      'bulk.archive': 'Archive',
      'bulk.markRead': 'Mark read',
      'bulk.markUnread': 'Mark unread',
      'bulk.move': 'Move...',
      'bulk.label': 'Label...',
      'bulk.delete': 'Delete',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));
vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

describe('MailView whole-mailbox offer is scoped to listHasMore and conversations (re #255)', () => {
  beforeEach(() => {
    mailMock.listFolder = 'inbox';
    mailMock.listSelectedIds = new Set<string>(['e1']);
    mailMock.listWholeMailboxSelected = false;
    mailMock.listFolderTotal = 3;
    mailMock.listFolderConversationTotal = 1;
    mailMock.listHasMore = false;
  });

  it('offers nothing when the page already shows every conversation (listHasMore false), even though the raw message total is larger', () => {
    const { container } = render(MailView);
    expect(container.querySelector('.whole-mailbox-banner')).toBeNull();
    expect(screen.queryByText(/auswählen$/)).toBeNull();
  });

  it('offers the conversation total, not the raw message total, once a further page exists (listHasMore true)', () => {
    mailMock.listHasMore = true;
    mailMock.listFolderConversationTotal = 5;
    render(MailView);
    expect(screen.getByText('Alle 5 Konversationen im Postfach auswählen')).toBeInTheDocument();
    expect(screen.queryByText(/Alle 3 /)).toBeNull();
  });

  it('the "N ausgewählt" chip reports the conversation total, not the raw message total, once whole-mailbox mode is engaged', () => {
    mailMock.listHasMore = true;
    mailMock.listFolderConversationTotal = 5;
    mailMock.listWholeMailboxSelected = true;
    render(MailView);
    expect(screen.getByText('5 ausgewählt')).toBeInTheDocument();
    expect(screen.queryByText('3 ausgewählt')).toBeNull();
  });
});
