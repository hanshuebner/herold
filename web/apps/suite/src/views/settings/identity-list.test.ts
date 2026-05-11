/**
 * IdentityList.svelte component tests (REQ-SET-IDENT-01..08).
 *
 * Covers:
 *   - Render with mixed-state identities (verified, verifying,
 *     unverified, external-no-submission).
 *   - Chip text per state.
 *   - Default-radio enablement gate (verified-only).
 *   - Default-radio promotion calls `mail.setDefaultIdentity`.
 *   - Row-click opens the edit dialog (callback fires).
 *   - Add / Verify / Resend buttons render but do not throw (stubs).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import type { Identity } from '../../lib/mail/types';

// ── Mocks ─────────────────────────────────────────────────────────────────

vi.mock('../../lib/auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
  hasIdentityVerification: vi.fn(() => true),
}));

// `mail` store: shape only what IdentityList uses.
vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map<string, Identity>(),
    setDefaultIdentity: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

vi.mock('../../lib/identities/identity-submission.svelte', () => {
  const mockHandle = {
    status: 'idle',
    data: null,
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

vi.mock('../../lib/mail/identity-avatar', () => ({
  identityAvatarUrl: vi.fn(() => null),
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    const map: Record<string, string> = {
      'settings.account.noIdentities': 'No identities loaded yet.',
      'settings.identityList.heading': 'Identities',
      'settings.identityList.addBtn': 'Add identity',
      'settings.identityList.addTooltip': 'Add a new sending identity',
      'settings.identityList.chip.verified': 'Verified',
      'settings.identityList.chip.verifying': 'Verification pending',
      'settings.identityList.chip.unverified': 'Unverified',
      'settings.identityList.verifyBtn': 'Verify',
      'settings.identityList.resendBtn': 'Resend',
      'settings.identityList.verifyTooltip': 'Enter the verification code',
      'settings.identityList.resendTooltip': 'Send the verification email again',
      'settings.identityList.defaultRadioDisabledTitle':
        'Only verified identities can be the default.',
      'settings.identityList.defaultRadioAria': `Set ${params?.email ?? ''} as default`,
      'settings.identityList.editRowAria': `Edit ${params?.email ?? ''}`,
      'settings.identityList.defaultChanged': 'Default identity updated',
      'settings.identityList.defaultChangeFailed':
        'Could not change default identity',
    };
    return map[key] ?? key;
  },
}));

// ── Imports after mocks ──────────────────────────────────────────────────

const { mail } = await import('../../lib/mail/store.svelte');
const { toast } = await import('../../lib/toast/toast.svelte');

import IdentityList from './IdentityList.svelte';

// ── Fixtures ─────────────────────────────────────────────────────────────

function makeIdentity(
  id: string,
  email: string,
  overrides: Partial<Identity> = {},
): Identity {
  return {
    id,
    name: '',
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    ...overrides,
  };
}

const VERIFIED_DEFAULT: Identity = makeIdentity('1', 'alice@example.local', {
  name: 'Alice',
  verifiedAt: '2026-01-01T00:00:00Z',
  isDefault: true,
});

const VERIFIED_SECOND: Identity = makeIdentity('2', 'bob@example.local', {
  name: 'Bob',
  verifiedAt: '2026-02-01T00:00:00Z',
});

const PENDING: Identity = makeIdentity('3', 'pending@example.local', {
  verifiedAt: null,
  verificationPendingSince: '2026-05-09T00:00:00Z',
});

const UNVERIFIED: Identity = makeIdentity('4', 'unverified@example.local', {
  verifiedAt: null,
});

function seedIdentities(...ids: Identity[]): void {
  // mail.identities is a Map<string, Identity> on the singleton; rewrite it
  // in place so each test starts from a known state.
  (mail as { identities: Map<string, Identity> }).identities = new Map(
    ids.map((id) => [id.id, id]),
  );
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('IdentityList', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    seedIdentities();
  });

  it('renders one row per identity', () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND, PENDING, UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const rows = container.querySelectorAll('[data-testid="identity-row"]');
    expect(rows.length).toBe(4);
  });

  it('renders the "Add identity" button when the identity-verification capability is present', () => {
    seedIdentities(VERIFIED_DEFAULT);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const btn = container.querySelector('[data-testid="identity-add-btn"]');
    expect(btn).not.toBeNull();
    expect(btn?.getAttribute('title')).toBe('Add a new sending identity');
  });

  it('hides the "Add identity" button when the identity-verification capability is absent', async () => {
    const mod = await import('../../lib/auth/capabilities');
    vi.mocked(mod.hasIdentityVerification).mockReturnValueOnce(false);
    seedIdentities(VERIFIED_DEFAULT);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const btn = container.querySelector('[data-testid="identity-add-btn"]');
    expect(btn).toBeNull();
  });

  it('invokes onadd when the Add identity button is clicked', async () => {
    seedIdentities(VERIFIED_DEFAULT);
    const onadd = vi.fn();
    const { container } = render(IdentityList, {
      props: { onedit: vi.fn(), onadd },
    });
    const btn = container.querySelector(
      '[data-testid="identity-add-btn"]',
    ) as HTMLButtonElement | null;
    expect(btn).not.toBeNull();
    await fireEvent.click(btn!);
    expect(onadd).toHaveBeenCalledTimes(1);
  });

  it('invokes onverify when the Verify button is clicked on an unverified row', async () => {
    seedIdentities(UNVERIFIED);
    const onverify = vi.fn();
    const { container } = render(IdentityList, {
      props: { onedit: vi.fn(), onverify },
    });
    const btn = container.querySelector(
      '[data-testid="identity-verify-btn"]',
    ) as HTMLButtonElement | null;
    expect(btn).not.toBeNull();
    await fireEvent.click(btn!);
    expect(onverify).toHaveBeenCalledWith(UNVERIFIED);
  });

  it('invokes onresend when the Resend button is clicked on a pending row', async () => {
    seedIdentities(PENDING);
    const onresend = vi.fn();
    const { container } = render(IdentityList, {
      props: { onedit: vi.fn(), onresend },
    });
    const btn = container.querySelector(
      '[data-testid="identity-resend-btn"]',
    ) as HTMLButtonElement | null;
    expect(btn).not.toBeNull();
    await fireEvent.click(btn!);
    expect(onresend).toHaveBeenCalledWith(PENDING);
  });

  it('renders the verifying chip on verification-pending rows', () => {
    seedIdentities(PENDING);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const chip = container.querySelector('[data-testid="identity-chip"]');
    expect(chip?.textContent?.trim()).toBe('Verification pending');
  });

  it('renders the unverified chip on unverified rows', () => {
    seedIdentities(UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const chip = container.querySelector('[data-testid="identity-chip"]');
    expect(chip?.textContent?.trim()).toBe('Unverified');
  });

  it('renders no chip on verified rows', () => {
    seedIdentities(VERIFIED_DEFAULT);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const chip = container.querySelector('[data-testid="identity-chip"]');
    expect(chip).toBeNull();
  });

  it('disables the default-radio on unverified rows', () => {
    seedIdentities(VERIFIED_DEFAULT, UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const radios = container.querySelectorAll<HTMLInputElement>(
      '[data-testid="identity-default-radio"]',
    );
    expect(radios.length).toBe(2);
    // The unverified row's radio is disabled.
    const unverifiedRow = container.querySelector(
      '[data-identity-id="4"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement | null;
    expect(unverifiedRow?.disabled).toBe(true);
  });

  it('disables the default-radio on verifying rows', () => {
    seedIdentities(VERIFIED_DEFAULT, PENDING);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const pendingRadio = container.querySelector(
      '[data-identity-id="3"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement | null;
    expect(pendingRadio?.disabled).toBe(true);
  });

  it('marks the default row with the default class', () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const defaultRow = container.querySelector('[data-identity-id="1"]');
    const nonDefaultRow = container.querySelector('[data-identity-id="2"]');
    expect(defaultRow?.classList.contains('default')).toBe(true);
    expect(nonDefaultRow?.classList.contains('default')).toBe(false);
  });

  it('promotes a verified non-default row to default on radio change', async () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const radio = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement;
    expect(radio).not.toBeNull();
    await fireEvent.click(radio);
    await fireEvent.change(radio);
    await vi.waitFor(() => {
      expect(vi.mocked(mail.setDefaultIdentity)).toHaveBeenCalledWith('2');
    });
    expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'Default identity updated' }),
    );
  });

  it('does not call setDefaultIdentity when the same row is re-selected', async () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const radio = container.querySelector(
      '[data-identity-id="1"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement;
    await fireEvent.change(radio);
    // Allow microtasks to flush.
    await Promise.resolve();
    expect(vi.mocked(mail.setDefaultIdentity)).not.toHaveBeenCalled();
  });

  it('surfaces an error toast when setDefaultIdentity rejects', async () => {
    vi.mocked(mail.setDefaultIdentity).mockRejectedValueOnce(new Error('boom'));
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const radio = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement;
    await fireEvent.change(radio);
    await vi.waitFor(() => {
      expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'Could not change default identity',
          kind: 'error',
        }),
      );
    });
  });

  it('opens the edit dialog when the row body is clicked', async () => {
    seedIdentities(VERIFIED_DEFAULT);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const rowBody = container.querySelector(
      '[data-identity-id="1"] .row-body',
    ) as HTMLElement;
    expect(rowBody).not.toBeNull();
    await fireEvent.click(rowBody);
    expect(onedit).toHaveBeenCalledWith(VERIFIED_DEFAULT);
  });

  it('does not open the edit dialog when the radio is clicked', async () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const radio = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement;
    await fireEvent.click(radio);
    expect(onedit).not.toHaveBeenCalled();
  });

  it('does not open the edit dialog when the Verify button is clicked', async () => {
    seedIdentities(UNVERIFIED);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const verifyBtn = container.querySelector(
      '[data-testid="identity-verify-btn"]',
    ) as HTMLButtonElement;
    expect(verifyBtn).not.toBeNull();
    await fireEvent.click(verifyBtn);
    expect(onedit).not.toHaveBeenCalled();
  });

  it('shows the Resend button on a verifying row', () => {
    seedIdentities(PENDING);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const resendBtn = container.querySelector(
      '[data-testid="identity-resend-btn"]',
    );
    expect(resendBtn).not.toBeNull();
  });

  it('shows the empty-state message when no identities are present', () => {
    seedIdentities();
    const { getByText } = render(IdentityList, { props: { onedit: vi.fn() } });
    expect(getByText('No identities loaded yet.')).toBeInTheDocument();
  });

  it('sorts the default identity first', () => {
    // Out-of-order input; the list must sort default-first.
    seedIdentities(VERIFIED_SECOND, UNVERIFIED, VERIFIED_DEFAULT, PENDING);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const rows = container.querySelectorAll('[data-testid="identity-row"]');
    expect(rows[0]?.getAttribute('data-identity-id')).toBe('1'); // default
    expect(rows[rows.length - 1]?.getAttribute('data-identity-id')).toBe('4'); // unverified
  });

  it('applies the disabled class to unverified rows (external-without-submission gate)', () => {
    seedIdentities(VERIFIED_DEFAULT, UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const unverifiedRow = container.querySelector('[data-identity-id="4"]');
    expect(unverifiedRow?.classList.contains('disabled')).toBe(true);
  });
});
