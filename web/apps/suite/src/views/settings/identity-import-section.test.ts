/**
 * IdentityImportSection.svelte unit tests.
 *
 * REQ-SET-IMAPIMP-01..04 / REQ-IMAP-IMP-61 / REQ-IMAP-IMP-90 /
 * REQ-IMAP-IMP-100..104.
 *
 * Covers:
 *   - Loading spinner renders while handle.status === 'idle' | 'loading'
 *   - Unconfigured state shows the "Set up mail import" button
 *   - Configured state shows the status card with state, sync time, error
 *   - Setup form opens and validates required fields
 *   - Remove dialog shows keep-or-delete choice
 *   - Complete migration confirm dialog renders
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

/** Shared mock handle state; mutated per test. */
const mockHandleState = {
  status: 'idle' as 'idle' | 'loading' | 'ready' | 'error',
  account: null as null | Record<string, unknown>,
  error: null as string | null,
  load: vi.fn(async () => undefined),
  refresh: vi.fn(async () => undefined),
  create: vi.fn(async () => ({ id: 'acc1', identityId: 'id1', accountName: 'Test' })),
  update: vi.fn(async () => ({})),
  destroy: vi.fn(async () => undefined),
};

vi.mock('../../lib/jmap/imap-import-store.svelte', () => ({
  imapImportStore: {
    forIdentity: vi.fn(() => mockHandleState),
  },
}));

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    mailboxes: new Map([
      ['mb1', { id: 'mb1', name: 'Gmail Import', totalEmails: 42 }],
    ]),
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string>) => {
    if (params) {
      return Object.entries(params).reduce(
        (s, [k, v]) => s.replace(`{${k}}`, v),
        key,
      );
    }
    return key;
  },
}));

// ── Test identity ─────────────────────────────────────────────────────────

import IdentityImportSection from './IdentityImportSection.svelte';

const identity = {
  id: 'id1',
  name: 'Test User',
  email: 'test@external.example',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
  verifiedAt: '2026-01-01T00:00:00Z',
};

type SectionProps = ComponentProps<typeof IdentityImportSection>;

