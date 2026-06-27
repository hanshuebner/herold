/**
 * IdentityEditPage.svelte component tests.
 *
 * REQ-SET-IDENT-10..13: the per-identity editor is now a dedicated
 * settings sub-page (Item 6), not a modal. Item 7: name / display name
 * / avatar / signature / reply-to / bcc autosave on blur — there are no
 * per-section Save buttons; the external SMTP section keeps its own.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../../lib/auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
  hasIMAPImport: vi.fn(() => false),
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
  };
});

vi.mock('../../lib/api/identity-submission', () => ({
  getSubmission: vi.fn(async () => ({ configured: false })),
  putSubmission: vi.fn(async () => undefined),
  deleteSubmission: vi.fn(async () => undefined),
  startOAuth: vi.fn(async () => undefined),
}));

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map(),
    mailboxes: new Map(),
    mailAccountId: 'acct1',
    updateIdentityName: vi.fn(async () => undefined),
    updateIdentityAvatar: vi.fn(async () => undefined),
    updateIdentityXFaceEnabled: vi.fn(async () => undefined),
    deleteIdentity: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/jmap/imap-import-store.svelte', () => {
  const mockHandle = {
    account: null,
    status: 'idle',
    error: null,
    load: vi.fn(async () => undefined),
    destroy: vi.fn(async () => undefined),
  };
  return {
    imapImportStore: {
      forIdentity: vi.fn(() => mockHandle),
    },
  };
});

vi.mock('../../lib/jmap/client', () => ({
  jmap: {
    batch: vi.fn(async (configure: (b: { call: (...args: unknown[]) => void }) => void) => {
      const calls: unknown[][] = [];
      configure({ call: (...args: unknown[]) => calls.push(args) });
      return { responses: [['Identity/set', {}, 'c1']] };
    }),
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn(),
  },
  strict: vi.fn(() => undefined),
}));

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    principalId: '1',
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
      capabilities: {},
    },
  },
  registerAccountResetCallback: vi.fn(),
}));

const { hasExternalSubmission } = await import('../../lib/auth/capabilities');
const { jmap } = await import('../../lib/jmap/client');
const { mail } = await import('../../lib/mail/store.svelte');
const { toast } = await import('../../lib/toast/toast.svelte');
const { confirm } = await import('../../lib/dialog/confirm.svelte');

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

const DELETABLE_IDENTITY = {
  ...IDENTITY,
  id: 'ident-del',
  mayDelete: true,
};

import IdentityEditPage from './IdentityEditPage.svelte';

describe('IdentityEditPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(hasExternalSubmission).mockReturnValue(false);
  });

  it('renders as a page (no modal backdrop)', () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    expect(container.querySelector('[data-testid="identity-edit-page"]')).not.toBeNull();
    expect(container.querySelector('.backdrop')).toBeNull();
    expect(container.querySelector('[role="dialog"]')).toBeNull();
  });

  it('renders identity name and email', () => {
    render(IdentityEditPage, { props: { identity: IDENTITY, onback: vi.fn() } });
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('alice@example.com')).toBeInTheDocument();
  });

  it('calls onback when the back button is clicked', async () => {
    const onback = vi.fn();
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback },
    });
    const back = container.querySelector(
      '[data-testid="identity-edit-back"]',
    ) as HTMLButtonElement;
    expect(back).not.toBeNull();
    await fireEvent.click(back);
    expect(onback).toHaveBeenCalledOnce();
  });

  it('has no per-section Save button for reply-to / bcc', () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    expect(container.querySelector('[data-testid="identity-edit-save"]')).toBeNull();
  });

  it('autosaves Reply-To on blur', async () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'reply@example.com' } });
    await fireEvent.blur(replyTo);
    await vi.waitFor(() => {
      expect(vi.mocked(jmap.batch)).toHaveBeenCalled();
    });
  });

  it('does not autosave Reply-To on blur when unchanged', async () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.blur(replyTo);
    await Promise.resolve();
    expect(vi.mocked(jmap.batch)).not.toHaveBeenCalled();
  });

  it('does not autosave a malformed Reply-To', async () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'not-an-email' } });
    await fireEvent.blur(replyTo);
    await Promise.resolve();
    expect(vi.mocked(jmap.batch)).not.toHaveBeenCalled();
    // The validation error is surfaced.
    expect(
      container.querySelector('[data-testid="identity-edit-validation-error"]'),
    ).not.toBeNull();
  });

  it('shows the autosave indicator after a save', async () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'reply@example.com' } });
    await fireEvent.blur(replyTo);
    await vi.waitFor(() => {
      const status = container.querySelector('[data-testid="identity-save-status"]');
      expect(status?.getAttribute('data-save-state')).toBe('saved');
    });
  });

  it('pre-fills Reply-To from the identity', () => {
    const id = {
      ...IDENTITY,
      replyTo: [{ name: null, email: 'reply@example.com' }],
    };
    const { container } = render(IdentityEditPage, {
      props: { identity: id, onback: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    expect(replyTo.value).toBe('reply@example.com');
  });

  it('hides the submission section when capability is absent', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(false);
    render(IdentityEditPage, { props: { identity: IDENTITY, onback: vi.fn() } });
    expect(screen.queryByText('External SMTP submission')).not.toBeInTheDocument();
  });

  it('hides the submission section when the identity is unverified', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(true);
    const unverified = { ...IDENTITY, verifiedAt: null };
    render(IdentityEditPage, { props: { identity: unverified, onback: vi.fn() } });
    expect(screen.queryByText('External SMTP submission')).not.toBeInTheDocument();
  });

  // ── removeIdentity ────────────────────────────────────────────────────────

  it('hides the Remove button when mayDelete is false', () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: IDENTITY, onback: vi.fn() },
    });
    expect(
      container.querySelector('[data-testid="identity-edit-remove-btn"]'),
    ).toBeNull();
  });

  it('shows the Remove button when mayDelete is true', () => {
    const { container } = render(IdentityEditPage, {
      props: { identity: DELETABLE_IDENTITY, onback: vi.fn() },
    });
    expect(
      container.querySelector('[data-testid="identity-edit-remove-btn"]'),
    ).not.toBeNull();
  });

  it('successful delete shows success toast and calls onback, not error toast', async () => {
    const onback = vi.fn();
    vi.mocked(confirm.ask).mockResolvedValue(true);
    vi.mocked(mail.deleteIdentity).mockResolvedValue(undefined);

    const { container } = render(IdentityEditPage, {
      props: { identity: DELETABLE_IDENTITY, onback },
    });
    const removeBtn = container.querySelector(
      '[data-testid="identity-edit-remove-btn"]',
    ) as HTMLButtonElement;
    await fireEvent.click(removeBtn);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.deleteIdentity)).toHaveBeenCalledWith(DELETABLE_IDENTITY.id);
    });
    await vi.waitFor(() => {
      expect(onback).toHaveBeenCalledOnce();
    });
    // Success toast must have fired — check kind is not 'error'.
    const calls = vi.mocked(toast.show).mock.calls;
    expect(calls.length).toBeGreaterThan(0);
    const successCall = calls.find(([spec]) => spec.kind !== 'error');
    expect(successCall).toBeDefined();
    // Error toast must NOT have fired.
    const errorCall = calls.find(([spec]) => spec.kind === 'error');
    expect(errorCall).toBeUndefined();
  });

  it('server notDestroyed response surfaces the error toast and does not call onback', async () => {
    const onback = vi.fn();
    vi.mocked(confirm.ask).mockResolvedValue(true);
    vi.mocked(mail.deleteIdentity).mockRejectedValue(
      new Error('identity has dependents'),
    );

    const { container } = render(IdentityEditPage, {
      props: { identity: DELETABLE_IDENTITY, onback },
    });
    const removeBtn = container.querySelector(
      '[data-testid="identity-edit-remove-btn"]',
    ) as HTMLButtonElement;
    await fireEvent.click(removeBtn);

    await vi.waitFor(() => {
      const calls = vi.mocked(toast.show).mock.calls;
      const errorCall = calls.find(([spec]) => spec.kind === 'error');
      expect(errorCall).toBeDefined();
    });
    expect(onback).not.toHaveBeenCalled();
  });

  it('cancelling the confirm dialog does not delete or navigate', async () => {
    const onback = vi.fn();
    vi.mocked(confirm.ask).mockResolvedValue(false);

    const { container } = render(IdentityEditPage, {
      props: { identity: DELETABLE_IDENTITY, onback },
    });
    const removeBtn = container.querySelector(
      '[data-testid="identity-edit-remove-btn"]',
    ) as HTMLButtonElement;
    await fireEvent.click(removeBtn);

    await vi.waitFor(() => {
      expect(vi.mocked(confirm.ask)).toHaveBeenCalled();
    });
    await Promise.resolve();
    expect(vi.mocked(mail.deleteIdentity)).not.toHaveBeenCalled();
    expect(onback).not.toHaveBeenCalled();
    expect(vi.mocked(toast.show)).not.toHaveBeenCalled();
  });
});
