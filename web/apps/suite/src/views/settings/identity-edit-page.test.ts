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
    mailAccountId: 'acct1',
    updateIdentityName: vi.fn(async () => undefined),
    updateIdentityAvatar: vi.fn(async () => undefined),
    updateIdentityXFaceEnabled: vi.fn(async () => undefined),
  },
}));

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
});
