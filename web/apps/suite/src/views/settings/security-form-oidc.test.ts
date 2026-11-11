/**
 * SecurityForm.svelte — "Sign-in providers" section (external OIDC
 * account linking, REQ-AUTH-50..58).
 *
 *   1. Hidden when no provider is configured and nothing is linked.
 *   2. Unlinked provider: "Link" starts the flow and navigates to the
 *      provider URL returned by .../oidc-links/begin.
 *   3. Linked provider: shows the provider account and unlinks after
 *      confirmation.
 *   4. The callback's oidc_link outcome is shown once and stripped from
 *      the route.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor, screen } from '@testing-library/svelte';
import SecurityForm from './SecurityForm.svelte';

const { mockGet, mockPost, mockDel, mockRouter } = vi.hoisted(() => ({
  mockGet: vi.fn(),
  mockPost: vi.fn(),
  mockDel: vi.fn(),
  mockRouter: { getParam: vi.fn(), replace: vi.fn() },
}));

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: { status: 'ready', principalId: '42' },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../../lib/api/client', () => ({
  get: (...args: unknown[]) => mockGet(...args),
  post: (...args: unknown[]) => mockPost(...args),
  put: vi.fn(),
  del: (...args: unknown[]) => mockDel(...args),
  ApiError: class ApiError extends Error {},
  UnauthenticatedError: class UnauthenticatedError extends Error {},
}));

vi.mock('../../lib/router/router.svelte', () => ({ router: mockRouter }));

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string>): string =>
    params ? `${key}:${Object.values(params).join(',')}` : key,
}));

const PRINCIPAL = {
  id: '42',
  canonical_email: 'alice@example.local',
  display_name: 'Alice',
  totp_enabled: false,
  flags: [],
};

function routeGets(providers: unknown[], links: unknown[]): void {
  mockGet.mockImplementation(async (path: string) => {
    if (path === '/api/v1/principals/42') return PRINCIPAL;
    if (path === '/api/v1/auth/oidc/providers') return { items: providers };
    if (path === '/api/v1/principals/42/oidc-links') return { items: links };
    throw new Error(`unexpected GET ${path}`);
  });
}

describe('SecurityForm — sign-in providers', () => {
  let assign: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    mockRouter.getParam.mockReturnValue(null);
    assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, assign });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('is hidden when no provider is configured and nothing is linked', async () => {
    routeGets([], []);
    const { container } = render(SecurityForm);
    await waitFor(() => expect(container.querySelector('.totp-status')).not.toBeNull());
    await waitFor(() => expect(mockGet).toHaveBeenCalledWith('/api/v1/principals/42/oidc-links'));
    expect(container.querySelector('[data-testid="oidc-provider-list"]')).toBeNull();
  });

  it('starts linking an unlinked provider', async () => {
    routeGets([{ id: 'PocketID', name: 'PocketID' }], []);
    mockPost.mockResolvedValue({ auth_url: 'https://login.example/authorize?state=x', state: 'x' });
    render(SecurityForm);

    expect(await screen.findByText('settings.security.oidc.notLinked')).toBeInTheDocument();
    await fireEvent.click(screen.getByRole('button', { name: 'settings.security.oidc.link' }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://login.example/authorize?state=x'));
    expect(mockPost).toHaveBeenCalledWith('/api/v1/principals/42/oidc-links/begin', { provider_id: 'PocketID' });
  });

  it('shows a linked provider account and unlinks it', async () => {
    routeGets(
      [{ id: 'PocketID', name: 'PocketID' }],
      [{ provider_id: 'PocketID', subject: 'uuid-1', email_at_provider: 'alice@private.example', linked_at: '' }],
    );
    mockDel.mockResolvedValue(undefined);
    render(SecurityForm);

    expect(
      await screen.findByText('settings.security.oidc.linkedAs:alice@private.example'),
    ).toBeInTheDocument();
    await fireEvent.click(screen.getByRole('button', { name: 'settings.security.oidc.unlink' }));

    await waitFor(() => expect(mockDel).toHaveBeenCalledWith('/api/v1/principals/42/oidc-links/PocketID'));
    expect(await screen.findByText('settings.security.oidc.notLinked')).toBeInTheDocument();
  });

  it('reports the link outcome from the callback once', async () => {
    routeGets([{ id: 'PocketID', name: 'PocketID' }], []);
    mockRouter.getParam.mockImplementation((k: string) => (k === 'oidc_link' ? 'already_linked' : null));
    render(SecurityForm);

    expect(await screen.findByTestId('oidc-link-outcome')).toHaveTextContent(
      'settings.security.oidc.outcome.already_linked',
    );
    expect(mockRouter.replace).toHaveBeenCalledWith('/settings/security');
  });
});
