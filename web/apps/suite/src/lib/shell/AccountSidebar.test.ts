/**
 * AccountSidebar.svelte tests (issue #212, REQ-MAIL-SUB-04/05).
 *
 * Covers: rendering only the scoped account's own mailbox tree, "Back to
 * All mail" returning to the combined /mail route, and the Compose
 * button defaulting the From identity to this account's own Identity
 * (REQ-MAIL-SUB-05) via compose.openWith's `identity` argument.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

let mockEntry: {
  accountId: string;
  name: string;
  identity: { id: string; email: string } | null;
  mailboxes: Array<{ id: string; name: string; role: string | null; unreadThreads: number; color?: string | null }>;
} | null = null;

const loadMock = vi.fn(async () => undefined);
vi.mock('../mail/sub-accounts.svelte', () => ({
  subAccounts: {
    find: (_accountId: string) => mockEntry,
    load: () => loadMock(),
  },
}));

const openWithMock = vi.fn();
vi.mock('../compose/compose.svelte', () => ({
  compose: { openWith: (args: unknown) => openWithMock(args) },
}));

const { routerNavigate } = vi.hoisted(() => ({ routerNavigate: vi.fn() }));
vi.mock('../router/router.svelte', () => ({
  router: {
    parts: ['account', 'acct-club'] as string[],
    navigate: routerNavigate,
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'shell.profile.allMail': 'All mail',
      'sidebar.compose': 'Compose',
      'sidebar.noCustom': 'No mailboxes',
    };
    return map[key] ?? key;
  },
}));

import AccountSidebar from './AccountSidebar.svelte';

beforeEach(() => {
  routerNavigate.mockClear();
  openWithMock.mockClear();
  loadMock.mockClear();
  mockEntry = {
    accountId: 'acct-club',
    name: 'club@example.com',
    identity: { id: '99', email: 'club@example.com' },
    mailboxes: [
      { id: 'mb-inbox', name: 'Inbox', role: 'inbox', unreadThreads: 4 },
      { id: 'mb-sent', name: 'Sent', role: 'sent', unreadThreads: 0 },
      { id: 'mb-label', name: 'Board', role: null, unreadThreads: 2 },
    ],
  };
});

describe('AccountSidebar', () => {
  it('renders only the scoped account name and its own mailboxes', () => {
    render(AccountSidebar, { props: { accountId: 'acct-club' } });
    expect(screen.getByTestId('account-sidebar-heading')).toHaveTextContent('club@example.com');
    expect(screen.getByText('Inbox')).toBeInTheDocument();
    expect(screen.getByText('Sent')).toBeInTheDocument();
    expect(screen.getByText('Board')).toBeInTheDocument();
    expect(screen.getByText('4')).toBeInTheDocument();
  });

  it('navigates to /mail when "Back to All mail" is clicked', async () => {
    render(AccountSidebar, { props: { accountId: 'acct-club' } });
    await fireEvent.click(screen.getByTestId('account-sidebar-back'));
    expect(routerNavigate).toHaveBeenCalledWith('/mail');
  });

  it('navigates to the account-scoped Inbox route for the Inbox row', async () => {
    render(AccountSidebar, { props: { accountId: 'acct-club' } });
    await fireEvent.click(screen.getByText('Inbox'));
    expect(routerNavigate).toHaveBeenCalledWith('/account/acct-club');
  });

  it('navigates to the account-scoped folder route for a non-inbox row', async () => {
    render(AccountSidebar, { props: { accountId: 'acct-club' } });
    await fireEvent.click(screen.getByText('Board'));
    expect(routerNavigate).toHaveBeenCalledWith('/account/acct-club/folder/mb-label');
  });

  it('Compose defaults the From identity to this account\'s own Identity', async () => {
    render(AccountSidebar, { props: { accountId: 'acct-club' } });
    await fireEvent.click(screen.getByText('Compose'));
    expect(openWithMock).toHaveBeenCalledWith(
      expect.objectContaining({ identity: { id: '99', email: 'club@example.com' } }),
    );
  });
});
