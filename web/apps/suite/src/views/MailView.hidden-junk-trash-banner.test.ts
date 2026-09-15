/**
 * Issue #384: a label applied to a message while it sits in Junk is
 * excluded from the label's folder view (REQ-SRC-06, issue #310), and the
 * view rendered the empty state with no explanation when every member was
 * hidden this way.
 *
 * MailView must show a "N labelled messages are in Spam or Trash" banner
 * driven by `mail.listHiddenJunkTrashCount`, with a link into the same
 * folder without the exclusion (`?unfiltered=1`); that linked view must
 * mark itself in its header so it isn't mistaken for the ordinary folder
 * view.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

const { LABEL_ID, mailMock, routerState, navigateMock, setParamMock } = vi.hoisted(() => {
  const LABEL_ID = 'mbx-label';
  const navigateMock = vi.fn();
  const setParamMock = vi.fn();
  const mailMock = {
    listSelectedIds: new Set<string>(),
    listEmailIds: [] as string[],
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listEmails: [] as unknown[],
    listFolder: 'mbx-label' as string,
    listWholeMailboxSelected: false,
    listFolderTotal: null as number | null,
    listUnfiltered: false,
    listHiddenJunkTrashCount: null as number | null,
    get listFolderLabel() {
      return 'not-spam';
    },
    mailboxes: new Map([['mbx-label', { id: 'mbx-label', name: 'not-spam', role: null }]]),
    emails: new Map(),
    threads: new Map(),
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
  const routerState = { folder: LABEL_ID, unfiltered: null as string | null };
  return { LABEL_ID, mailMock, routerState, navigateMock, setParamMock };
});

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: { ask: vi.fn() } }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return ['mail', 'folder', routerState.folder];
    },
    matches(...prefix: string[]): boolean {
      const p = ['mail', 'folder', routerState.folder];
      return prefix.every((seg: string, i: number) => p[i] === seg);
    },
    navigate: navigateMock,
    getParam: (key: string) => (key === 'unfiltered' ? routerState.unfiltered : null),
    setParam: setParamMock,
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
      'list.loading': 'Loading...',
      'list.refresh': 'Refresh',
      'list.retry': 'Retry',
      'list.emptyTrash': 'Empty Trash',
      'list.couldNotLoad': 'Could not load',
      'list.empty.folder': '{name} is empty.',
      'mail.list.actionsAria': 'List actions',
      'mail.list.threadsAria': 'Messages in {name}',
      'mail.hiddenJunkTrash.bannerOne': '{n} labelled message is in Spam or Trash.',
      'mail.hiddenJunkTrash.bannerMany': '{n} labelled messages are in Spam or Trash.',
      'mail.hiddenJunkTrash.show': 'Show them',
      'mail.hiddenJunkTrash.viewHeader': 'Showing every message in "{name}", including Spam and Trash.',
      'mail.hiddenJunkTrash.backToFiltered': 'Back to {name}',
    };
    const template = map[key] ?? key;
    if (!args) return template;
    return template.replace(/\{(\w+)\}/g, (_, name: string) => String(args[name] ?? `{${name}}`));
  },
  localeTag: () => 'en',
}));

vi.mock('../lib/mail/search-query', () => ({
  decodeChips: vi.fn().mockReturnValue([]),
}));

import MailView from './MailView.svelte';

describe('MailView hidden-members banner (re #384)', () => {
  beforeEach(() => {
    mailMock.listFolder = LABEL_ID;
    mailMock.listUnfiltered = false;
    mailMock.listHiddenJunkTrashCount = null;
    mailMock.listEmailIds = [];
    mailMock.listEmails = [];
    routerState.folder = LABEL_ID;
    routerState.unfiltered = null;
    navigateMock.mockClear();
    setParamMock.mockClear();
  });

  it('a label whose only members sit in Junk renders the banner with the right count', () => {
    mailMock.listHiddenJunkTrashCount = 23;
    render(MailView);
    expect(screen.getByText('23 labelled messages are in Spam or Trash.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Show them' })).toBeInTheDocument();
  });

  it('clicking the banner link switches the view to the unfiltered query', async () => {
    mailMock.listHiddenJunkTrashCount = 23;
    render(MailView);
    await fireEvent.click(screen.getByRole('button', { name: 'Show them' }));
    expect(setParamMock).toHaveBeenCalledWith('unfiltered', '1');
  });

  it('a label with visible members and no hidden ones renders no banner', () => {
    mailMock.listHiddenJunkTrashCount = 0;
    mailMock.listEmailIds = ['e1'];
    mailMock.listEmails = [
      {
        id: 'e1',
        threadId: 't1',
        mailboxIds: { [LABEL_ID]: true },
        keywords: {},
        from: [{ name: 'Bob', email: 'bob@example.local' }],
        subject: 'Visible message',
        preview: '',
        receivedAt: '2026-01-01T00:00:00Z',
        hasAttachment: false,
      },
    ];
    render(MailView);
    expect(screen.queryByText(/is in Spam or Trash/)).not.toBeInTheDocument();
  });

  it('the unfiltered linked view marks itself in the view header', () => {
    mailMock.listUnfiltered = true;
    mailMock.listHiddenJunkTrashCount = null;
    routerState.unfiltered = '1';
    render(MailView);
    expect(
      screen.getByText('Showing every message in "not-spam", including Spam and Trash.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Back to not-spam' })).toBeInTheDocument();
  });

  it('the unfiltered view does not also render the hidden-members banner', () => {
    mailMock.listUnfiltered = true;
    mailMock.listHiddenJunkTrashCount = null;
    routerState.unfiltered = '1';
    render(MailView);
    expect(screen.queryByText(/is in Spam or Trash/)).not.toBeInTheDocument();
  });
});
