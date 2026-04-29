/**
 * IdentityEditDialog.svelte component tests.
 *
 * REQ-MAIL-SUBMIT-01: identity edit dialog with submission section.
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

// Import mocks for control in tests.
const { hasExternalSubmission } = await import('../../lib/auth/capabilities');

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
});
