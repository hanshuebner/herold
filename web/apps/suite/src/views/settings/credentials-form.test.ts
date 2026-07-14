/**
 * CredentialsForm.svelte component tests (issue #224).
 *
 * Covers:
 *   - Renders one row per credential, grouped by kind (session / device
 *     token / oauth2 grant), with the attributes needed to identify it.
 *   - The current session is marked with the "This session" chip and a
 *     "Sign out" (not "Revoke") action.
 *   - Revoking a non-current row confirms, then calls
 *     credentials.revoke(kind, id) and shows a success toast.
 *   - A cancelled confirm does not call revoke().
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import type { CredentialDTO } from '../../lib/settings/credentials.svelte';

// ── Mocks ─────────────────────────────────────────────────────────────────

const { mockLoad, mockRevoke, credentialsState } = vi.hoisted(() => {
  const mockLoad = vi.fn(async () => undefined);
  const mockRevoke = vi.fn(async () => undefined);
  const credentialsState = { items: [] as CredentialDTO[], status: 'ready', errorMessage: null as string | null };
  return { mockLoad, mockRevoke, credentialsState };
});

vi.mock('../../lib/settings/credentials.svelte', () => ({
  credentials: {
    get items() {
      return credentialsState.items;
    },
    get status() {
      return credentialsState.status;
    },
    get loading() {
      return credentialsState.status === 'loading' || credentialsState.status === 'idle';
    },
    get errorMessage() {
      return credentialsState.errorMessage;
    },
    load: mockLoad,
    revoke: mockRevoke,
  },
}));

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: {
    principalId: 'p-1',
    signalUnauthenticated: vi.fn(),
  },
}));

const { mockToastShow } = vi.hoisted(() => ({ mockToastShow: vi.fn() }));
vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: mockToastShow, dismiss: vi.fn(), current: null },
}));

const { mockConfirmAsk } = vi.hoisted(() => ({ mockConfirmAsk: vi.fn(async () => true) }));
vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: mockConfirmAsk, pending: null, decide: vi.fn() },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string =>
    params ? `${key}:${JSON.stringify(params)}` : key,
  localeTag: (): string => 'en-US',
}));

import CredentialsForm from './CredentialsForm.svelte';

// ── Fixtures ─────────────────────────────────────────────────────────────

function makeItem(overrides: Partial<CredentialDTO> = {}): CredentialDTO {
  return {
    kind: 'session',
    id: 's-1',
    created_at: '2026-07-01T10:00:00Z',
    is_current: false,
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  credentialsState.items = [];
  credentialsState.status = 'ready';
  credentialsState.errorMessage = null;
});

beforeEach(() => {
  mockConfirmAsk.mockResolvedValue(true);
});

describe('CredentialsForm: rendering', () => {
  it('renders the empty state when there are no credentials', () => {
    credentialsState.items = [];
    const { getByText } = render(CredentialsForm);
    expect(getByText('settings.credentials.empty')).toBeInTheDocument();
  });

  it('renders one row per credential kind, grouped', () => {
    credentialsState.items = [
      makeItem({ kind: 'session', id: 's-1', is_current: true, user_agent: 'Mozilla/5.0 (Macintosh)' }),
      makeItem({ kind: 'session', id: 's-2', is_current: false, user_agent: 'Mozilla/5.0 (iPhone)' }),
      makeItem({ kind: 'device_token', id: '7', label: 'My phone', created_at: '2026-06-01T00:00:00Z' }),
      makeItem({
        kind: 'oauth2_grant',
        id: 'fam-1',
        label: 'Herold Android',
        client_id: 'android-client',
        created_at: '2026-05-01T00:00:00Z',
        expires_at: '2026-08-01T00:00:00Z',
      }),
    ];
    const { getByTestId, getByText } = render(CredentialsForm);

    const sessionsGroup = getByTestId('credentials-sessions');
    expect(sessionsGroup.querySelectorAll('li')).toHaveLength(2);

    const deviceTokensGroup = getByTestId('credentials-device-tokens');
    expect(deviceTokensGroup.querySelectorAll('li')).toHaveLength(1);
    expect(getByText('My phone')).toBeInTheDocument();

    const oauthGroup = getByTestId('credentials-oauth-grants');
    expect(oauthGroup.querySelectorAll('li')).toHaveLength(1);
    expect(getByText('Herold Android')).toBeInTheDocument();
  });

  it('marks the current session with the "this session" chip and Sign out action', () => {
    credentialsState.items = [
      makeItem({ kind: 'session', id: 's-1', is_current: true, user_agent: 'Mozilla/5.0 (Macintosh)' }),
    ];
    const { getByText } = render(CredentialsForm);
    expect(getByText('settings.credentials.thisSession')).toBeInTheDocument();
    expect(getByText('settings.credentials.signOut')).toBeInTheDocument();
  });

  it('shows a Revoke action (not Sign out) for a non-current session', () => {
    credentialsState.items = [
      makeItem({ kind: 'session', id: 's-1', is_current: false, user_agent: 'Mozilla/5.0 (Windows)' }),
    ];
    const { getByText, queryByText } = render(CredentialsForm);
    expect(getByText('settings.credentials.revoke')).toBeInTheDocument();
    expect(queryByText('settings.credentials.signOut')).not.toBeInTheDocument();
  });

  it('renders the loading state', () => {
    credentialsState.status = 'loading';
    const { getByText } = render(CredentialsForm);
    expect(getByText('common.loading')).toBeInTheDocument();
  });

  it('renders the error state', () => {
    credentialsState.status = 'error';
    credentialsState.errorMessage = 'boom';
    const { getByText } = render(CredentialsForm);
    expect(getByText('boom')).toBeInTheDocument();
  });
});

describe('CredentialsForm: revoke action', () => {
  it('confirms, then calls credentials.revoke(kind, id) for a non-current device token', async () => {
    credentialsState.items = [makeItem({ kind: 'device_token', id: '7', label: 'My phone' })];
    const { getByText } = render(CredentialsForm);

    await fireEvent.click(getByText('settings.credentials.revoke'));

    await waitFor(() => {
      expect(mockConfirmAsk).toHaveBeenCalledOnce();
      expect(mockRevoke).toHaveBeenCalledWith('device_token', '7');
      expect(mockToastShow).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'settings.credentials.revoked' }),
      );
    });
  });

  it('does not call revoke() when the confirm dialog is cancelled', async () => {
    mockConfirmAsk.mockResolvedValueOnce(false);
    credentialsState.items = [makeItem({ kind: 'oauth2_grant', id: 'fam-1', label: 'Some app' })];
    const { getByText } = render(CredentialsForm);

    await fireEvent.click(getByText('settings.credentials.revoke'));

    await waitFor(() => expect(mockConfirmAsk).toHaveBeenCalledOnce());
    expect(mockRevoke).not.toHaveBeenCalled();
  });

  it('revoking the current session calls revoke() but shows no success toast (forced-login takes over)', async () => {
    credentialsState.items = [
      makeItem({ kind: 'session', id: 's-1', is_current: true, user_agent: 'Mozilla/5.0 (Macintosh)' }),
    ];
    const { getByText } = render(CredentialsForm);

    await fireEvent.click(getByText('settings.credentials.signOut'));

    await waitFor(() => {
      expect(mockRevoke).toHaveBeenCalledWith('session', 's-1');
    });
    expect(mockToastShow).not.toHaveBeenCalled();
  });
});
