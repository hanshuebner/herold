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
    deleteIdentity: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: {
    ask: vi.fn(async () => true),
    pending: null,
    decide: vi.fn(),
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
      'settings.identityList.editBtn': 'Edit',
      'settings.identityList.defaultBadge': 'Default',
      'settings.identityList.defaultChanged': 'Default identity updated',
      'settings.identityList.defaultChangeFailed':
        'Could not change default identity',
      'settings.identityList.rowMenuAria': `Actions for ${params?.email ?? ''}`,
      'settings.identityList.deleteBtn': 'Delete',
      'settings.identityList.deleteConfirmTitle': 'Delete this identity?',
      'settings.identityList.deleteConfirmMessage': `Delete the identity ${params?.email ?? ''}?`,
      'settings.identityList.deleteConfirm': 'Delete identity',
      'settings.identityList.deleted': `Identity ${params?.email ?? ''} deleted`,
      'settings.identityList.deleteFailed': 'Could not delete the identity',
      'settings.identityWizard.cancel': 'Cancel',
    };
    return map[key] ?? key;
  },
}));

// ── Imports after mocks ──────────────────────────────────────────────────

const { mail } = await import('../../lib/mail/store.svelte');
const { toast } = await import('../../lib/toast/toast.svelte');
const { confirm } = await import('../../lib/dialog/confirm.svelte');

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

  it('opens the editor when the kebab menu Edit item is clicked (re #20)', async () => {
    seedIdentities(VERIFIED_SECOND);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    const editItem = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-row-menu-edit"]',
    ) as HTMLButtonElement;
    expect(editItem).not.toBeNull();
    await fireEvent.click(editItem);
    expect(onedit).toHaveBeenCalledWith(VERIFIED_SECOND);
  });

  it('renders a per-row kebab menu trigger on every row (re #20)', () => {
    seedIdentities(VERIFIED_DEFAULT, UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const triggers = container.querySelectorAll(
      '[data-testid="identity-row-menu-trigger"]',
    );
    expect(triggers.length).toBe(2);
  });

  it('the kebab menu is closed until its trigger is clicked', async () => {
    seedIdentities(VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    expect(
      container.querySelector('[data-testid="identity-row-menu"]'),
    ).toBeNull();
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    expect(
      container.querySelector('[data-testid="identity-row-menu"]'),
    ).not.toBeNull();
  });

  it('hides the Delete item for an identity whose mayDelete is false (re #20)', async () => {
    // The synthesized default identity (id "default") cannot be deleted.
    const synthDefault = makeIdentity('default', 'alice@example.local', {
      name: 'Alice',
      verifiedAt: '2026-01-01T00:00:00Z',
      isDefault: true,
      mayDelete: false,
    });
    seedIdentities(synthDefault);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="default"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    expect(
      container.querySelector('[data-testid="identity-row-menu-edit"]'),
    ).not.toBeNull();
    expect(
      container.querySelector('[data-testid="identity-row-menu-delete"]'),
    ).toBeNull();
  });

  it('shows the Delete item for a deletable identity (re #20)', async () => {
    seedIdentities(VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    expect(
      container.querySelector('[data-testid="identity-row-menu-delete"]'),
    ).not.toBeNull();
  });

  it('confirms then calls deleteIdentity when Delete is chosen (re #20)', async () => {
    vi.mocked(confirm.ask).mockResolvedValueOnce(true);
    seedIdentities(VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-delete"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(confirm.ask)).toHaveBeenCalled();
      expect(vi.mocked(mail.deleteIdentity)).toHaveBeenCalledWith('2');
    });
    expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining('deleted'),
      }),
    );
  });

  it('does not call deleteIdentity when the confirm dialog is cancelled (re #20)', async () => {
    vi.mocked(confirm.ask).mockResolvedValueOnce(false);
    seedIdentities(VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-delete"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(confirm.ask)).toHaveBeenCalled();
    });
    expect(vi.mocked(mail.deleteIdentity)).not.toHaveBeenCalled();
  });

  it('surfaces an error toast when deleteIdentity rejects (re #20)', async () => {
    vi.mocked(confirm.ask).mockResolvedValueOnce(true);
    vi.mocked(mail.deleteIdentity).mockRejectedValueOnce(new Error('boom'));
    seedIdentities(VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="2"] [data-testid="identity-row-menu-delete"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'Could not delete the identity',
          kind: 'error',
        }),
      );
    });
  });

  // ── re #18: row body is the click-to-edit surface ─────────────────────
  //
  // The card area carries role=button + onclick that fires `onedit`; the
  // radio sits OUTSIDE the card (its own column) so a click on the
  // default-selector never also opens the editor. Interactive children
  // inside the card (status buttons, kebab) stopPropagation so they only
  // fire their own handlers.

  it('renders the row body as a click target (role=button, focusable)', () => {
    seedIdentities(VERIFIED_DEFAULT);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const card = container.querySelector(
      '[data-identity-id="1"] [data-testid="identity-card-body"]',
    ) as HTMLElement;
    expect(card).not.toBeNull();
    expect(card.getAttribute('role')).toBe('button');
    expect(card.getAttribute('tabindex')).toBe('0');
  });

  it('opens the editor when the card body is clicked (re #18)', async () => {
    seedIdentities(VERIFIED_SECOND);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const card = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-card-body"]',
    ) as HTMLElement;
    expect(card).not.toBeNull();
    await fireEvent.click(card);
    expect(onedit).toHaveBeenCalledWith(VERIFIED_SECOND);
  });

  it('opens the editor when Enter is pressed on the focused card body (re #18)', async () => {
    seedIdentities(VERIFIED_SECOND);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const card = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-card-body"]',
    ) as HTMLElement;
    await fireEvent.keyDown(card, { key: 'Enter' });
    expect(onedit).toHaveBeenCalledWith(VERIFIED_SECOND);
  });

  it('places the radio outside the card body (re #18)', () => {
    seedIdentities(VERIFIED_DEFAULT);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const row = container.querySelector('[data-identity-id="1"]') as HTMLElement;
    const radio = row.querySelector(
      '[data-testid="identity-default-radio"]',
    ) as HTMLElement;
    const card = row.querySelector(
      '[data-testid="identity-card-body"]',
    ) as HTMLElement;
    expect(radio).not.toBeNull();
    expect(card).not.toBeNull();
    // The radio must NOT be a descendant of the card body, so clicking
    // the radio cannot also fire the card's onclick handler.
    expect(card.contains(radio)).toBe(false);
  });

  it('does not open the editor when the radio is clicked', async () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const radio = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-default-radio"]',
    ) as HTMLInputElement;
    await fireEvent.click(radio);
    expect(onedit).not.toHaveBeenCalled();
  });

  it('does not open the editor when the Verify button is clicked', async () => {
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

  it('does not open the editor when the kebab trigger is clicked (re #18)', async () => {
    seedIdentities(VERIFIED_SECOND);
    const onedit = vi.fn();
    const { container } = render(IdentityList, { props: { onedit } });
    const kebab = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-row-menu-trigger"]',
    ) as HTMLButtonElement;
    await fireEvent.click(kebab);
    expect(onedit).not.toHaveBeenCalled();
  });

  it('shows the Delete item for an unverified identity with mayDelete=true (re #22)', async () => {
    seedIdentities(UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="4"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    expect(
      container.querySelector('[data-testid="identity-row-menu-delete"]'),
    ).not.toBeNull();
  });

  it('shows the Delete item for a verification-pending identity with mayDelete=true (re #22)', async () => {
    seedIdentities(PENDING);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="3"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    expect(
      container.querySelector('[data-testid="identity-row-menu-delete"]'),
    ).not.toBeNull();
  });

  it('calls deleteIdentity for an unverified identity after confirmation (re #22)', async () => {
    vi.mocked(confirm.ask).mockResolvedValueOnce(true);
    seedIdentities(UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="4"] [data-testid="identity-row-menu-trigger"]',
      ) as HTMLButtonElement,
    );
    await fireEvent.click(
      container.querySelector(
        '[data-identity-id="4"] [data-testid="identity-row-menu-delete"]',
      ) as HTMLButtonElement,
    );
    await vi.waitFor(() => {
      expect(vi.mocked(mail.deleteIdentity)).toHaveBeenCalledWith('4');
    });
    expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining('deleted') }),
    );
  });

  it('renders a static Standard badge on the default row only', () => {
    seedIdentities(VERIFIED_DEFAULT, VERIFIED_SECOND);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const defaultBadge = container.querySelector(
      '[data-identity-id="1"] [data-testid="identity-default-badge"]',
    );
    const nonDefaultBadge = container.querySelector(
      '[data-identity-id="2"] [data-testid="identity-default-badge"]',
    );
    expect(defaultBadge).not.toBeNull();
    expect(nonDefaultBadge).toBeNull();
  });

  it('verification status renders as a non-interactive label, not a button', () => {
    seedIdentities(UNVERIFIED);
    const { container } = render(IdentityList, { props: { onedit: vi.fn() } });
    const chip = container.querySelector('[data-testid="identity-chip"]');
    expect(chip).not.toBeNull();
    // The status label is a <span>, never a <button>.
    expect(chip?.tagName).toBe('SPAN');
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
