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
  mockEmails = [];
  mockEntry = {
    accountId: 'acct-club',
    name: 'club@example.com',
    identity: { id: '99', email: 'club@example.com' },
    mailboxes: [{ id: 'mb-inbox', role: 'inbox' }],
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
});
