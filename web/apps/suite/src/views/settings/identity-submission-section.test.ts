/**
 * IdentitySubmissionSection.svelte component tests.
 *
 * REQ-MAIL-SUBMIT-01..03: toggle, OAuth buttons, manual entry probe failure.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../../lib/api/identity-submission', () => ({
  getSubmission: vi.fn(async () => ({ configured: false })),
  putSubmission: vi.fn(async () => undefined),
  deleteSubmission: vi.fn(async () => undefined),
  startOAuth: vi.fn(async () => undefined),
}));

vi.mock('../../lib/identities/identity-submission.svelte', () => {
  const mockHandle = {
    status: 'ready',
    data: { configured: false },
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
    data: { configured: boolean; state?: string };
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
    // Reset mock handle to unconfigured default.
    _mockHandle.status = 'ready';
    _mockHandle.data = { configured: false };
    _mockHandle.error = null;
  });

  it('renders the toggle radio group', async () => {
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
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    // Host field and OAuth buttons now visible.
    expect(screen.getByText('Sign in with Google')).toBeInTheDocument();
    expect(screen.getByText('Sign in with Microsoft')).toBeInTheDocument();
  });

  it('OAuth buttons are shown by default (server 503 surfaces inline)', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    // Both OAuth buttons are visible; the suite defaults to showing both
    // and handles 503 inline (per spec option (b) fall-through).
    expect(screen.getByText('Sign in with Google')).toBeInTheDocument();
    expect(screen.getByText('Sign in with Microsoft')).toBeInTheDocument();
  });

  it('clicking Sign in with Google calls startOAuth with gmail', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const gmailBtn = screen.getByText('Sign in with Google');
    await fireEvent.click(gmailBtn);

    expect(startOAuth).toHaveBeenCalledWith('ident-1', 'gmail');
  });

  it('clicking Sign in with Microsoft calls startOAuth with m365', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    const msBtn = screen.getByText('Sign in with Microsoft');
    await fireEvent.click(msBtn);

    expect(startOAuth).toHaveBeenCalledWith('ident-1', 'm365');
  });

  it('submitting password mode issues correct PUT body shape', async () => {
    render(IdentitySubmissionSection, { props: { identity: IDENTITY } });

    const externalRadio = screen.getAllByRole('radio')[1]!;
    await fireEvent.click(externalRadio);

    // Fill in fields.
    const hostInput = screen.getByPlaceholderText('smtp.gmail.com');
    await fireEvent.input(hostInput, { target: { value: 'smtp.example.com' } });

    // Submit the form.
    const saveBtn = screen.getByText('Save and test connection');
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

    const saveBtn = screen.getByText('Save and test connection');
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

    const saveBtn = screen.getByText('Save and test connection');
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
