/**
 * ContactsEditView.svelte component tests (re #190).
 *
 * The edit form (an existing contactId) autosaves immediately: field edits
 * schedule a debounced Contact/set patch, using the same
 * AutosaveController/SaveStatus indicator as the Settings identity editor.
 * There is no Speichern/Abbrechen commit step in edit mode. Create mode
 * (no contactId yet) keeps the explicit Erstellen/Abbrechen commit since
 * there is no id to patch against.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'contacts.edit.title.edit': 'Kontakt bearbeiten',
      'contacts.edit.title.create': 'Neuer Kontakt',
      'contacts.edit.save': 'Speichern',
      'contacts.edit.saving': 'Wird gespeichert…',
      'contacts.edit.cancel': 'Abbrechen',
      'contacts.detail.backToDetail': 'Zurück',
      'contacts.list.title': 'Kontakte',
      'contacts.edit.autosaveBlocked': 'Nicht gespeichert — bitte markiertes Feld korrigieren.',
      'contacts.edit.removeEntry': 'Entfernen',
      'settings.identityEdit.saving': 'Saving…',
      'settings.identityEdit.saved': 'Saved',
      'settings.identityEdit.saveFailed': 'Could not save',
    };
    return map[key] ?? key;
  },
  LOCALES: ['en', 'de'],
}));

vi.mock('../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

vi.mock('../lib/contacts/store.svelte', () => ({
  contacts: { reload: vi.fn(async () => undefined) },
}));

vi.mock('../lib/contacts/list-store.svelte', () => ({
  contactsListStore: { init: vi.fn(async () => undefined) },
}));

vi.mock('../lib/router/router.svelte', () => ({
  router: { navigate: vi.fn(), parts: [], getParam: vi.fn(() => null), setParam: vi.fn() },
}));

vi.mock('../lib/auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:contacts': 'acct1' },
      capabilities: {},
    },
  },
}));

const CARD_1 = {
  id: '1',
  '@type': 'Card',
  version: '1.0',
  uid: 'uid-1',
  kind: 'individual',
  name: { full: 'Alice Example', components: [{ kind: 'given', value: 'Alice' }, { kind: 'surname', value: 'Example' }] },
  emails: { e1: { address: 'alice@example.local' } },
};

vi.mock('../lib/jmap/client', () => ({
  jmap: {
    batch: vi.fn(async (configure: (b: { call: (...args: unknown[]) => void }) => void) => {
      const calls: unknown[][] = [];
      configure({ call: (...args: unknown[]) => calls.push(args) });
      const [method] = calls[0] as [string, Record<string, unknown>];
      if (method === 'Contact/get') {
        return { responses: [['Contact/get', { list: [CARD_1] }, 'c1']], sessionState: 's1' };
      }
      // Contact/set update — default happy path; overridden per-test via mockImplementationOnce.
      return { responses: [['Contact/set', { updated: { '1': null } }, 'c1']], sessionState: 's1' };
    }),
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn(() => null),
  },
}));

vi.mock('../lib/jmap/types', () => ({
  Capability: { Contacts: 'urn:ietf:params:jmap:contacts' },
}));

const { jmap } = await import('../lib/jmap/client');

import ContactsEditView from './ContactsEditView.svelte';

describe('ContactsEditView — immediate save (re #190)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(jmap.batch).mockImplementation(
      async (configure: any) => {
        const calls: unknown[][] = [];
        configure({ call: (...args: unknown[]) => calls.push(args) });
        const [method] = calls[0] as [string, Record<string, unknown>];
        if (method === 'Contact/get') {
          return { responses: [['Contact/get', { list: [CARD_1] }, 'c1']], sessionState: 's1' };
        }
        return { responses: [['Contact/set', { updated: { '1': null } }, 'c1']], sessionState: 's1' };
      },
    );
  });

  it('edit mode has no explicit Speichern/Abbrechen buttons', async () => {
    render(ContactsEditView, { props: { contactId: '1' } });
    await screen.findByDisplayValue('Alice');
    expect(screen.queryByText('Speichern')).toBeNull();
    expect(screen.queryByText('Abbrechen')).toBeNull();
  });

  it('create mode keeps the explicit Speichern/Abbrechen buttons', async () => {
    render(ContactsEditView, { props: { contactId: null } });
    expect(await screen.findByText('Speichern')).toBeInTheDocument();
    expect(screen.getByText('Abbrechen')).toBeInTheDocument();
  });

  it('flushes a pending edit via Ctrl+S and patches Contact/set', async () => {
    render(ContactsEditView, { props: { contactId: '1' } });
    const given = await screen.findByDisplayValue('Alice');
    await fireEvent.input(given, { target: { value: 'Alicia' } });

    await fireEvent.keyDown(document, { key: 's', ctrlKey: true });

    await vi.waitFor(() => {
      const setCall = vi.mocked(jmap.batch).mock.calls.find((_, i) => i > 0);
      expect(setCall).toBeDefined();
    });
  });

  it('does not send Contact/set for an unchanged form', async () => {
    render(ContactsEditView, { props: { contactId: '1' } });
    await screen.findByDisplayValue('Alice');
    vi.mocked(jmap.batch).mockClear();

    await fireEvent.keyDown(document, { key: 's', ctrlKey: true });
    await Promise.resolve();

    expect(vi.mocked(jmap.batch)).not.toHaveBeenCalled();
  });

  it('reverts to the server value and surfaces an error when the save is rejected', async () => {
    render(ContactsEditView, { props: { contactId: '1' } });
    const given = await screen.findByDisplayValue('Alice');
    await fireEvent.input(given, { target: { value: 'Alicia' } });

    vi.mocked(jmap.batch).mockImplementationOnce(
      async (configure: any) => {
        const calls: unknown[][] = [];
        configure({ call: (...args: unknown[]) => calls.push(args) });
        return {
          responses: [[
            'Contact/set',
            { notUpdated: { '1': { type: 'invalidPatch', description: 'boom' } } },
            'c1',
          ]],
          sessionState: 's1',
        };
      },
    );

    await fireEvent.keyDown(document, { key: 's', ctrlKey: true });

    await vi.waitFor(() => {
      expect((screen.getByDisplayValue('Alice') as HTMLInputElement).value).toBe('Alice');
    });
    const status = document.querySelector('[data-testid="identity-save-status"]');
    expect(status?.getAttribute('data-save-state')).toBe('error');
  });

  it('renders the email remove control as an icon outside the chip row, and removes the row when clicked (re #193)', async () => {
    render(ContactsEditView, { props: { contactId: '1' } });
    const emailInput = await screen.findByDisplayValue('alice@example.local');
    expect(emailInput).toBeInTheDocument();

    const [firstRemoveButton] = screen.getAllByRole('button', { name: 'Entfernen' });
    expect(firstRemoveButton).toBeInTheDocument();

    // Icon control, not a plain text link: renders an <svg> (TrashIcon) and
    // carries no visible "Entfernen" text node — the old plain-text button
    // had no <svg> and its whole accessible name was its rendered text.
    expect(firstRemoveButton!.querySelector('svg')).not.toBeNull();
    expect(firstRemoveButton!.textContent?.trim()).toBe('');

    // Right-aligned outside the chip strip: no longer sits inline after the
    // "Bevorzugt" checkbox inside .chips-row.
    expect(firstRemoveButton!.closest('.chips-row')).toBeNull();

    await fireEvent.click(firstRemoveButton!);

    expect(screen.queryByDisplayValue('alice@example.local')).toBeNull();
  });
});
