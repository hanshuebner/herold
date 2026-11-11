/**
 * LoginView.svelte — external OIDC sign-in (REQ-AUTH-50..58).
 *
 *   1. Configured providers render as "Sign in with <name>" buttons.
 *   2. Clicking one starts the flow and navigates to the provider URL.
 *   3. No provider configured: no buttons, no divider.
 *   4. A refused provider sign-in (?oidc_error=not_linked) is explained
 *      and removed from the URL.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor, screen } from '@testing-library/svelte';
import LoginView from './LoginView.svelte';

const { mockAuth, mockGet, mockPost } = vi.hoisted(() => ({
  mockAuth: {
    status: 'unauthenticated' as const,
    needsStepUp: false,
    errorMessage: null as string | null,
    login: vi.fn(),
  },
  mockGet: vi.fn(),
  mockPost: vi.fn(),
}));

vi.mock('./auth.svelte', () => ({
  auth: mockAuth,
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../api/client', () => ({
  get: (...args: unknown[]) => mockGet(...args),
  post: (...args: unknown[]) => mockPost(...args),
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string>): string =>
    params ? `${key}:${Object.values(params).join(',')}` : key,
}));

describe('LoginView — external OIDC providers', () => {
  let assign: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mockGet.mockReset();
    mockPost.mockReset();
    assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, assign, search: '', pathname: '/', hash: '' });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('renders a button per configured provider and starts the flow on click', async () => {
    mockGet.mockResolvedValue({ items: [{ id: 'PocketID', name: 'PocketID' }] });
    mockPost.mockResolvedValue({ auth_url: 'https://login.example/authorize?state=s' });
    render(LoginView);

    const btn = await screen.findByRole('button', { name: 'login.oidc.signInWith:PocketID' });
    expect(mockGet).toHaveBeenCalledWith('/api/v1/auth/oidc/providers');
    await fireEvent.click(btn);

    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://login.example/authorize?state=s'));
    expect(mockPost).toHaveBeenCalledWith('/api/v1/auth/oidc/begin', { provider_id: 'PocketID' });
  });

  it('shows the server error when the flow cannot start', async () => {
    mockGet.mockResolvedValue({ items: [{ id: 'PocketID', name: 'PocketID' }] });
    mockPost.mockRejectedValue(new Error('provider unavailable'));
    render(LoginView);

    await fireEvent.click(await screen.findByRole('button', { name: 'login.oidc.signInWith:PocketID' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('provider unavailable');
    expect(assign).not.toHaveBeenCalled();
  });

  it('renders no provider section when none is configured', async () => {
    mockGet.mockResolvedValue({ items: [] });
    render(LoginView);
    await waitFor(() => expect(mockGet).toHaveBeenCalled());
    expect(document.querySelector('[data-testid="oidc-providers"]')).toBeNull();
  });

  it('explains a refused provider sign-in and strips it from the URL', async () => {
    mockGet.mockResolvedValue({ items: [] });
    const replaceState = vi.spyOn(window.history, 'replaceState');
    vi.stubGlobal('location', {
      ...window.location,
      assign,
      search: '?oidc_error=not_linked',
      pathname: '/',
      hash: '#/mail',
    });
    render(LoginView);

    expect(await screen.findByRole('alert')).toHaveTextContent('login.oidc.error.not_linked');
    expect(replaceState).toHaveBeenCalledWith(null, '', '/#/mail');
    replaceState.mockRestore();
  });
});
