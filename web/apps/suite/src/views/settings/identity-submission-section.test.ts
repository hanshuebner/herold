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

const { startOAuth, putSubmission } = await import('../../lib/api/identity-submission');
const submissionModule = await import('../../lib/identities/identity-submission.svelte') as unknown as {
  submissionStore: { forIdentity: ReturnType<typeof vi.fn>; evict: ReturnType<typeof vi.fn> };
  _mockHandle: {
    status: string;
    data: {
      configured: boolean;
      state?: string;
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

  it('#74: when domain_authoritative is false, a notice about the foreign domain is shown', async () => {
    _mockHandle.data.domain_authoritative = false;

    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    // The foreign domain notice should be rendered with role="note".
    expect(screen.getByRole('note')).toBeInTheDocument();
    // The notice should mention external SMTP or DKIM.
    const notice = screen.getByRole('note');
    expect(notice.textContent).toMatch(/DKIM/);
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
      auth_method: 'password',
      host: 'smtp.example.com',
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
});
