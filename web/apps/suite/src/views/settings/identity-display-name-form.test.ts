/**
 * IdentityDisplayNameForm.svelte component tests.
 *
 * Covers REQ-SET-02 (identity display name editing). Item 7: the form
 * autosaves on blur — there is no Save button. Feedback is the editor
 * page's shared AutosaveController, passed in as a prop.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { AutosaveController } from './autosave.svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map(),
    mailAccountId: 'acct1',
    updateIdentityName: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'settings.displayName.label': 'Display name',
      'settings.displayName.helper':
        "Used in outbound mail's From header as 'Name' <address>.",
      'settings.identityEdit.saving': 'Saving…',
      'settings.identityEdit.saved': 'Saved',
      'settings.identityEdit.saveFailed': 'Could not save',
    };
    return map[key] ?? key;
  },
}));

const { mail } = await import('../../lib/mail/store.svelte');

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

import IdentityDisplayNameForm from './IdentityDisplayNameForm.svelte';

describe('IdentityDisplayNameForm (autosave)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(mail.updateIdentityName).mockResolvedValue(undefined);
  });

  it('pre-fills the input with the current identity name', () => {
    render(IdentityDisplayNameForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    expect((screen.getByRole('textbox') as HTMLInputElement).value).toBe('Alice');
  });

  it('renders the label and helper text', () => {
    render(IdentityDisplayNameForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    expect(screen.getByText('Display name')).toBeInTheDocument();
  });

  it('has no Save button — it autosaves', () => {
    render(IdentityDisplayNameForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('autosaves on blur when the value changed', async () => {
    render(IdentityDisplayNameForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });
    await fireEvent.blur(input);
    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityName)).toHaveBeenCalledWith('default', 'Bob');
    });
  });

  it('does not save on blur when the value is unchanged', async () => {
    render(IdentityDisplayNameForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const input = screen.getByRole('textbox');
    await fireEvent.blur(input);
    await Promise.resolve();
    expect(vi.mocked(mail.updateIdentityName)).not.toHaveBeenCalled();
  });

  it('drives the autosave controller through saving -> saved', async () => {
    const autosave = new AutosaveController();
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY, autosave } });
    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });
    await fireEvent.blur(input);
    await vi.waitFor(() => {
      expect(autosave.state).toBe('saved');
    });
  });

  it('surfaces an error on the controller when the save rejects', async () => {
    vi.mocked(mail.updateIdentityName).mockRejectedValue(new Error('server error'));
    const autosave = new AutosaveController();
    render(IdentityDisplayNameForm, { props: { identity: IDENTITY, autosave } });
    const input = screen.getByRole('textbox');
    await fireEvent.input(input, { target: { value: 'Bob' } });
    await fireEvent.blur(input);
    await vi.waitFor(() => {
      expect(autosave.state).toBe('error');
      expect(autosave.errorMessage).toBe('server error');
    });
  });
});
