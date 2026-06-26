/**
 * LoginView.svelte — empty-field validation (re #53).
 *
 * Verifies that submitting the sign-in form with an empty field:
 *   1. Does NOT call auth.login (no network request is sent).
 *   2. Shows the login.fieldsRequired validation message.
 *   3. Never surfaces raw "HTTP 400" text in the error area.
 *
 * The TOTP step-up auto-submit path is not blocked by the validation
 * because auth.needsStepUp is false in all cases below (that path is
 * exercised separately in the live-browser puppeteer session).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import LoginView from './LoginView.svelte';

// ── Auth mock ─────────────────────────────────────────────────────────────
// vi.mock factories are hoisted to the top of the file by vitest, so any
// variable referenced inside one must be defined via vi.hoisted() rather
// than a plain const — otherwise the variable is not yet initialised when
// the factory runs.

const { mockLogin } = vi.hoisted(() => ({
  mockLogin: vi.fn(),
}));

vi.mock('./auth.svelte', () => ({
  auth: {
    status: 'unauthenticated',
    needsStepUp: false,
    errorMessage: null,
    login: mockLogin,
  },
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
