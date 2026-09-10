/**
 * SubAccountMailView.svelte tests (issue #212, REQ-MAIL-SUB-02/04/05).
 *
 * Covers: the message list resolves to the account's Inbox by default,
 * opening a message calls subAccounts.openEmail (which marks it seen),
 * and the header Compose button defaults the From identity to this
 * account's own Identity.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

let mockEntry: {
  accountId: string;
  name: string;
  identity: { id: string; email: string } | null;
  mailboxes: Array<{ id: string; role: string | null }>;
} | null = null;

const loadMock = vi.fn(async () => undefined);
const loadEmailsMock = vi.fn(async (_accountId: string, _mailboxId: string) => undefined);
const openEmailMock = vi.fn(async (_accountId: string, _emailId: string) => undefined);
const closeReadingMock = vi.fn();
let mockEmails: Array<{ id: string; subject: string | null; from: Array<{ name: string | null; email: string }>; preview: string; receivedAt: string; keywords: Record<string, true> }> = [];

vi.mock('../lib/mail/sub-accounts.svelte', () => ({
  subAccounts: {
    find: (_accountId: string) => mockEntry,
    load: () => loadMock(),
    loadEmails: (accountId: string, mailboxId: string) => loadEmailsMock(accountId, mailboxId),
    openEmail: (accountId: string, emailId: string) => openEmailMock(accountId, emailId),
    closeReading: () => closeReadingMock(),
    get emails() {
      return mockEmails;
    },
    listStatus: 'ready',
    listErrorMessage: null,
    reading: null,
    readingStatus: 'idle',
  },
}));

const openWithMock = vi.fn();
vi.mock('../lib/compose/compose.svelte', () => ({
  compose: { openWith: (args: unknown) => openWithMock(args) },
}));

vi.mock('../lib/jmap/client', () => ({
  jmap: { downloadUrl: vi.fn(() => null) },
}));

const mirrorScopedEmailsMock = vi.fn();
const bulkSetSeenMock = vi.fn(async (_ids: string[], _seen: boolean) => undefined);
const bulkMoveToMailboxMock = vi.fn(async (_ids: string[], _targetId: string) => undefined);
vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    mirrorScopedEmails: (accountId: string, emails: unknown[]) =>
      mirrorScopedEmailsMock(accountId, emails),
    bulkSetSeen: (ids: string[], seen: boolean) => bulkSetSeenMock(ids, seen),
    bulkMoveToMailbox: (ids: string[], targetId: string) => bulkMoveToMailboxMock(ids, targetId),
  },
}));

const movePickerOpenMock = vi.fn();
vi.mock('../lib/mail/move-picker.svelte', () => ({
  movePicker: {
    open: (emailId: string, accountId: string | null) => movePickerOpenMock(emailId, accountId),
    isOpen: false,
    accountId: null,
  },
}));

vi.mock('../lib/toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));

vi.mock('../lib/mail/HtmlBody.svelte', () => ({ default: () => null }));
vi.mock('../lib/mail/sanitize', () => ({ sanitizeHtml: (s: string) => s }));

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'sidebar.compose': 'Compose',
      'common.loading': 'Loading...',
      'archive.empty': 'No messages',
      'archive.selectMessage': 'Select a message',
      'msg.noSender': 'No sender',
      'msg.noSubject': '(no subject)',
      'msg.kebab.openLabel': 'More actions',
      'msg.kebab.markUnread': 'Mark as unread',
      'msg.kebab.markRead': 'Mark as read',
      'msg.kebab.move': 'Move to...',
      'msg.kebab.delete': 'Delete this message',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en-US',
}));

import SubAccountMailView from './SubAccountMailView.svelte';

beforeEach(() => {
  loadMock.mockClear();
  loadEmailsMock.mockClear();
  openEmailMock.mockClear();
  closeReadingMock.mockClear();
  openWithMock.mockClear();
  mirrorScopedEmailsMock.mockClear();
  bulkSetSeenMock.mockClear();
  bulkMoveToMailboxMock.mockClear();
  movePickerOpenMock.mockClear();
  mockEmails = [];
  mockEntry = {
    accountId: 'acct-club',
    name: 'club@example.com',
    identity: { id: '99', email: 'club@example.com' },
    mailboxes: [
      { id: 'mb-inbox', role: 'inbox' },
      { id: 'mb-trash', role: 'trash' },
    ],
  };
});

describe('SubAccountMailView', () => {
  it('loads the account Inbox by default when no mailboxId prop is given', () => {
    render(SubAccountMailView, { props: { accountId: 'acct-club' } });
    expect(loadEmailsMock).toHaveBeenCalledWith('acct-club', 'mb-inbox');
  });

  it('loads a specific mailbox when mailboxId is given', () => {
    render(SubAccountMailView, { props: { accountId: 'acct-club', mailboxId: 'mb-other' } });
    expect(loadEmailsMock).toHaveBeenCalledWith('acct-club', 'mb-other');
  });

  it('opens a message via subAccounts.openEmail when a row is clicked', async () => {
    mockEmails = [
      {
        id: 'e1',
        subject: 'Hello',
        from: [{ name: 'Alice', email: 'alice@example.com' }],
        preview: 'preview text',
        receivedAt: '2026-01-01T00:00:00Z',
        keywords: {},
      },
    ];
    render(SubAccountMailView, { props: { accountId: 'acct-club' } });
    await fireEvent.click(screen.getByText('Hello'));
    expect(openEmailMock).toHaveBeenCalledWith('acct-club', 'e1');
  });

  it('Compose defaults the From identity to this account\'s own Identity', async () => {
    render(SubAccountMailView, { props: { accountId: 'acct-club' } });
    await fireEvent.click(screen.getByText('Compose'));
    expect(openWithMock).toHaveBeenCalledWith(
      expect.objectContaining({ identity: { id: '99', email: 'club@example.com' } }),
    );
  });

  it('mirrors fetched rows into the shared mail store cache', () => {
    mockEmails = [
      {
        id: 'e1',
        subject: 'Hello',
        from: [],
        preview: 'p',
        receivedAt: '2026-01-01T00:00:00Z',
        keywords: {},
      },
    ];
    render(SubAccountMailView, { props: { accountId: 'acct-club' } });
    expect(mirrorScopedEmailsMock).toHaveBeenCalledWith('acct-club', mockEmails);
  });

  describe('per-row actions (issue #212, REQ-MAIL-SUB-04)', () => {
    beforeEach(() => {
      mockEmails = [
        {
          id: 'e1',
          subject: 'Hello',
          from: [],
          preview: 'p',
          receivedAt: '2026-01-01T00:00:00Z',
          keywords: {},
        },
      ];
    });

    it('Move opens the shared movePicker scoped to this sub-account', async () => {
      render(SubAccountMailView, { props: { accountId: 'acct-club' } });
      await fireEvent.click(screen.getByLabelText('More actions'));
      await fireEvent.click(screen.getByText('Move to...'));
      expect(movePickerOpenMock).toHaveBeenCalledWith('e1', 'acct-club');
    });

    it('Delete moves the message to this sub-account\'s own Trash mailbox', async () => {
      render(SubAccountMailView, { props: { accountId: 'acct-club' } });
      await fireEvent.click(screen.getByLabelText('More actions'));
      await fireEvent.click(screen.getByText('Delete this message'));
      expect(bulkMoveToMailboxMock).toHaveBeenCalledWith(['e1'], 'mb-trash');
    });

    it('"Mark as read" calls mail.bulkSetSeen(true) for an unread message', async () => {
      render(SubAccountMailView, { props: { accountId: 'acct-club' } });
      await fireEvent.click(screen.getByLabelText('More actions'));
      await fireEvent.click(screen.getByText('Mark as read'));
      expect(bulkSetSeenMock).toHaveBeenCalledWith(['e1'], true);
    });
  });
});
