/**
 * Tests for the ProfileMenu scope switcher (issue #212, REQ-MAIL-SUB-02/09).
 *
 * Capability gating: with the sub-accounts capability absent, the menu
 * renders exactly the pre-#212 Settings/Sign-out list -- no scope section
 * at all. With it present, "All mail" plus one row per separated identity
 * render above Settings, each row's unread badge reflects the sub-account
 * store, and clicking a row navigates into that scope.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

const hasSubAccountsMock = vi.fn(() => false);
vi.mock('../auth/capabilities', () => ({
  hasSubAccounts: () => hasSubAccountsMock(),
}));

const { routerNavigate } = vi.hoisted(() => ({ routerNavigate: vi.fn() }));
vi.mock('../router/router.svelte', () => ({
  router: {
    parts: [] as string[],
    navigate: routerNavigate,
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: { username: 'alice@example.com' },
    logout: vi.fn(),
  },
}));

const loadMock = vi.fn(async () => {});
let mockList: Array<{ accountId: string; name: string; unreadThreads: number }> = [];
vi.mock('../mail/sub-accounts.svelte', () => ({
  subAccounts: {
    get list() {
      return mockList;
    },
    load: () => loadMock(),
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'shell.profile.menu': 'Account menu',
      'shell.profile.scopesLabel': 'Accounts',
      'shell.profile.allMail': 'All mail',
      'shell.profile.scopeUnreadAria': '{count} unread',
      'settings.title': 'Settings',
      'settings.account.signOut': 'Sign out',
    };
    let s = map[key] ?? key;
    if (params) {
      for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, String(v));
    }
    return s;
  },
}));

import ProfileMenu from './ProfileMenu.svelte';

beforeEach(() => {
  hasSubAccountsMock.mockReturnValue(false);
  routerNavigate.mockClear();
  loadMock.mockClear();
  mockList = [];
});

describe('ProfileMenu -- capability absent', () => {
  it('renders only Settings and Sign out, no scope section', async () => {
    render(ProfileMenu);
    await fireEvent.click(screen.getByRole('button', { name: 'Account menu' }));

    expect(screen.getByText('Settings')).toBeInTheDocument();
    expect(screen.getByText('Sign out')).toBeInTheDocument();
    expect(screen.queryByText('Accounts')).not.toBeInTheDocument();
    expect(screen.queryByText('All mail')).not.toBeInTheDocument();
    expect(loadMock).not.toHaveBeenCalled();
  });
});

describe('ProfileMenu -- capability present', () => {
  beforeEach(() => {
    hasSubAccountsMock.mockReturnValue(true);
    mockList = [
      { accountId: 'acct-club', name: 'club@example.com', unreadThreads: 3 },
      { accountId: 'acct-empty', name: 'empty@example.com', unreadThreads: 0 },
    ];
  });

  it('renders All mail plus one row per separated identity with unread badges', async () => {
    render(ProfileMenu);
    await fireEvent.click(screen.getByRole('button', { name: 'Account menu' }));

    expect(loadMock).toHaveBeenCalled();
    expect(screen.getByText('All mail')).toBeInTheDocument();
    expect(screen.getByText('club@example.com')).toBeInTheDocument();
    expect(screen.getByText('empty@example.com')).toBeInTheDocument();
    expect(screen.getByText('3')).toBeInTheDocument();
  });

  it('navigates to /account/<id> when a scope row is clicked', async () => {
    render(ProfileMenu);
    await fireEvent.click(screen.getByRole('button', { name: 'Account menu' }));
    await fireEvent.click(screen.getByTestId('profile-scope-acct-club'));

    expect(routerNavigate).toHaveBeenCalledWith('/account/acct-club');
  });

  it('navigates to /mail when "All mail" is clicked', async () => {
    render(ProfileMenu);
    await fireEvent.click(screen.getByRole('button', { name: 'Account menu' }));
    await fireEvent.click(screen.getByTestId('profile-scope-all'));

    expect(routerNavigate).toHaveBeenCalledWith('/mail');
  });
});
