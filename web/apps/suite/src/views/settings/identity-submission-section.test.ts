/**
 * IdentitySubmissionSection.svelte component tests.
 *
 * REQ-MAIL-SUBMIT-01..03: toggle, OAuth buttons, manual entry probe failure.
 * re #73: OAuth buttons gated on available_oauth_providers from the server.
 * re #74: foreign-domain identities force external submission; local-server
 *         option is not offered.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../../lib/api/identity-submission', () => ({
  getSubmission: vi.fn(async () => ({
    configured: false,
    available_oauth_providers: [],
    domain_authoritative: true,
  })),
  putSubmission: vi.fn(async () => undefined),
  deleteSubmission: vi.fn(async () => undefined),
  startOAuth: vi.fn(async () => undefined),
  testSubmission: vi.fn(async () => ({ ok: true, detail: 'message queued' })),
}));

vi.mock('../../lib/identities/identity-submission.svelte', () => {
  const mockHandle = {
    status: 'ready',
    data: {
      configured: false,
      available_oauth_providers: [] as string[],
      domain_authoritative: true,
    },
    error: null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
  };
  return {
    submissionStore: {
      forIdentity: vi.fn(() => mockHandle),
      evict: vi.fn(),
    },
    _mockHandle: mockHandle,
  };
});

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: {
    ask: vi.fn(async () => true),
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

const { startOAuth, putSubmission, testSubmission } = await import('../../lib/api/identity-submission');
const submissionModule = await import('../../lib/identities/identity-submission.svelte') as unknown as {
  submissionStore: { forIdentity: ReturnType<typeof vi.fn>; evict: ReturnType<typeof vi.fn> };
  _mockHandle: {
    status: string;
    data: {
      configured: boolean;
      state?: string;
      submit_host?: string;
      submit_port?: number;
      submit_security?: string;
      submit_auth_method?: string;
      available_oauth_providers: string[];
      domain_authoritative: boolean;
    };
    error: string | null;
    load: ReturnType<typeof vi.fn>;
    refresh: ReturnType<typeof vi.fn>;
  };
};
const { submissionStore, _mockHandle } = submissionModule;

// Minimal Identity fixture.
const IDENTITY = {
  id: 'ident-1',
  name: 'Alice',
  email: 'alice@example.com',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
};

// ── Component import (after mocks) ────────────────────────────────────────

import IdentitySubmissionSection from './IdentitySubmissionSection.svelte';

describe('IdentitySubmissionSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Reset mock handle to unconfigured default with no OAuth providers and
    // authoritative domain.
    _mockHandle.status = 'ready';
    _mockHandle.data = {
      configured: false,
      available_oauth_providers: [],
      domain_authoritative: true,
    };
    _mockHandle.error = null;
  });

  it('renders the toggle radio group for an authoritative domain', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    expect(screen.getByText('Use this server')).toBeInTheDocument();
    expect(screen.getByText('Use an external SMTP server')).toBeInTheDocument();
  });

  it('does not show the external panel when "Use this server" is selected', () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // When unconfigured and "Use this server" is selected, the
    // manual-entry fields and OAuth buttons should not be visible.
    expect(screen.queryByLabelText('Host')).not.toBeInTheDocument();
    expect(screen.queryByText('Sign in with Google')).not.toBeInTheDocument();
  });

  it('reveals the external panel when the toggle is switched', async () => {
    // Give the server both OAuth providers so we can verify button rendering.
    _mockHandle.data.available_oauth_providers = ['gmail', 'm365'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    // Host field and OAuth buttons now visible.
    expect(screen.getByText('Sign in with Google')).toBeInTheDocument();
    expect(screen.getByText('Sign in with Microsoft')).toBeInTheDocument();
  });

  // ── #73: OAuth button gating ─────────────────────────────────────────────

  it('#73: when available_oauth_providers is empty, no OAuth buttons are shown', async () => {
    // Default mock data has available_oauth_providers = [].
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    expect(screen.queryByText('Sign in with Google')).not.toBeInTheDocument();
    expect(screen.queryByText('Sign in with Microsoft')).not.toBeInTheDocument();
  });

  it('#73: when available_oauth_providers is ["gmail"], only Google button is shown', async () => {
    _mockHandle.data.available_oauth_providers = ['gmail'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    expect(screen.getByText('Sign in with Google')).toBeInTheDocument();
    expect(screen.queryByText('Sign in with Microsoft')).not.toBeInTheDocument();
  });

  it('#73: when available_oauth_providers is ["m365"], only Microsoft button is shown', async () => {
    _mockHandle.data.available_oauth_providers = ['m365'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    expect(screen.queryByText('Sign in with Google')).not.toBeInTheDocument();
    expect(screen.getByText('Sign in with Microsoft')).toBeInTheDocument();
  });

  it('#73: when available_oauth_providers has both providers, both buttons are shown', async () => {
    _mockHandle.data.available_oauth_providers = ['gmail', 'm365'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    expect(screen.getByText('Sign in with Google')).toBeInTheDocument();
    expect(screen.getByText('Sign in with Microsoft')).toBeInTheDocument();
  });

  it('#73: clicking the Google button calls startOAuth with gmail', async () => {
    _mockHandle.data.available_oauth_providers = ['gmail'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const gmailBtn = screen.getByText('Sign in with Google');
    await fireEvent.click(gmailBtn);

    expect(startOAuth).toHaveBeenCalledWith('ident-1', 'gmail');
  });

  it('#73: clicking the Microsoft button calls startOAuth with m365', async () => {
    _mockHandle.data.available_oauth_providers = ['m365'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const msBtn = screen.getByText('Sign in with Microsoft');
    await fireEvent.click(msBtn);

    expect(startOAuth).toHaveBeenCalledWith('ident-1', 'm365');
  });

  // ── #74: foreign-domain gating ───────────────────────────────────────────

  it('#74: when domain_authoritative is false, "Use this server" radio is not shown', async () => {
    _mockHandle.data.domain_authoritative = false;

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    expect(screen.queryByText('Use this server')).not.toBeInTheDocument();
    expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
  });

  it('#74: when domain_authoritative is false, no explanatory notice is shown', async () => {
    _mockHandle.data.domain_authoritative = false;

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // No role="note" element — the DKIM/DMARC notice was removed (re #74 comment 911).
    expect(screen.queryByRole('note')).not.toBeInTheDocument();
    // No DKIM text anywhere.
    expect(screen.queryByText(/DKIM/)).not.toBeInTheDocument();
  });

  it('#74: when domain_authoritative is false, the external panel is shown directly', async () => {
    _mockHandle.data.domain_authoritative = false;

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // The manual entry host field should be visible without clicking a radio.
    expect(screen.getByPlaceholderText('smtp.gmail.com')).toBeInTheDocument();
  });

  it('#74: when domain_authoritative is false and OAuth providers are available, OAuth buttons show', async () => {
    _mockHandle.data.domain_authoritative = false;
    _mockHandle.data.available_oauth_providers = ['gmail'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // OAuth button should be visible immediately (external panel is forced open).
    expect(screen.getByText('Sign in with Google')).toBeInTheDocument();
  });

  it('#74: when domain_authoritative is true, "Use this server" remains the default', async () => {
    // Default mock data has domain_authoritative: true.
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // "Use this server" radio should be checked.
    const radios = screen.getAllByRole('radio');
    expect(radios[0]).toBeChecked();
    expect(radios[1]).not.toBeChecked();

    // No foreign domain notice.
    expect(screen.queryByRole('note')).not.toBeInTheDocument();
  });

  // ── Username (auth_user) field (re #118) ─────────────────────────────────

  it('#118: username field is present when external panel is open', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    expect(screen.getByLabelText('Username')).toBeInTheDocument();
  });

  it('#118: username field defaults to the identity email', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement;
    expect(usernameInput.value).toBe(IDENTITY.email);
  });

  it('#118: username value is sent as auth_user in the PUT body', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const hostInput = screen.getByPlaceholderText('smtp.gmail.com');
    await fireEvent.input(hostInput, { target: { value: 'smtp.example.com' } });

    const usernameInput = screen.getByLabelText('Username');
    await fireEvent.input(usernameInput, { target: { value: 'custom@provider.com' } });

    const saveBtn = screen.getByRole('button', { name: 'Save and test connection' });
    await fireEvent.click(saveBtn);

    expect(putSubmission).toHaveBeenCalledWith('ident-1', expect.objectContaining({
      auth_user: 'custom@provider.com',
    }));
  });

  it('#118: auth_user defaults to identity email when username field is not changed', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const hostInput = screen.getByPlaceholderText('smtp.gmail.com');
    await fireEvent.input(hostInput, { target: { value: 'smtp.example.com' } });

    const saveBtn = screen.getByRole('button', { name: 'Save and test connection' });
    await fireEvent.click(saveBtn);

    expect(putSubmission).toHaveBeenCalledWith('ident-1', expect.objectContaining({
      auth_user: IDENTITY.email,
    }));
  });

  // ── Manual form and probe failure ────────────────────────────────────────

  it('submitting password mode issues correct PUT body shape', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    // Fill in fields.
    const hostInput = screen.getByPlaceholderText('smtp.gmail.com');
    await fireEvent.input(hostInput, { target: { value: 'smtp.example.com' } });

    // Submit the form.
    const saveBtn = screen.getByRole('button', { name: 'Save and test connection' });
    await fireEvent.click(saveBtn);

    expect(putSubmission).toHaveBeenCalledWith('ident-1', expect.objectContaining({
      submit_auth_method: 'password',
      submit_host: 'smtp.example.com',
    }));
  });

  it('422 probe failure renders inline error without closing', async () => {
    // Make putSubmission throw a 422 ApiError.
    const { ApiError } = await import('../../lib/api/client');
    vi.mocked(putSubmission).mockRejectedValueOnce(
      new ApiError(422, 'probe failed', {
        type: 'external_submission_probe_failed',
        category: 'auth-failed',
        diagnostic: '535 Bad credentials',
      }),
    );

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const hostInput = screen.getByPlaceholderText('smtp.gmail.com');
    await fireEvent.input(hostInput, { target: { value: 'smtp.example.com' } });

    const saveBtn = screen.getByRole('button', { name: 'Save and test connection' });
    await fireEvent.click(saveBtn);

    // The inline error should be visible.
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByRole('alert').textContent).toContain('535 Bad credentials');

    // The form should still be present (dialog did not close).
    expect(screen.getByText('Save and test connection')).toBeInTheDocument();
  });

  it('cancel after probe failure does not call DELETE', async () => {
    const { ApiError } = await import('../../lib/api/client');
    const { deleteSubmission } = await import('../../lib/api/identity-submission');

    vi.mocked(putSubmission).mockRejectedValueOnce(
      new ApiError(422, 'probe failed', {
        type: 'external_submission_probe_failed',
        category: 'unreachable',
        diagnostic: 'connection refused',
      }),
    );

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const hostInput = screen.getByPlaceholderText('smtp.gmail.com');
    await fireEvent.input(hostInput, { target: { value: 'smtp.example.com' } });

    const saveBtn = screen.getByRole('button', { name: 'Save and test connection' });
    await fireEvent.click(saveBtn);

    // Toggle back to "Use this server" (effectively cancelling).
    const localRadio = screen.getAllByRole('radio')[0]!;
    await fireEvent.click(localRadio);

    // DELETE should not have been called (no row was created on probe failure).
    expect(deleteSubmission).not.toHaveBeenCalled();
  });

  it('OAuth 503 error shows inline error without navigating', async () => {
    const { ApiError } = await import('../../lib/api/client');
    vi.mocked(startOAuth).mockRejectedValueOnce(
      new ApiError(503, 'oauth_provider_not_configured', {
        message: 'Gmail OAuth not configured',
      }),
    );

    _mockHandle.data.available_oauth_providers = ['gmail'];

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const gmailBtn = screen.getByText('Sign in with Google');
    await fireEvent.click(gmailBtn);

    // Inline error should appear.
    const alerts = screen.queryAllByRole('alert');
    const hasProviderError = alerts.some((a) => a.textContent?.includes('not configured'));
    expect(hasProviderError).toBe(true);
  });

  // ── OAuth2-configured state (re #105) ─────────────────────────────────────

  // ── #121: OAuth button gating on isConfigured, not isOAuthConfigured ────────

  it('#121: hides OAuth sign-in buttons when submission is already configured with password auth', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls',
      submit_auth_method: 'password',
      state: 'ok',
      available_oauth_providers: ['gmail', 'm365'],
      domain_authoritative: false, // forces external panel open
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // Even though providers are available, the identity is already configured
    // with password auth, so the OAuth sign-in buttons must NOT appear.
    expect(screen.queryByText('Sign in with Google')).not.toBeInTheDocument();
    expect(screen.queryByText('Sign in with Microsoft')).not.toBeInTheDocument();
  });

  it('#121: hides OAuth sign-in buttons when submission is configured with password auth on authoritative domain', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls',
      submit_auth_method: 'password',
      state: 'ok',
      available_oauth_providers: ['gmail'],
      domain_authoritative: true,
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // The "Use an external SMTP server" radio is the active one (configured=true
    // seeds useExternal=true); the OAuth button must still be suppressed.
    expect(screen.queryByText('Sign in with Google')).not.toBeInTheDocument();
  });

  it('hides OAuth sign-in buttons when submission is already configured with oauth2', async () => {
    // Simulate an existing oauth2 submission for smtp.gmail.com.
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.gmail.com',
      submit_port: 465,
      submit_security: 'implicit_tls',
      submit_auth_method: 'oauth2',
      state: 'ok',
      available_oauth_providers: ['gmail'],
      domain_authoritative: false, // external domain forces external mode
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // The OAuth "Sign in with Google" button must NOT appear when oauth2 is
    // already configured — it is only shown for initial setup (re #105).
    expect(screen.queryByText('Sign in with Google')).not.toBeInTheDocument();
  });

  it('shows "Test connection" button when submission is already configured with oauth2 and provider is known', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.gmail.com',
      submit_port: 465,
      submit_security: 'implicit_tls',
      submit_auth_method: 'oauth2',
      state: 'ok',
      available_oauth_providers: ['gmail'],
      domain_authoritative: false,
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // A "Test connection" button must appear to re-run the OAuth flow (re #105).
    expect(screen.getByText('Test connection')).toBeInTheDocument();
    // The disabled "Save and test connection" submit button must NOT appear.
    expect(screen.queryByText('Save and test connection')).not.toBeInTheDocument();
  });

  it('calls startOAuth when "Test connection" is clicked for an oauth2-configured submission', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.gmail.com',
      submit_port: 465,
      submit_security: 'implicit_tls',
      submit_auth_method: 'oauth2',
      state: 'ok',
      available_oauth_providers: ['gmail'],
      domain_authoritative: false,
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const testBtn = screen.getByText('Test connection');
    await fireEvent.click(testBtn);

    await vi.waitFor(() => {
      expect(vi.mocked(startOAuth)).toHaveBeenCalledWith(IDENTITY.id, 'gmail');
    });
  });

  // ── On-demand SMTP probe button (re #113, re #115) ─────────────────────────

  it('shows "Send test message" button when submission is configured (password)', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls',
      submit_auth_method: 'password',
      state: 'ok',
      available_oauth_providers: [],
      domain_authoritative: true,
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    expect(screen.getByText('Send test message')).toBeInTheDocument();
  });

  it('shows "Send test message" button when submission is oauth2-configured', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.gmail.com',
      submit_port: 465,
      submit_security: 'implicit_tls',
      submit_auth_method: 'oauth2',
      state: 'ok',
      available_oauth_providers: ['gmail'],
      domain_authoritative: false,
    };

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // Both "Send test message" (SMTP probe) and "Test connection" (OAuth re-auth)
    // must be visible — they are distinct buttons.
    expect(screen.getByText('Send test message')).toBeInTheDocument();
    expect(screen.getByText('Test connection')).toBeInTheDocument();
  });

  it('does not show "Send test message" button when submission is not configured', async () => {
    // Default mock: configured: false.
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // Toggle to external panel.
    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    expect(screen.queryByText('Send test message')).not.toBeInTheDocument();
  });

  it('clicking "Send test message" calls testSubmission and shows success result', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls',
      submit_auth_method: 'password',
      state: 'ok',
      available_oauth_providers: [],
      domain_authoritative: true,
    };

    vi.mocked(testSubmission).mockResolvedValueOnce({
      ok: true,
      detail: 'message queued',
    });

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const btn = screen.getByText('Send test message');
    await fireEvent.click(btn);

    await vi.waitFor(() => {
      expect(vi.mocked(testSubmission)).toHaveBeenCalledWith(IDENTITY.id);
    });

    // Success message renders inline.
    await vi.waitFor(() => {
      const status = screen.queryByRole('status');
      expect(status).toBeTruthy();
      expect(status!.textContent).toContain('test message was sent to your inbox');
    });
  });

  it('clicking "Send test message" shows failure result with diagnostic detail', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls',
      submit_auth_method: 'password',
      state: 'ok',
      available_oauth_providers: [],
      domain_authoritative: true,
    };

    vi.mocked(testSubmission).mockResolvedValueOnce({
      ok: false,
      detail: '535 Authentication failed',
    });

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const btn = screen.getByText('Send test message');
    await fireEvent.click(btn);

    await vi.waitFor(() => {
      const alert = screen.queryByRole('alert');
      // There should be an alert that is the test-result failure (distinct from
      // any probeError alert which would say 'Authentication failed' without the
      // 'Connection failed:' prefix).
      const testFail = Array.from(document.querySelectorAll('[role=alert]')).find(
        (el) => el.textContent?.includes('Connection failed:'),
      );
      expect(testFail).toBeTruthy();
      expect(testFail!.textContent).toContain('535 Authentication failed');
    });
  });

  it('test result is cleared when the toggle is switched', async () => {
    _mockHandle.data = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls',
      submit_auth_method: 'password',
      state: 'ok',
      available_oauth_providers: [],
      domain_authoritative: true,
    };

    vi.mocked(testSubmission).mockResolvedValueOnce({
      ok: true,
      detail: 'ok',
    });

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const btn = screen.getByText('Send test message');
    await fireEvent.click(btn);

    await vi.waitFor(() => {
      expect(screen.queryByRole('status')).toBeTruthy();
    });

    // Toggle back to "Use this server".
    const localRadio = screen.getAllByRole('radio')[0]!;
    await fireEvent.click(localRadio);

    // The success status should be gone.
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });
});
