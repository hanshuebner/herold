/**
 * Regression test for issue #59 / #54: right-aligned date column.
 *
 * Commit d6d5d96 added `text-align: right` to the `.date` span in
 * MailView.svelte so that dates are right-aligned within their trailing
 * auto-width column.  This file pins the behaviour so a future layout
 * refactor cannot silently drop the rule.
 *
 * happy-dom does not cascade scoped Svelte component styles into
 * `getComputedStyle`, so the assertion is done in two steps:
 *
 *   1. Structural: render the component with a minimal thread list and
 *      confirm that at least one `.date` span is present in the DOM.
 *      This ensures the rule is not vacuously satisfied because the
 *      element never rendered.
 *
 *   2. Source: read MailView.svelte as a raw string via Vite's `?raw`
 *      suffix and assert that the `<style>` block contains the exact
 *      `text-align: right` declaration inside the `.date { ... }` rule.
 *      This is approach (c) from the task brief and is the established
 *      fallback for style assertions in this test suite when computed
 *      styles are not available from happy-dom.
 */

import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';
// Vite's ?raw suffix imports the file as a plain string.  vitest uses the
// same transform pipeline so this works identically in tests and in builds.
import mailViewSource from './MailView.svelte?raw';

// ── Fixtures (via vi.hoisted so they are available in vi.mock factories) ──────

const { mailMock } = vi.hoisted(() => {
  const EMAIL = {
    id: 'e-date-1',
    threadId: 'tid-date-1',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.com' }],
    to: [{ name: 'Bob', email: 'bob@example.com' }],
    subject: 'Date alignment test',
    preview: 'This message exercises the date column.',
    receivedAt: '2024-03-10T08:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
  };

  const INBOX_MBX = {
    id: 'mbx-inbox',
    name: 'Inbox',
    role: 'inbox',
    parentId: null,
    sortOrder: 0,
    totalEmails: 1,
    unreadEmails: 0,
    totalThreads: 1,
    unreadThreads: 0,
  } as import('../lib/mail/types').Mailbox;

  const mailMock = {
    listLoadStatus: 'ready' as const,
    listError: null,
    listFocusedIndex: -1,
    listFolder: 'inbox' as string,
    get listFolderLabel() {
      return 'Inbox';
    },
    listSelectedIds: new Set<string>(),
    listEmailIds: [EMAIL.id],
    get listEmails() {
      return [EMAIL];
    },
    mailboxes: new Map([['mbx-inbox', INBOX_MBX]]),
    get customMailboxes() {
      return [];
    },
    emails: new Map([[EMAIL.id, EMAIL]]),
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
    get searchQuery() {
      return '';
    },
    threadEmails: vi.fn().mockReturnValue([]),
    threadStatus: vi.fn().mockReturnValue('idle'),
    threadError: vi.fn().mockReturnValue(null),
    loadThread: vi.fn().mockResolvedValue(undefined),
  };

  return { mailMock };
});

// ── Module mocks ──────────────────────────────────────────────────────────────

vi.mock('../lib/mail/store.svelte', () => ({ mail: mailMock }));
vi.mock('../lib/dialog/confirm.svelte', () => ({ confirm: { ask: vi.fn() } }));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return ['mail', 'folder', 'inbox'];
    },
    matches(...prefix: string[]): boolean {
      const p = ['mail', 'folder', 'inbox'];
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

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Extract the declarations inside the first occurrence of `selector { ... }`
 * from a CSS string.  The search is not a full CSS parser; it is intentionally
 * minimal and relies on the well-formatted style blocks the Svelte compiler
 * emits.
 */
function extractRuleBody(css: string, selector: string): string {
  // Escape special CSS selector characters for use in a RegExp.
  const escaped = selector.replace(/[.[\]]/g, (c) => `\\${c}`);
  const re = new RegExp(`${escaped}\\s*\\{([^}]*)\\}`);
  const m = css.match(re);
  return m?.[1] ?? '';
}

/**
 * Return the raw text of the `<style>` block from the MailView.svelte
 * source string (imported via Vite's `?raw` suffix).
 */
function mailViewStyles(): string {
  const match = mailViewSource.match(/<style>([\s\S]*?)<\/style>/);
  if (!match?.[1]) throw new Error('Could not find <style> block in MailView.svelte');
  return match[1];
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('MailView date column right-alignment (re #59 / #54)', () => {
  it('renders a .date span inside a thread row', () => {
    const { container } = render(MailView);
    const dateEl = container.querySelector('.thread-row .date');
    expect(dateEl).not.toBeNull();
  });

  it('applies text-align: right to .date in the component stylesheet', () => {
    const css = mailViewStyles();
    const body = extractRuleBody(css, '.date');
    // The rule body must contain `text-align: right`.  Whitespace around
    // the colon and semicolon is normalised so minor formatting changes
    // do not break the assertion.
    expect(body.replace(/\s+/g, ' ')).toContain('text-align: right');
  });
});
