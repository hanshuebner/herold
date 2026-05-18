/**
 * IdentitySignatureForm.svelte component tests (REQ-SET-03).
 *
 * Item 7: the signature autosaves on blur — no Save button. Feedback is
 * the editor page's shared AutosaveController, passed in as a prop.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { AutosaveController } from './autosave.svelte';

// ── Mocks ─────────────────────────────────────────────────────────────────

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map(),
    mailAccountId: 'acct1',
  },
}));

vi.mock('../../lib/jmap/client', () => ({
  jmap: {
    batch: vi.fn(async (configure: (b: { call: (...args: unknown[]) => void }) => void) => {
      configure({ call: () => undefined });
      return { responses: [['Identity/set', {}, 'c1']] };
    }),
  },
  strict: vi.fn(() => undefined),
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'settings.identity.signatureLabel': 'Signature',
      'settings.identity.signatureNoAccount': 'No mail account.',
      'settings.identityEdit.saving': 'Saving…',
      'settings.identityEdit.saved': 'Saved',
      'settings.identityEdit.saveFailed': 'Could not save',
    };
    return map[key] ?? key;
  },
}));

const { jmap } = await import('../../lib/jmap/client');

const IDENTITY = {
  id: 'ident-1',
  name: 'Alice',
  email: 'alice@example.com',
  replyTo: null,
  bcc: null,
  textSignature: 'Best, Alice',
  htmlSignature: '',
  mayDelete: false,
};

import IdentitySignatureForm from './IdentitySignatureForm.svelte';

describe('IdentitySignatureForm (autosave)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('pre-fills the textarea with the current signature', () => {
    render(IdentitySignatureForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('Best, Alice');
  });

  it('has no Save button — it autosaves', () => {
    render(IdentitySignatureForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('autosaves on blur when the signature changed', async () => {
    render(IdentitySignatureForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const ta = screen.getByRole('textbox');
    await fireEvent.input(ta, { target: { value: 'Cheers, Alice' } });
    await fireEvent.blur(ta);
    await vi.waitFor(() => {
      expect(vi.mocked(jmap.batch)).toHaveBeenCalled();
    });
  });

  it('does not save on blur when the signature is unchanged', async () => {
    render(IdentitySignatureForm, {
      props: { identity: IDENTITY, autosave: new AutosaveController() },
    });
    const ta = screen.getByRole('textbox');
    await fireEvent.blur(ta);
    await Promise.resolve();
    expect(vi.mocked(jmap.batch)).not.toHaveBeenCalled();
  });

  it('drives the autosave controller to saved', async () => {
    const autosave = new AutosaveController();
    render(IdentitySignatureForm, { props: { identity: IDENTITY, autosave } });
    const ta = screen.getByRole('textbox');
    await fireEvent.input(ta, { target: { value: 'Cheers' } });
    await fireEvent.blur(ta);
    await vi.waitFor(() => {
      expect(autosave.state).toBe('saved');
    });
  });
});
