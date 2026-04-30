/**
 * IdentityDisplayNameForm.svelte component tests.
 *
 * Covers REQ-SET-02 (identity display name editing): pre-fill, correct
 * Identity/set payload, and local identity cache update on success.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

// vi.mock is hoisted; capture the function via the mock module itself.
vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map([
      [
        'default',
        {
          id: 'default',
          name: 'Alice',
          email: 'alice@example.local',
          replyTo: null,
          bcc: null,
          textSignature: '',
          htmlSignature: '',
          mayDelete: false,
        },
      ],
    ]),
    mailAccountId: 'acct1',
    updateIdentityName: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'settings.displayName.label': 'Display name',
      'settings.displayName.helper':
        "Used in outbound mail's From header as 'Name' <address>.",
      'settings.save': 'Save',
      'settings.saved': 'Settings saved',
      'settings.saveFailed': 'Could not save settings',
    };
    return map[key] ?? key;
  },
}));

// ── Import mocked modules for assertion ──────────────────────────────────

const { mail } = await import('../../lib/mail/store.svelte');
const { toast } = await import('../../lib/toast/toast.svelte');

// ── Fixtures ─────────────────────────────────────────────────────────────

const IDENTITY = {
  id: 'default',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
};

// ── Tests ─────────────────────────────────────────────────────────────────

import IdentityDisplayNameForm from './IdentityDisplayNameForm.svelte';

describe('IdentityDisplayNameForm', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(mail.updateIdentityName).mockResolvedValue(undefined);
  });

  it('pre-fills the input with the current identity name', () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    expect((input as HTMLInputElement).value).toBe('Alice');
  });

  it('pre-fills with empty string when identity name is empty', () => {
    const noName = { ...IDENTITY, name: '' };
    render(IdentityDisplayNameForm, { props: { identity: noName } });

    const input = screen.getByRole('textbox');
    expect((input as HTMLInputElement).value).toBe('');
  });

  it('renders the label and helper text', () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    expect(screen.getByText('Display name')).toBeInTheDocument();
    expect(
      screen.getByText("Used in outbound mail's From header as 'Name' <address>."),
    ).toBeInTheDocument();
  });

  it('Save button is disabled when value has not changed', () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const saveBtn = screen.getByRole('button', { name: 'Save' });
    expect(saveBtn).toBeDisabled();
  });

  it('Save button becomes enabled after the user edits the field', async () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Alice B.' } });

    const saveBtn = screen.getByRole('button', { name: 'Save' });
    expect(saveBtn).not.toBeDisabled();
  });

  it('calls updateIdentityName with the correct identity id and new name on submit', async () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });

    const form = input.closest('form')!;
    await fireEvent.submit(form);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityName)).toHaveBeenCalledWith('default', 'Bob');
    });
  });

  it('shows a success toast on save', async () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });

    const form = input.closest('form')!;
    await fireEvent.submit(form);

    await vi.waitFor(() => {
      expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Settings saved' }),
      );
    });
  });

  it('clears dirty state after a successful save', async () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });

    const form = input.closest('form')!;
    await fireEvent.submit(form);

    await vi.waitFor(() => {
      const saveBtn = screen.getByRole('button', { name: 'Save' });
      expect(saveBtn).toBeDisabled();
    });
  });

  it('shows an error toast and keeps dirty state when updateIdentityName rejects', async () => {
    vi.mocked(mail.updateIdentityName).mockRejectedValue(new Error('server error'));

    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });

    const form = input.closest('form')!;
    await fireEvent.submit(form);

    await vi.waitFor(() => {
      expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Could not save settings', kind: 'error' }),
      );
    });

    // Save button must remain enabled so the user can retry.
    const saveBtn = screen.getByRole('button', { name: 'Save' });
    expect(saveBtn).not.toBeDisabled();
  });

  it('Revert button restores the original value', async () => {
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY } });

    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Changed' } });

    const revertBtn = screen.getByRole('button', { name: 'Revert' });
    await fireEvent.click(revertBtn);

    expect((input as HTMLInputElement).value).toBe('Alice');
  });
});
