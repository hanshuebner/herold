/**
 * IdentityEditDialog.svelte component tests.
 *
 * REQ-MAIL-SUBMIT-01: identity edit dialog with submission section.
 * REQ-SET-IDENT-10..13: dialog hosts the full editor (avatar, name,
 * reply-to/bcc, signature, submission). Working-copy + Save/Cancel
 * semantics for Reply-To and Bcc.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

// Mock capabilities so we can toggle external submission on/off.
vi.mock('../../lib/auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
}));

// Mock the submission store (used by IdentitySubmissionSection, which is
// a child of IdentityEditDialog).
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

// Mock identity-submission API so network calls don't escape.
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
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

// Mock the mail store — the new dialog issues Identity/set on Save for
// the Reply-To / Bcc working copy (REQ-SET-IDENT-11) so it needs the
// mailAccountId getter and the identities cache to mirror into.
vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map(),
    mailAccountId: 'acct1',
    updateIdentityName: vi.fn(async () => undefined),
    updateIdentityAvatar: vi.fn(async () => undefined),
    updateIdentityXFaceEnabled: vi.fn(async () => undefined),
  },
}));

// Mock the JMAP client. `batch` returns a `responses` array shaped as
// the identity-edit-dialog Save handler expects: an Identity/set
// response with no `notUpdated` entries.
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
}));

// Import mocks for control in tests.
const { hasExternalSubmission } = await import('../../lib/auth/capabilities');
const { jmap } = await import('../../lib/jmap/client');
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

import IdentityEditDialog from './IdentityEditDialog.svelte';

describe('IdentityEditDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Default: external submission capability is off so no child section
    // causes import chain issues.
    vi.mocked(hasExternalSubmission).mockReturnValue(false);
  });

  it('renders identity name and email', () => {
    const onclose = vi.fn();
    render(IdentityEditDialog, { props: { identity: IDENTITY, onclose } });

    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('alice@example.com')).toBeInTheDocument();
  });

  it('shows the edit identity title', () => {
    const onclose = vi.fn();
    render(IdentityEditDialog, { props: { identity: IDENTITY, onclose } });

    expect(screen.getByText('Edit identity')).toBeInTheDocument();
  });

  it('calls onclose when the close button is clicked', async () => {
    const onclose = vi.fn();
    render(IdentityEditDialog, { props: { identity: IDENTITY, onclose } });

    const closeBtn = screen.getByLabelText('Close');
    await fireEvent.click(closeBtn);

    expect(onclose).toHaveBeenCalledOnce();
  });

  it('hides the submission section when capability is absent', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(false);

    const onclose = vi.fn();
    render(IdentityEditDialog, { props: { identity: IDENTITY, onclose } });

    // When capability is off, no submission-section heading should appear.
    expect(screen.queryByText('External SMTP submission')).not.toBeInTheDocument();
  });

  it('renders identity email in monospace identity-email class', () => {
    const onclose = vi.fn();
    render(IdentityEditDialog, { props: { identity: IDENTITY, onclose } });

    const emailEl = screen.getByText('alice@example.com');
    expect(emailEl.className).toContain('identity-email');
  });

  it('renders identity name in identity-name class', () => {
    const onclose = vi.fn();
    render(IdentityEditDialog, { props: { identity: IDENTITY, onclose } });

    const nameEl = screen.getByText('Alice');
    expect(nameEl.className).toContain('identity-name');
  });

  it('identity with no name shows email in the name position', () => {
    const onclose = vi.fn();
    const noNameIdentity = { ...IDENTITY, name: '' };
    render(IdentityEditDialog, { props: { identity: noNameIdentity, onclose } });

    // Without a name, the identity-name span renders the email address
    // as a fallback. The email also appears in the identity-email span
    // so there are two matching elements.
    const matches = screen.getAllByText('alice@example.com');
    expect(matches.length).toBeGreaterThanOrEqual(1);
    // At least one of them should be in the identity-name position.
    const hasNameSpan = matches.some((el) => el.className.includes('identity-name'));
    expect(hasNameSpan).toBe(true);
  });

  // ── Reply-To / Bcc working copy (REQ-SET-IDENT-11) ────────────────────

  it('renders empty Reply-To and Bcc inputs when identity has none', () => {
    const onclose = vi.fn();
    const { container } = render(IdentityEditDialog, {
      props: { identity: IDENTITY, onclose },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    const bcc = container.querySelector(
      '[data-testid="identity-edit-bcc"]',
    ) as HTMLInputElement;
    expect(replyTo.value).toBe('');
    expect(bcc.value).toBe('');
  });

  it('pre-fills Reply-To from the identity', () => {
    const id = {
      ...IDENTITY,
      replyTo: [{ name: null, email: 'reply@example.com' }],
    };
    const { container } = render(IdentityEditDialog, {
      props: { identity: id, onclose: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    expect(replyTo.value).toBe('reply@example.com');
  });

  it('Save button is disabled until the user edits a field', async () => {
    const { container } = render(IdentityEditDialog, {
      props: { identity: IDENTITY, onclose: vi.fn() },
    });
    const save = container.querySelector(
      '[data-testid="identity-edit-save"]',
    ) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'foo@bar.com' } });
    await vi.waitFor(() => {
      expect(save.disabled).toBe(false);
    });
  });

  it('shows the invalid-email error when Reply-To is malformed', async () => {
    const { container } = render(IdentityEditDialog, {
      props: { identity: IDENTITY, onclose: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'not-an-email' } });
    const err = container.querySelector(
      '[data-testid="identity-edit-validation-error"]',
    );
    expect(err).not.toBeNull();
    const save = container.querySelector(
      '[data-testid="identity-edit-save"]',
    ) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
  });

  it('calls Identity/set on Save with the Reply-To payload', async () => {
    const { container } = render(IdentityEditDialog, {
      props: { identity: IDENTITY, onclose: vi.fn() },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'reply@example.com' } });

    const form = replyTo.closest('form')!;
    await fireEvent.submit(form);

    await vi.waitFor(() => {
      expect(vi.mocked(jmap.batch)).toHaveBeenCalled();
    });
  });

  it('Cancel triggers the discard-confirm when there are unsaved edits', async () => {
    const onclose = vi.fn();
    const { container } = render(IdentityEditDialog, {
      props: { identity: IDENTITY, onclose },
    });
    const replyTo = container.querySelector(
      '[data-testid="identity-edit-replyto"]',
    ) as HTMLInputElement;
    await fireEvent.input(replyTo, { target: { value: 'foo@bar.com' } });
    const cancel = container.querySelector(
      '[data-testid="identity-edit-cancel"]',
    ) as HTMLButtonElement;
    await fireEvent.click(cancel);
    await vi.waitFor(() => {
      expect(vi.mocked(confirm.ask)).toHaveBeenCalled();
    });
  });

  it('Cancel closes without prompt when there are no unsaved edits', async () => {
    const onclose = vi.fn();
    const { container } = render(IdentityEditDialog, {
      props: { identity: IDENTITY, onclose },
    });
    const cancel = container.querySelector(
      '[data-testid="identity-edit-cancel"]',
    ) as HTMLButtonElement;
    await fireEvent.click(cancel);
    expect(vi.mocked(confirm.ask)).not.toHaveBeenCalled();
    expect(onclose).toHaveBeenCalled();
  });

  it('hides the external-submission section when the identity is unverified', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(true);
    const unverified = { ...IDENTITY, verifiedAt: null };
    render(IdentityEditDialog, { props: { identity: unverified, onclose: vi.fn() } });
    // The submission section heading would render if visible; with the
    // verified gate active it must not.
    expect(screen.queryByText('External SMTP submission')).not.toBeInTheDocument();
  });
});
