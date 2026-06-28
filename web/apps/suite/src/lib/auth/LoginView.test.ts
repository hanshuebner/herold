/**
 * LoginView.svelte — empty-field validation (re #53), TOTP step-up
 * error-message gating (re #29), and TOTP autofocus (re #68).
 *
 * Verifies that submitting the sign-in form with an empty field:
 *   1. Does NOT call auth.login (no network request is sent).
 *   2. Shows the login.fieldsRequired validation message.
 *   3. Never surfaces raw "HTTP 400" text in the error area.
 *
 * Also verifies the TOTP step-up error-message gating:
 *   4. When the password step triggers step-up, the TOTP form appears with
 *      NO error message.
 *   5. When a submitted TOTP code is rejected, the wrong-code message appears.
 *   6. When a correct TOTP code is submitted, login() is called with no error.
 *
 * And TOTP autofocus (re #68):
 *   7. The TOTP input is focused after a rejected code (tick()+focus() path).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import LoginView from './LoginView.svelte';

// ── Auth mock ─────────────────────────────────────────────────────────────
// vi.mock factories are hoisted to the top of the file by vitest, so any
// variable referenced inside one must be defined via vi.hoisted() rather
// than a plain const — otherwise the variable is not yet initialised when
// the factory runs.
//
// mockAuth is a plain mutable object so individual tests can adjust
// needsStepUp before rendering (or via mockLogin side-effects) without
// needing Svelte reactive state.

const { mockLogin, mockAuth } = vi.hoisted(() => {
  const mockLogin = vi.fn();
  const mockAuth = {
    status: 'unauthenticated' as const,
    needsStepUp: false,
    errorMessage: null as string | null,
    login: mockLogin,
  };
  return { mockLogin, mockAuth };
});

vi.mock('./auth.svelte', () => ({
  auth: mockAuth,
  registerAccountResetCallback: vi.fn(),
}));

// ── i18n mock — return the key so assertions are stable ───────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => key,
}));

// ── Helpers ───────────────────────────────────────────────────────────────

/**
 * Fill in the email input. Svelte 5 bind:value syncs on 'input' events;
 * fireEvent.input with { target: { value } } is the testing-library idiom.
 */
async function fillEmail(value: string): Promise<void> {
  const input = document.querySelector<HTMLInputElement>('#email')!;
  await fireEvent.input(input, { target: { value } });
}

async function fillPassword(value: string): Promise<void> {
  const input = document.querySelector<HTMLInputElement>('#password')!;
  await fireEvent.input(input, { target: { value } });
}

async function submitForm(): Promise<void> {
  const form = document.querySelector('form')!;
  await fireEvent.submit(form);
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('LoginView — empty-field validation (re #53)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAuth.needsStepUp = false;
    mockAuth.errorMessage = null;
  });

  it('does not call auth.login when password is empty', async () => {
    render(LoginView);
    await fillEmail('alice@example.local');
    await submitForm();
    expect(mockLogin).not.toHaveBeenCalled();
  });

  it('does not call auth.login when email is empty', async () => {
    render(LoginView);
    await fillPassword('testpass123...');
    await submitForm();
    expect(mockLogin).not.toHaveBeenCalled();
  });

  it('does not call auth.login when both fields are empty', async () => {
    render(LoginView);
    await submitForm();
    expect(mockLogin).not.toHaveBeenCalled();
  });

  it('does not call auth.login when email is whitespace only', async () => {
    render(LoginView);
    await fillEmail('   ');
    await fillPassword('testpass123...');
    await submitForm();
    expect(mockLogin).not.toHaveBeenCalled();
  });

  it('shows the login.fieldsRequired message when password is empty', async () => {
    render(LoginView);
    await fillEmail('alice@example.local');
    await submitForm();
    await waitFor(() => {
      const error = document.querySelector('.error');
      expect(error).not.toBeNull();
      expect(error!.textContent).toContain('login.fieldsRequired');
    });
  });

  it('shows the login.fieldsRequired message when email is empty', async () => {
    render(LoginView);
    await fillPassword('testpass123...');
    await submitForm();
    await waitFor(() => {
      const error = document.querySelector('.error');
      expect(error).not.toBeNull();
      expect(error!.textContent).toContain('login.fieldsRequired');
    });
  });

  it('does not show "HTTP 400" text when a field is empty', async () => {
    render(LoginView);
    await fillEmail('alice@example.local');
    await submitForm();
    // No network request fires; the error shown must be the validation message,
    // never the raw HTTP status that the server would return.
    const error = document.querySelector('.error');
    if (error) {
      expect(error.textContent).not.toContain('HTTP 400');
    }
    expect(mockLogin).not.toHaveBeenCalled();
  });

  it('calls auth.login when both fields are filled', async () => {
    // login resolves to undefined (success handled by auth state machine).
    mockLogin.mockResolvedValue(undefined);
    render(LoginView);
    await fillEmail('alice@example.local');
    await fillPassword('testpass123...');
    await submitForm();
    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith({
        email: 'alice@example.local',
        password: 'testpass123...',
        totpCode: undefined,
      });
    });
  });
});

