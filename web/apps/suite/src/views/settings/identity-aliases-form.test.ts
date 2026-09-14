/**
 * IdentityAliasesForm.svelte component tests (issue #387).
 *
 * Item 7: the alias list autosaves on add / remove — no Save button.
 * Feedback is the editor page's shared AutosaveController, passed in as
 * a prop, mirroring IdentityDisplayNameForm's test shape.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { AutosaveController } from './autosave.svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map(),
    mailAccountId: 'acct1',
    updateIdentityAliases: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'settings.identityEdit.aliasesHeading': 'Alias addresses',
      'settings.identityEdit.aliasesHelper': 'Addresses that select this identity.',
      'settings.identityEdit.aliasesEmpty': 'No alias addresses yet.',
      'settings.identityEdit.aliasAddLabel': 'Alias address',
      'settings.identityEdit.aliasPlaceholder': 'alias@example.com',
      'settings.identityEdit.aliasAdd': 'Add',
      'settings.identityEdit.aliasRemoveAria': 'Remove alias {email}',
      'settings.identityEdit.aliasDuplicate': 'This address is already in the list.',
      'settings.identityEdit.aliasIsPrimary': "This is already the identity's primary address.",
      'settings.identityEdit.invalidEmail': 'Enter a valid email address or leave blank.',
      'settings.identityEdit.saving': 'Saving…',
      'settings.identityEdit.saved': 'Saved',
      'settings.identityEdit.saveFailed': 'Could not save',
    };
    let out = map[key] ?? key;
    if (params) {
      for (const [k, v] of Object.entries(params)) out = out.replace(`{${k}}`, String(v));
    }
    return out;
  },
}));

const { mail } = await import('../../lib/mail/store.svelte');

const IDENTITY = {
  id: 'ident-1',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
  aliases: ['vorsitz@classic-computing.de'],
};

import IdentityAliasesForm from './IdentityAliasesForm.svelte';

describe('IdentityAliasesForm (autosave)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(mail.updateIdentityAliases).mockResolvedValue(undefined);
  });

  it('renders the existing alias list', () => {
    render(IdentityAliasesForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    expect(screen.getByText('vorsitz@classic-computing.de')).toBeInTheDocument();
  });

  it('shows an empty-state hint when there are no aliases', () => {
    render(IdentityAliasesForm, {
      props: { identity: { ...IDENTITY, aliases: [] }, autosave: new AutosaveController() },
    });
    expect(screen.getByText('No alias addresses yet.')).toBeInTheDocument();
  });

  it('adds a new alias and persists the full list via updateIdentityAliases', async () => {
    render(IdentityAliasesForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const input = screen.getByTestId('identity-alias-input');
    await fireEvent.input(input, { target: { value: 'other@example.com' } });
    await fireEvent.click(screen.getByTestId('identity-alias-add'));

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAliases)).toHaveBeenCalledWith('ident-1', [
        'vorsitz@classic-computing.de',
        'other@example.com',
      ]);
    });
    // The new chip renders optimistically.
    expect(screen.getByText('other@example.com')).toBeInTheDocument();
    // The input clears after a successful add.
    expect((input as HTMLInputElement).value).toBe('');
  });

  it('rejects a malformed address without calling the store', async () => {
    render(IdentityAliasesForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const input = screen.getByTestId('identity-alias-input');
    await fireEvent.input(input, { target: { value: 'not-an-email' } });
    await fireEvent.click(screen.getByTestId('identity-alias-add'));

    expect(screen.getByTestId('identity-alias-error')).toHaveTextContent(
      'Enter a valid email address or leave blank.',
    );
    expect(mail.updateIdentityAliases).not.toHaveBeenCalled();
  });

  it('rejects a duplicate alias without calling the store', async () => {
    render(IdentityAliasesForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const input = screen.getByTestId('identity-alias-input');
    await fireEvent.input(input, { target: { value: 'VORSITZ@classic-computing.de' } });
    await fireEvent.click(screen.getByTestId('identity-alias-add'));

    expect(screen.getByTestId('identity-alias-error')).toHaveTextContent(
      'This address is already in the list.',
    );
    expect(mail.updateIdentityAliases).not.toHaveBeenCalled();
  });

  it('removes an alias and persists the remaining list', async () => {
    render(IdentityAliasesForm, {
      props: {
        identity: { ...IDENTITY, aliases: ['a@example.com', 'b@example.com'] },
        autosave: new AutosaveController(),
      },
    });
    const removeButtons = screen.getAllByTestId('identity-alias-remove');
    await fireEvent.click(removeButtons[0]!);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAliases)).toHaveBeenCalledWith('ident-1', [
        'b@example.com',
      ]);
    });
    expect(screen.queryByText('a@example.com')).not.toBeInTheDocument();
    expect(screen.getByText('b@example.com')).toBeInTheDocument();
  });

  it('surfaces the server invalidProperties description on the shared autosave controller and reverts the optimistic add', async () => {
    vi.mocked(mail.updateIdentityAliases).mockRejectedValue(
      new Error('alias is already claimed by another identity'),
    );
    const autosave = new AutosaveController();
    render(IdentityAliasesForm, { props: { identity: IDENTITY, autosave } });

    const input = screen.getByTestId('identity-alias-input');
    await fireEvent.input(input, { target: { value: 'taken@example.com' } });
    await fireEvent.click(screen.getByTestId('identity-alias-add'));

    await vi.waitFor(() => {
      expect(autosave.state).toBe('error');
      expect(autosave.errorMessage).toBe('alias is already claimed by another identity');
    });
    // The optimistic add is reverted on failure.
    expect(screen.queryByText('taken@example.com')).not.toBeInTheDocument();
    expect(screen.getByText('vorsitz@classic-computing.de')).toBeInTheDocument();
  });
});