function renderSection(props: Partial<SectionProps> = {}): ReturnType<typeof render> {
  return render(IdentityImportSection, {
    props: { identity, ...props } as SectionProps,
  });
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('IdentityImportSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockHandleState.status = 'idle';
    mockHandleState.account = null;
    mockHandleState.error = null;
  });

  it('renders the section title', () => {
    renderSection();
    expect(
      screen.getByTestId('identity-import-section'),
    ).toBeInTheDocument();
    expect(
      screen.getByText('settings.import.sectionTitle'),
    ).toBeInTheDocument();
  });

  it('calls handle.load() on mount', () => {
    renderSection();
    expect(mockHandleState.load).toHaveBeenCalledOnce();
  });

  it('shows spinner while loading', () => {
    mockHandleState.status = 'loading';
    renderSection();
    const spinner = document.querySelector('[role="status"]');
    expect(spinner).toBeInTheDocument();
    expect(
      screen.queryByTestId('import-setup-btn'),
    ).not.toBeInTheDocument();
  });

  it('shows setup button when unconfigured (status=ready, account=null)', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    expect(screen.getByTestId('import-setup-btn')).toBeInTheDocument();
    expect(
      screen.queryByTestId('import-status-card'),
    ).not.toBeInTheDocument();
  });

  it('shows status card when an account is configured', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: '2026-06-01T12:00:00Z',
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    const card = screen.getByTestId('import-status-card');
    expect(card).toBeInTheDocument();
    const badge = screen.getByTestId('import-state-badge');
    expect(badge).toHaveTextContent('settings.import.status.enabled');
    expect(screen.queryByTestId('import-last-error')).not.toBeInTheDocument();
  });

  it('shows last error when account has an error', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: 'all',
      state: 'errored',
      lastSuccessAt: null,
      lastError: 'authentication failed',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    expect(screen.getByTestId('import-last-error')).toBeInTheDocument();
    const badge = screen.getByTestId('import-state-badge');
    expect(badge).toHaveTextContent('settings.import.status.errored');
  });

  it('opens the setup form when "Set up mail import" is clicked', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    const btn = screen.getByTestId('import-setup-btn');
    await fireEvent.click(btn);
    expect(screen.getByTestId('import-form')).toBeInTheDocument();
    expect(screen.getByTestId('import-field-host')).toBeInTheDocument();
    expect(screen.getByTestId('import-field-credential')).toBeInTheDocument();
    expect(screen.getByTestId('import-field-horizon')).toBeInTheDocument();
  });

  it('validates required fields before saving', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));
    // Clear username (pre-filled from identity.email) then save.
    const usernameField = screen.getByTestId('import-field-username') as HTMLInputElement;
    await fireEvent.input(usernameField, { target: { value: '' } });
    const hostField = screen.getByTestId('import-field-host') as HTMLInputElement;
    await fireEvent.input(hostField, { target: { value: '' } });
    await fireEvent.click(screen.getByTestId('import-save-btn'));
    expect(mockHandleState.create).not.toHaveBeenCalled();
  });

  it('shows the remove import dialog when remove button is clicked', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: null,
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-remove-btn'));
    expect(screen.getByTestId('import-remove-dialog')).toBeInTheDocument();
    // Both keep and delete options should be present.
    expect(
      screen.getByText('settings.import.removeKeepTitle'),
    ).toBeInTheDocument();
    expect(
      screen.getByText('settings.import.removeDeleteTitle'),
    ).toBeInTheDocument();
  });

  it('shows the provenance message count in the remove dialog', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import', // matches the mocked mailbox name
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: null,
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-remove-btn'));
    // The mocked mailbox has 42 messages; verify the count-bearing i18n key is
    // used rather than the "unknown count" fallback. The test i18n mock returns
    // the key string (not the translated string with the real number), so we
    // check for key presence instead of the literal count.
    const dialog = screen.getByTestId('import-remove-dialog');
    expect(dialog.textContent).toContain('settings.import.removeDeleteDesc');
    expect(dialog.textContent).not.toContain(
      'settings.import.removeDeleteDescUnknown',
    );
  });

  it('calls handle.destroy with deleteImportedMail=false when "keep" is confirmed', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: null,
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-remove-btn'));
    // 'keep' is the default; confirm without switching.
    await fireEvent.click(screen.getByTestId('import-remove-confirm'));
    expect(mockHandleState.destroy).toHaveBeenCalledWith('acc1', false);
  });

  it('calls handle.destroy with deleteImportedMail=true when "delete" is chosen', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: null,
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-remove-btn'));
    // Select the "delete" radio.
    const radios = screen.getAllByRole('radio') as HTMLInputElement[];
    const deleteRadio = radios.find((r) => r.value === 'delete');
    expect(deleteRadio).toBeDefined();
    await fireEvent.click(deleteRadio!);
    await fireEvent.click(screen.getByTestId('import-remove-confirm'));
    expect(mockHandleState.destroy).toHaveBeenCalledWith('acc1', true);
  });

  it('shows complete migration confirm dialog when migrate button is clicked', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: null,
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-migrate-btn'));
    expect(screen.getByTestId('import-migrate-dialog')).toBeInTheDocument();
    expect(
      screen.getByText('settings.import.migrateTitle'),
    ).toBeInTheDocument();
  });

  it('calls handle.update with state=migrating on migration confirm', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: '90d',
      state: 'enabled',
      lastSuccessAt: null,
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-migrate-btn'));
    await fireEvent.click(screen.getByTestId('import-migrate-confirm'));
    expect(mockHandleState.update).toHaveBeenCalledWith('acc1', {
      state: 'migrating',
    });
  });

  it('hides migrate button for migrated accounts', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = {
      id: 'acc1',
      identityId: 'id1',
      accountName: 'Gmail Import',
      host: 'imap.gmail.com',
      port: 993,
      tlsMode: 'implicit',
      username: 'test@external.example',
      authMethod: 'app_password',
      backfillHorizon: 'all',
      state: 'migrated',
      lastSuccessAt: '2026-06-01T12:00:00Z',
      lastError: '',
      deletePropagates: true,
      hasCredential: true,
    };
    renderSection();
    expect(screen.queryByTestId('import-migrate-btn')).not.toBeInTheDocument();
  });
});
