/**
 * AccountsSection.svelte tests (issue #212, REQ-MAIL-SUB-01).
 *
 * Covers the empty state, the migrating-state progress readout, last
 * sync from the underlying IMAP-import account, and the remove flow
 * (two-step confirm: remove-as-separated-account, then keep-vs-purge).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

const removeSeparationMock = vi.fn(
  async (_accountId: string, _identityId: string, _keepMail: boolean) => undefined,
);
const refreshMock = vi.fn(async () => undefined);
const loadMock = vi.fn(async () => undefined);
let mockEntries: unknown[] = [];

vi.mock('../../lib/mail/sub-accounts.svelte', () => ({
  subAccounts: {
    get list() {
      return mockEntries;
    },
    load: () => loadMock(),
    refresh: () => refreshMock(),
    removeSeparation: (accountId: string, identityId: string, keepMail: boolean) =>
      removeSeparationMock(accountId, identityId, keepMail),
  },
}));

const loadIdentitiesMock = vi.fn(async () => undefined);
vi.mock('../../lib/mail/store.svelte', () => ({
  mail: { loadIdentities: () => loadIdentitiesMock() },
}));

const refreshSessionMock = vi.fn(async () => undefined);
vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: { refreshSession: () => refreshSessionMock() },
}));

const importHandleFactory = vi.fn(() => ({
  status: 'ready',
  account: null as null | { id: string; state: string; lastSuccessAt: string | null },
  load: vi.fn(async () => undefined),
  update: vi.fn(async () => undefined),
}));
vi.mock('../../lib/jmap/imap-import-store.svelte', () => ({
  imapImportStore: {
    forIdentity: () => importHandleFactory(),
  },
}));

const askMock = vi.fn(async (_spec: unknown) => true);
vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: (spec: unknown) => askMock(spec) },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'settings.accounts.hint': 'hint',
      'settings.accounts.empty': 'No separated accounts yet.',
      'settings.accounts.stateMigrating': 'Moving mail...',
      'settings.accounts.stateSeparated': 'Separated',
      'settings.accounts.progress': '{moved} of {total} messages moved',
      'settings.accounts.lastSync': 'Last sync',
      'settings.accounts.lastSyncNever': 'Never',
      'settings.accounts.pause': 'Pause',
      'settings.accounts.resume': 'Resume',
      'settings.accounts.remove': 'Remove',
      'common.cancel': 'Cancel',
    };
    let s = map[key] ?? key;
    if (params) for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, String(v));
    return s;
  },
  localeTag: () => 'en-US',
}));

import AccountsSection from './AccountsSection.svelte';

function makeEntry(overrides: Record<string, unknown> = {}) {
  return {
    accountId: 'acct-sub-1',
    name: 'club@example.com',
    identity: {
      id: '99',
      name: 'Club',
      email: 'club@example.com',
      mayDelete: true,
      separation: { state: 'separated', messagesTotal: 5, messagesMoved: 5, messagesCopied: 0 },
    },
    mailboxes: [],
    unreadThreads: 0,
    loadStatus: 'ready',
    errorMessage: null,
    ...overrides,
  };
}

beforeEach(() => {
  mockEntries = [];
  askMock.mockReset().mockResolvedValue(true);
  removeSeparationMock.mockReset().mockResolvedValue(undefined);
  refreshMock.mockClear();
  loadMock.mockClear();
  loadIdentitiesMock.mockClear();
  refreshSessionMock.mockClear();
  importHandleFactory.mockClear();
  importHandleFactory.mockReturnValue({
    status: 'ready',
    account: null,
    load: vi.fn(async () => undefined),
    update: vi.fn(async () => undefined),
  });
});

describe('AccountsSection -- empty state', () => {
  it('shows the empty-state message when there are no separated accounts', () => {
    render(AccountsSection);
    expect(screen.getByTestId('accounts-empty')).toBeInTheDocument();
    expect(loadMock).toHaveBeenCalled();
  });
});

describe('AccountsSection -- rows', () => {
  it('shows the migrating state and progress readout', () => {
    mockEntries = [
      makeEntry({
        identity: {
          id: '99',
          name: 'Club',
          email: 'club@example.com',
          mayDelete: true,
          separation: { state: 'migrating', messagesTotal: 10, messagesMoved: 3, messagesCopied: 1 },
        },
      }),
    ];
    render(AccountsSection);
    expect(screen.getByText('Moving mail...')).toBeInTheDocument();
    expect(screen.getByText('4 of 10 messages moved')).toBeInTheDocument();
  });

  it('shows the separated state with no progress readout', () => {
    mockEntries = [makeEntry()];
    render(AccountsSection);
    expect(screen.getByText('Separated')).toBeInTheDocument();
    expect(screen.queryByText(/messages moved/)).not.toBeInTheDocument();
  });

  it('hides Pause when there is no underlying IMAP-import account', () => {
    mockEntries = [makeEntry()];
    render(AccountsSection);
    expect(screen.queryByTestId('account-pause-acct-sub-1')).not.toBeInTheDocument();
  });

  it('shows Pause / last-sync when an IMAP-import account exists', () => {
    importHandleFactory.mockReturnValue({
      status: 'ready',
      account: { id: 'imp-1', state: 'enabled', lastSuccessAt: '2026-01-01T00:00:00Z' },
      load: vi.fn(async () => undefined),
      update: vi.fn(async () => undefined),
    });
    mockEntries = [makeEntry()];
    render(AccountsSection);
    expect(screen.getByTestId('account-pause-acct-sub-1')).toHaveTextContent('Pause');
  });
});

describe('AccountsSection -- remove flow', () => {
  beforeEach(() => {
    mockEntries = [makeEntry()];
  });

  it('keeps the mail when the purge confirm is declined', async () => {
    askMock.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    render(AccountsSection);
    await fireEvent.click(screen.getByTestId('account-remove-acct-sub-1'));

    await vi.waitFor(() => {
      expect(removeSeparationMock).toHaveBeenCalledWith('acct-sub-1', '99', true);
    });
    expect(loadIdentitiesMock).toHaveBeenCalled();
    // The session must be refreshed so a removed sub-account does not
    // linger in the derived list with a now-stale accountId.
    await vi.waitFor(() => {
      expect(refreshSessionMock).toHaveBeenCalled();
    });
  });

  it('purges the mail when the purge confirm is accepted', async () => {
    askMock.mockResolvedValueOnce(true).mockResolvedValueOnce(true);
    render(AccountsSection);
    await fireEvent.click(screen.getByTestId('account-remove-acct-sub-1'));

    await vi.waitFor(() => {
      expect(removeSeparationMock).toHaveBeenCalledWith('acct-sub-1', '99', false);
    });
  });

  it('does nothing when the first confirm is declined', async () => {
    askMock.mockResolvedValueOnce(false);
    render(AccountsSection);
    await fireEvent.click(screen.getByTestId('account-remove-acct-sub-1'));

    await vi.waitFor(() => {
      expect(askMock).toHaveBeenCalledTimes(1);
    });
    expect(removeSeparationMock).not.toHaveBeenCalled();
  });
});