describe('LoginView — TOTP step-up error gating (re #29)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAuth.needsStepUp = false;
    mockAuth.errorMessage = null;
  });

  it('shows no error when the password step triggers step-up', async () => {
    // Simulate auth.login() setting needsStepUp = true and throwing.
    // wasStepUp is false at the start of doLogin(), so the catch block
    // must NOT set errorMessage.
    mockLogin.mockImplementationOnce(() => {
      mockAuth.needsStepUp = true;
      return Promise.reject(new Error('Enter your two-factor authentication code to continue.'));
    });
    render(LoginView);
    await fillEmail('alice@example.local');
    await fillPassword('testpass123...');
    await submitForm();
    await waitFor(() => {
      // submitting goes false after finally; wait for async completion.
      expect(mockLogin).toHaveBeenCalledOnce();
    });
    // The TOTP form is now required, but the error element must be absent.
    const error = document.querySelector('.error');
    expect(error).toBeNull();
  });

  it('shows the wrong-code error when a submitted TOTP code is rejected', async () => {
    // Render with needsStepUp already true so the TOTP field is in the DOM
    // from the initial render (the mock object is plain, not reactive, so
    // the template evaluates needsStepUp once at mount time).
    //
    // We do NOT enter six digits here — entering six digits triggers
    // onTotpInput's auto-submit path which would consume the mock before
    // the explicit submitForm() call. Instead we submit with an empty
    // totpCode; the validation bypass (needsStepUp=true) lets it through.
    mockAuth.needsStepUp = true;
    // auth.login() throws; needsStepUp stays true.
    mockLogin.mockRejectedValueOnce(new Error('Invalid TOTP code.'));
    render(LoginView);
    // The TOTP field is rendered because needsStepUp was true at mount.
    await submitForm();
    await waitFor(() => {
      const error = document.querySelector('.error');
      expect(error).not.toBeNull();
      expect(error!.textContent).toContain('login.totpWrongCode');
    });
  });

  it('calls login and shows no error on successful TOTP submit', async () => {
    mockAuth.needsStepUp = true;
    mockLogin.mockResolvedValueOnce(undefined);
    render(LoginView);
    await submitForm();
    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledOnce();
    });
    expect(document.querySelector('.error')).toBeNull();
  });

  it('focuses the TOTP input after a submitted code is rejected', async () => {
    // Render with step-up already active so the TOTP input is in the DOM.
    // After auth.login() rejects, doLogin() calls tick() then focus().
    // This exercises the same tick()+focus() code path as the initial
    // step-up transition; the initial-transition case (where the {#if}
    // block renders the TOTP input for the first time) is verified via the
    // puppeteer browser test against a live instance (re #68).
    mockAuth.needsStepUp = true;
    mockLogin.mockRejectedValueOnce(new Error('Invalid TOTP code.'));
    const { container } = render(LoginView);
    // The mock auth is not reactive, so the TOTP input rendered on mount
    // (needsStepUp was true at render time). Submit to trigger doLogin().
    await submitForm();
    await waitFor(() => {
      const totpInput = container.querySelector<HTMLInputElement>('#totp-code');
      expect(totpInput).not.toBeNull();
      expect(document.activeElement).toBe(totpInput);
    });
  });
});
