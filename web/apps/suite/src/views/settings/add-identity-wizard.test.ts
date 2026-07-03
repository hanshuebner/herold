/**
 * Component tests for AddIdentityWizard (REQ-SET-IDENT-30..33).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import type { Identity } from '../../lib/mail/types';

// ── Mocks ─────────────────────────────────────────────────────────────────

vi.mock('../../lib/auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => true),
  hasIdentityVerification: vi.fn(() => true),
}));

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

vi.mock('../../lib/api/identity-submission', () => ({
  putSubmission: vi.fn(async () => undefined),
  deleteSubmission: vi.fn(async () => undefined),
  startOAuth: vi.fn(async () => undefined),
  getSubmission: vi.fn(async () => ({ configured: false })),
  // Default: detect-provider returns null → code flow unchanged.
  detectProvider: vi.fn(async () => null),
}));

vi.mock('../../lib/identities/identity-submission.svelte', () => {
  const handle = {
    status: 'idle',
    data: null,
    error: null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
  };
  return {
    submissionStore: {
      forIdentity: vi.fn(() => handle),
      evict: vi.fn(),
    },
  };
});

// Stub the mail store. IdentitySetError is a pure structured-error
// type the wizard needs for `instanceof` checks; the class is defined
// inside the factory because vi.mock is hoisted above module scope.
vi.mock('../../lib/mail/store.svelte', () => {
  class IdentitySetError extends Error {
    readonly type: string;
    readonly properties: string[];
    constructor(type: string, description?: string, properties: string[] = []) {
      super(description ?? type);
      this.name = 'IdentitySetError';
      this.type = type;
      this.properties = properties;
    }
  }
  return {
    IdentitySetError,
    mail: {
      identities: new Map<string, Identity>(),
      loadIdentities: vi.fn(async () => undefined),
      createIdentity: vi.fn(async (email: string, name: string) => ({
        id: 'new-identity',
        name,
        email,
        replyTo: null,
        bcc: null,
        textSignature: '',
        htmlSignature: '',
        mayDelete: true,
        verifiedAt: null,
        verificationPendingSince: '2026-05-11T00:00:00Z',
      } as Identity)),
    },
  };
});

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'settings.identityWizard.title': 'Add identity',
      'settings.identityWizard.close': 'Close',
      'settings.identityWizard.cancel': 'Cancel',
      'settings.identityWizard.next': 'Next',
      'settings.identityWizard.create': 'Create',
      'settings.identityWizard.creating': 'Creating…',
      'settings.identityWizard.step1Title': 'Address',
      'settings.identityWizard.step1Intro': 'Step 1 intro.',
      'settings.identityWizard.emailLabel': 'Email address',
      'settings.identityWizard.emailHelper': 'Email helper.',
      'settings.identityWizard.emailInvalid': 'Enter a valid email address.',
      'settings.identityWizard.displayNameLabel': 'Display name (optional)',
      'settings.identityWizard.displayNameHelper': 'Name helper.',
      'settings.identityWizard.domainBlocked': `Blocked: ${params?.domain ?? ''}`,
      'settings.identityWizard.emailExists':
        'An identity with this email address already exists.',
      'settings.identityWizard.createFailed':
        'Could not create the identity. Please try again.',
      'settings.identityWizard.step2Title': 'Confirm',
      'settings.identityWizard.step2Intro': `Sent to ${params?.email ?? ''}.`,
      'settings.identityWizard.codeLabel': 'Verification code',
      'settings.identityWizard.codeHelper': 'Code helper.',
      'settings.identityWizard.codeInvalid': 'Enter exactly 6 digits.',
      'settings.identityWizard.codeWrong': 'That code did not match.',
      'settings.identityWizard.verify': 'Verify',
      'settings.identityWizard.verifying': 'Verifying…',
      'settings.identityWizard.resend': 'Resend email',
      'settings.identityWizard.resending': 'Resending…',
      'settings.identityWizard.resendOk': 'Resent.',
      'settings.identityWizard.resendRateLimited': `Try in ${params?.seconds ?? ''} s.`,
      'settings.identityWizard.resendRateLimitedShort': 'Wait before resending.',
      'settings.identityWizard.cancelPendingNotice': 'Closing keeps it pending.',
      'settings.identityWizard.step3Title': 'Configure external SMTP',
      'settings.identityWizard.step3Intro': `External: ${params?.domain ?? ''}.`,
      'settings.identityWizard.step3SkipNote': 'You can skip.',
      'settings.identityWizard.smtpHost': 'SMTP host',
      'settings.identityWizard.smtpPort': 'Port',
      'settings.identityWizard.smtpUser': 'Username',
      'settings.identityWizard.smtpPassword': 'Password',
      'settings.identityWizard.smtpSecurity': 'Security',
      'settings.identityWizard.smtpSecurityStartTLS': 'STARTTLS',
      'settings.identityWizard.smtpSecurityImplicit': 'Implicit TLS',
      'settings.identityWizard.smtpSecurityNone': 'None (plain)',
      'settings.identityWizard.smtpHostRequired': 'Host required.',
      'settings.identityWizard.smtpUserRequired': 'Username required.',
      'settings.identityWizard.smtpPasswordRequired': 'Password required.',
      'settings.identityWizard.smtpPortInvalid': 'Port invalid.',
      'settings.identityWizard.smtpSaveError': `Error: ${params?.message ?? ''}`,
      'settings.identityWizard.successToast': `Verified ${params?.email ?? ''}`,
      'settings.identityWizard.createdToast': `Sent to ${params?.email ?? ''}`,
      'settings.identityWizard.done': 'Done',
      'settings.identityWizard.skip': 'Skip',
    };
    return map[key] ?? key;
  },
}));

// ── Imports after mocks ───────────────────────────────────────────────────

const { mail, IdentitySetError } = await import('../../lib/mail/store.svelte');
const { postVerifyCode, postVerifyResend } = await import(
  '../../lib/api/identity-verify'
);
const { putSubmission, startOAuth, detectProvider } = await import(
  '../../lib/api/identity-submission'
);
const { ApiError } = await import('../../lib/api/client');
const { toast } = await import('../../lib/toast/toast.svelte');
const { hasExternalSubmission } = await import('../../lib/auth/capabilities');

import AddIdentityWizard from './AddIdentityWizard.svelte';

const HOSTED = new Set(['example.local']);

// ── Helpers ───────────────────────────────────────────────────────────────

/**
 * Fill the step-2 six-box CodeInput one digit per box, mirroring how a
 * user types the verification code.
 */
