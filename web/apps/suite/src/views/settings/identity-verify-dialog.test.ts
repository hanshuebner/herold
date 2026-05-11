/**
 * Component tests for IdentityVerifyDialog (REQ-SET-IDENT-20..21).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import type { Identity } from '../../lib/mail/types';

// ── Mocks ─────────────────────────────────────────────────────────────────

vi.mock('../../lib/api/identity-verify', () => ({
  postVerifyCode: vi.fn(async () => undefined),
  postVerifyResend: vi.fn(async () => ({ retryAfter: null })),
  retryAfterOf: vi.fn((err: unknown) => {
    if (err && typeof err === 'object' && 'retryAfter' in err) {
      const v = (err as { retryAfter: number | null }).retryAfter;
      return typeof v === 'number' ? v : null;
    }
    return null;
  }),
}));

vi.mock('../../lib/api/client', async () => {
  const actual = await vi.importActual<typeof import('../../lib/api/client')>(
    '../../lib/api/client',
  );
  return actual;
});

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map<string, Identity>(),
    loadIdentities: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'settings.identityVerify.title': 'Verify identity',
      'settings.identityVerify.close': 'Close',
      'settings.identityVerify.intro': `We sent a verification message to ${params?.email ?? ''}.`,
      'settings.identityVerify.codeLabel': 'Verification code',
      'settings.identityVerify.verify': 'Verify',
      'settings.identityVerify.verifying': 'Verifying…',
      'settings.identityVerify.resend': 'Resend email',
      'settings.identityVerify.resending': 'Resending…',
      'settings.identityVerify.resendOk': 'Verification email sent again.',
      'settings.identityVerify.resendRateLimited': `Try again in ${params?.seconds ?? ''} s.`,
      'settings.identityVerify.resendRateLimitedShort': 'Wait before resending.',
      'settings.identityVerify.successToast': `Verified ${params?.email ?? ''}`,
      'settings.identityVerify.cancel': 'Cancel',
      'settings.identityWizard.codeInvalid': 'Enter exactly 6 digits.',
      'settings.identityWizard.codeWrong': 'That code did not match.',
    };
    return map[key] ?? key;
  },
}));

// ── Imports after mocks ───────────────────────────────────────────────────

const { postVerifyCode, postVerifyResend } = await import('../../lib/api/identity-verify');
const { mail } = await import('../../lib/mail/store.svelte');
const { toast } = await import('../../lib/toast/toast.svelte');
const { ApiError } = await import('../../lib/api/client');

import IdentityVerifyDialog from './IdentityVerifyDialog.svelte';

// ── Fixture ───────────────────────────────────────────────────────────────

const PENDING: Identity = {
  id: '3',
  name: 'Pending',
  email: 'pending@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
  verifiedAt: null,
  verificationPendingSince: '2026-05-09T00:00:00Z',
};

// ── Tests ─────────────────────────────────────────────────────────────────

describe('IdentityVerifyDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the dialog with the identity email in the intro', () => {
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose: vi.fn() },
    });
    const intro = container.querySelector('[data-testid="identity-verify-intro"]');
    expect(intro?.textContent).toContain('pending@example.local');
  });

  it('disables the Verify button until 6 digits are entered', async () => {
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose: vi.fn() },
    });
    const verifyBtn = container.querySelector(
      '[data-testid="identity-verify-submit"]',
    ) as HTMLButtonElement;
    expect(verifyBtn.disabled).toBe(true);

    const input = container.querySelector(
      '[data-testid="identity-verify-code"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: '123' } });
    expect(verifyBtn.disabled).toBe(true);

    await fireEvent.input(input, { target: { value: '123456' } });
    expect(verifyBtn.disabled).toBe(false);
  });

  it('strips non-digits and caps at 6 characters', async () => {
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose: vi.fn() },
    });
    const input = container.querySelector(
      '[data-testid="identity-verify-code"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'ab12c34de5678f' } });
    // 12345678 → first 6 digits → 123456.
    expect(input.value).toBe('123456');
  });

  it('calls postVerifyCode + closes + emits toast on success', async () => {
    const onclose = vi.fn();
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose },
    });
    const input = container.querySelector(
      '[data-testid="identity-verify-code"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: '123456' } });
    const verifyBtn = container.querySelector(
      '[data-testid="identity-verify-submit"]',
    ) as HTMLButtonElement;
    await fireEvent.click(verifyBtn);
    await vi.waitFor(() => {
      expect(vi.mocked(postVerifyCode)).toHaveBeenCalledWith('3', '123456');
    });
    expect(vi.mocked(mail.loadIdentities)).toHaveBeenCalled();
    expect(onclose).toHaveBeenCalled();
    expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining('Verified pending@example.local') }),
    );
  });

  it('surfaces an inline error on a wrong-code response', async () => {
    vi.mocked(postVerifyCode).mockRejectedValueOnce(
      new ApiError(400, 'code did not match'),
    );
    const onclose = vi.fn();
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose },
    });
    const input = container.querySelector(
      '[data-testid="identity-verify-code"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: '999999' } });
    const verifyBtn = container.querySelector(
      '[data-testid="identity-verify-submit"]',
    ) as HTMLButtonElement;
    await fireEvent.click(verifyBtn);
    await vi.waitFor(() => {
      const err = container.querySelector('[data-testid="identity-verify-code-error"]');
      expect(err?.textContent).toContain('did not match');
    });
    expect(onclose).not.toHaveBeenCalled();
  });

  it('calls postVerifyResend on resend click and surfaces the OK notice', async () => {
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose: vi.fn() },
    });
    const resendBtn = container.querySelector(
      '[data-testid="identity-verify-resend"]',
    ) as HTMLButtonElement;
    await fireEvent.click(resendBtn);
    await vi.waitFor(() => {
      expect(vi.mocked(postVerifyResend)).toHaveBeenCalledWith('3');
    });
    await vi.waitFor(() => {
      // After the resolved resend the cooldown is 60 — the OK notice
      // is overridden by the countdown display.
      const cooldown = container.querySelector('[data-testid="identity-verify-cooldown"]');
      expect(cooldown).not.toBeNull();
    });
  });

  it('renders the Retry-After countdown on a 429 from resend', async () => {
    const err = new ApiError(429, 'rate-limited');
    (err as InstanceType<typeof ApiError> & { retryAfter: number | null }).retryAfter = 47;
    vi.mocked(postVerifyResend).mockRejectedValueOnce(err);
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose: vi.fn() },
    });
    const resendBtn = container.querySelector(
      '[data-testid="identity-verify-resend"]',
    ) as HTMLButtonElement;
    await fireEvent.click(resendBtn);
    await vi.waitFor(() => {
      const cooldown = container.querySelector(
        '[data-testid="identity-verify-cooldown"]',
      );
      expect(cooldown?.textContent).toContain('47');
    });
    expect(resendBtn.disabled).toBe(true);
  });

  it('closes via the cancel button', async () => {
    const onclose = vi.fn();
    const { container } = render(IdentityVerifyDialog, {
      props: { identity: PENDING, onclose },
    });
    const cancelBtn = container.querySelector(
      '[data-testid="identity-verify-cancel"]',
    ) as HTMLButtonElement;
    await fireEvent.click(cancelBtn);
    expect(onclose).toHaveBeenCalled();
  });
});