async function typeWizardCode(
  container: HTMLElement,
  digits: string,
): Promise<void> {
  const boxes = Array.from(
    container.querySelectorAll<HTMLInputElement>(
      '[data-testid^="identity-wizard-code-"]',
    ),
  );
  for (let i = 0; i < digits.length && i < boxes.length; i++) {
    const box = boxes[i]!;
    box.value = digits[i]!;
    await fireEvent.input(box);
  }
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('AddIdentityWizard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders Step 1 on open', () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    expect(container.querySelector('[data-testid="identity-wizard-step-1"]')).not.toBeNull();
  });

  it('disables Next while the email is invalid', async () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const next = container.querySelector(
      '[data-testid="identity-wizard-next-step1"]',
    ) as HTMLButtonElement;
    expect(next.disabled).toBe(true);
    const input = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'not-an-email' } });
    expect(next.disabled).toBe(true);
    await fireEvent.input(input, { target: { value: 'alice2@example.local' } });
    expect(next.disabled).toBe(false);
  });

  it('does not show the email error while the user is still typing (validate-on-blur)', async () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const input = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    // Typing an invalid value without blurring must NOT surface the error.
    await fireEvent.input(input, { target: { value: 'not-an-email' } });
    expect(
      container.querySelector('[data-testid="identity-wizard-email-error"]'),
    ).toBeNull();
  });

  it('shows the email error after blur with an invalid value', async () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const input = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'not-an-email' } });
    // Error must not appear yet.
    expect(
      container.querySelector('[data-testid="identity-wizard-email-error"]'),
    ).toBeNull();
    // Blur triggers validation.
    await fireEvent.blur(input);
    const err = container.querySelector('[data-testid="identity-wizard-email-error"]');
    expect(err?.textContent).toContain('valid email');
  });

  it('clears the email error once the value becomes valid', async () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const input = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    // Establish blurred-invalid state.
    await fireEvent.input(input, { target: { value: 'not-an-email' } });
    await fireEvent.blur(input);
    expect(
      container.querySelector('[data-testid="identity-wizard-email-error"]'),
    ).not.toBeNull();
    // Correct the email: error should disappear as the value is now valid.
    await fireEvent.input(input, { target: { value: 'alice@example.com' } });
    expect(
      container.querySelector('[data-testid="identity-wizard-email-error"]'),
    ).toBeNull();
  });

  it('never renders a step-progress indicator on any step (re #94)', async () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    // Step 1: no indicator.
    expect(
      container.querySelector('[data-testid="identity-wizard-step-indicator"]'),
    ).toBeNull();

    // Advance to Step 2.
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    // Step 2: still no indicator.
    expect(
      container.querySelector('[data-testid="identity-wizard-step-indicator"]'),
    ).toBeNull();
  });

  it('advances to Step 2 on successful create (hosted domain)', async () => {
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    const nameInput = container.querySelector(
      '[data-testid="identity-wizard-display-name"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.input(nameInput, { target: { value: 'Alice 2' } });
    const next = container.querySelector(
      '[data-testid="identity-wizard-next-step1"]',
    ) as HTMLButtonElement;
    await fireEvent.click(next);
    await vi.waitFor(() => {
      // detectProvider returns null (mock default) so no OAuth path;
      // the third opts argument is undefined for the non-OAuth case.
      expect(vi.mocked(mail.createIdentity)).toHaveBeenCalledWith(
        'alice2@example.local',
        'Alice 2',
        undefined,
      );
    });
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
  });

  it('surfaces a forbiddenFrom error inline on create failure', async () => {
    vi.mocked(mail.createIdentity).mockRejectedValueOnce(
      new Error('domain not permitted by server policy'),
    );
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@blocked.example' } });
    const next = container.querySelector(
      '[data-testid="identity-wizard-next-step1"]',
    ) as HTMLButtonElement;
    await fireEvent.click(next);
    await vi.waitFor(() => {
      const err = container.querySelector('[data-testid="identity-wizard-create-error"]');
      expect(err?.textContent).toContain('Blocked: blocked.example');
    });
  });

  it('maps an invalidProperties/email setError to the localized duplicate message (re #21)', async () => {
    // The server rejects a create for an already-registered email with
    // a structured invalidProperties error naming `email`. The wizard
    // must surface a localized string, not the raw English description.
    vi.mocked(mail.createIdentity).mockRejectedValueOnce(
      new IdentitySetError(
        'invalidProperties',
        'an identity with this email already exists',
        ['email'],
      ),
    );
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      const err = container.querySelector('[data-testid="identity-wizard-create-error"]');
      expect(err?.textContent).toContain('already exists');
    });
    // The raw English server description must not leak through.
    const err = container.querySelector('[data-testid="identity-wizard-create-error"]');
    expect(err?.textContent).not.toContain('an identity with this email');
  });

  it('falls back to a generic message for an unrecognized setError', async () => {
    vi.mocked(mail.createIdentity).mockRejectedValueOnce(
      new IdentitySetError('serverFail', 'internal allocator error'),
    );
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      const err = container.querySelector('[data-testid="identity-wizard-create-error"]');
      expect(err?.textContent).toContain('Could not create the identity');
    });
  });

  it('closes immediately on Step 1 cancel (no identity created)', async () => {
    const onclose = vi.fn();
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose },
    });
    const cancel = container.querySelector(
      '[data-testid="identity-wizard-cancel-step1"]',
    ) as HTMLButtonElement;
    await fireEvent.click(cancel);
    expect(onclose).toHaveBeenCalled();
    expect(vi.mocked(mail.createIdentity)).not.toHaveBeenCalled();
  });

  it('closes immediately on the first Step 2 cancel click and shows a toast', async () => {
    const onclose = vi.fn();
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    const cancel = container.querySelector(
      '[data-testid="identity-wizard-cancel-step2"]',
    ) as HTMLButtonElement;
    await fireEvent.click(cancel);
    // First click closes — no two-click confirmation.
    expect(onclose).toHaveBeenCalledOnce();
    // The "stays pending" message is surfaced as a toast, not inline.
    expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'Closing keeps it pending.' }),
    );
    // The old inline notice element no longer exists.
    expect(
      container.querySelector('[data-testid="identity-wizard-cancel-notice"]'),
    ).toBeNull();
  });

  it('closes after verification on a hosted-domain identity', async () => {
    const onclose = vi.fn();
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    // oncomplete fires on the sixth digit; no Verify button click needed.
    await typeWizardCode(container, '123456');
    await vi.waitFor(() => {
      expect(vi.mocked(postVerifyCode)).toHaveBeenCalledWith('new-identity', '123456');
    });
    await vi.waitFor(() => {
      expect(onclose).toHaveBeenCalled();
    });
  });

  it('advances to Step 3 on an external-domain identity', async () => {
    vi.mocked(mail.createIdentity).mockResolvedValueOnce({
      id: 'new-identity',
      name: '',
      email: 'alice2@gmail.com',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: true,
      verifiedAt: null,
      verificationPendingSince: '2026-05-11T00:00:00Z',
    });
    // After verification the cache contains the verified row; the
    // wizard re-reads the email from the cache for domain detection.
    (mail.identities as Map<string, Identity>).set('new-identity', {
      id: 'new-identity',
      name: '',
      email: 'alice2@gmail.com',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: true,
      verifiedAt: '2026-05-11T01:00:00Z',
    });
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@gmail.com' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    // oncomplete fires on the sixth digit; no Verify button click needed.
    await typeWizardCode(container, '654321');
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-3"]'),
      ).not.toBeNull();
    });
  });

  it('Step 3 Skip closes the wizard without calling putSubmission', async () => {
    vi.mocked(mail.createIdentity).mockResolvedValueOnce({
      id: 'new-identity',
      name: '',
      email: 'alice2@gmail.com',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: true,
      verifiedAt: null,
      verificationPendingSince: '2026-05-11T00:00:00Z',
    });
    (mail.identities as Map<string, Identity>).set('new-identity', {
      id: 'new-identity',
      name: '',
      email: 'alice2@gmail.com',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: true,
      verifiedAt: '2026-05-11T01:00:00Z',
    });
    const onclose = vi.fn();
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@gmail.com' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    // oncomplete fires on the sixth digit; no Verify button click needed.
    await typeWizardCode(container, '654321');
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-3"]'),
      ).not.toBeNull();
    });
    const skip = container.querySelector(
      '[data-testid="identity-wizard-skip-step3"]',
    ) as HTMLButtonElement;
    await fireEvent.click(skip);
    expect(onclose).toHaveBeenCalled();
    expect(vi.mocked(putSubmission)).not.toHaveBeenCalled();
  });

  // ── detect-provider / OAuth routing (re #92) ─────────────────────────────

  it('calls startOAuth with gmail when detect-provider returns google and external submission is enabled', async () => {
    vi.mocked(detectProvider).mockResolvedValueOnce('google');
    // startOAuth resolves: browser would navigate away. The wizard does NOT
    // advance to step 2 (early return in onStep1Submit).
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(startOAuth)).toHaveBeenCalledWith('new-identity', 'gmail');
    });
    // skipVerificationEmail must be true when OAuth will prove ownership (re #105).
    expect(vi.mocked(mail.createIdentity)).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(String),
      { skipVerificationEmail: true },
    );
    // Because startOAuth resolved (mock), the wizard returned early and
    // step 2 is NOT shown.
    expect(
      container.querySelector('[data-testid="identity-wizard-step-2"]'),
    ).toBeNull();
  });

  it('calls startOAuth with m365 when detect-provider returns microsoft', async () => {
    vi.mocked(detectProvider).mockResolvedValueOnce('microsoft');
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(startOAuth)).toHaveBeenCalledWith('new-identity', 'm365');
    });
    expect(vi.mocked(mail.createIdentity)).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(String),
      { skipVerificationEmail: true },
    );
    expect(
      container.querySelector('[data-testid="identity-wizard-step-2"]'),
    ).toBeNull();
  });

  it('falls back to the code step when detect-provider returns google but startOAuth returns 503', async () => {
    vi.mocked(detectProvider).mockResolvedValueOnce('google');
    vi.mocked(startOAuth).mockRejectedValueOnce(new ApiError(503, 'provider not configured'));
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(startOAuth)).toHaveBeenCalledWith('new-identity', 'gmail');
    });
    // skipVerificationEmail was true; startOAuth 503 → postVerifyResend called
    // so the user has a code to enter in step 2 (re #105).
    await vi.waitFor(() => {
      expect(vi.mocked(postVerifyResend)).toHaveBeenCalledWith('new-identity');
    });
    // startOAuth threw 503: fall through to verification-code step.
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
  });

  it('goes to the code step when detect-provider returns null (no detected provider)', async () => {
    // detectProvider default mock returns null.
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    expect(vi.mocked(startOAuth)).not.toHaveBeenCalled();
  });

  it('goes to the code step when detect-provider errors (network failure)', async () => {
    vi.mocked(detectProvider).mockRejectedValueOnce(new Error('network failure'));
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    expect(vi.mocked(startOAuth)).not.toHaveBeenCalled();
  });

  it('goes to the code step when detect-provider returns google but external submission is disabled', async () => {
    vi.mocked(detectProvider).mockResolvedValueOnce('google');
    vi.mocked(hasExternalSubmission).mockReturnValueOnce(false);
    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    expect(vi.mocked(startOAuth)).not.toHaveBeenCalled();
  });

  it('renders the Retry-After countdown on a 429 from resend', async () => {
    const err = new ApiError(429, 'rate-limited');
    (err as InstanceType<typeof ApiError> & { retryAfter: number | null }).retryAfter = 47;
    vi.mocked(postVerifyResend).mockRejectedValueOnce(err);

    const { container } = render(AddIdentityWizard, {
      props: { hostedDomains: HOSTED, onclose: vi.fn() },
    });
    const emailInput = container.querySelector(
      '[data-testid="identity-wizard-email"]',
    ) as HTMLInputElement;
    await fireEvent.input(emailInput, { target: { value: 'alice2@example.local' } });
    await fireEvent.click(
      container.querySelector(
        '[data-testid="identity-wizard-next-step1"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(
        container.querySelector('[data-testid="identity-wizard-step-2"]'),
      ).not.toBeNull();
    });
    const resend = container.querySelector(
      '[data-testid="identity-wizard-resend"]',
    ) as HTMLButtonElement;
    await fireEvent.click(resend);
    await vi.waitFor(() => {
      const cooldown = container.querySelector('[data-testid="identity-wizard-cooldown"]');
      expect(cooldown?.textContent).toContain('47');
    });
  });
});
