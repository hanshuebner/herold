/**
 * IdentityImportSection.svelte — excluded folders editor tests (re #305).
 *
 * Covers:
 *   - Setup form: adding a folder creates a chip; empty input is rejected;
 *     duplicate entries are not added twice; chips can be removed.
 *   - Edit form: existing account.excludedFolders pre-populates the chips.
 *   - Save: excludedFolders is forwarded to handle.create / handle.update.
 *   - Status card: shows an excluded-folders summary line when configured.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';

// ── Mock dependencies ────────────────────────────────────────────────────

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
    mailboxes: new Map(),
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

const configuredAccount = {
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
  excludedFolders: ['Spam', 'Promo'],
};

describe('IdentityImportSection excluded folders editor (re #305)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockHandleState.status = 'idle';
    mockHandleState.account = null;
    mockHandleState.error = null;
  });

  it('adds a chip when a folder name is entered and Add is clicked', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));

    const input = screen.getByTestId('import-excluded-folder-input') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'Spam' } });
    await fireEvent.click(screen.getByTestId('import-excluded-folder-add-btn'));

    const chips = screen.getAllByTestId('excluded-folder-chip');
    expect(chips).toHaveLength(1);
    expect(chips[0]).toHaveTextContent('Spam');
    expect(input.value).toBe('');
  });

  it('adds a chip on Enter keydown', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));

    const input = screen.getByTestId('import-excluded-folder-input') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'Trash' } });
    await fireEvent.keyDown(input, { key: 'Enter' });

    expect(screen.getAllByTestId('excluded-folder-chip')).toHaveLength(1);
    expect(screen.getByTestId('excluded-folder-chip')).toHaveTextContent('Trash');
  });

  it('rejects an empty entry with a validation error and adds nothing', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));

    await fireEvent.click(screen.getByTestId('import-excluded-folder-add-btn'));

    expect(screen.queryByTestId('excluded-folder-chip')).not.toBeInTheDocument();
    expect(
      screen.getByText('settings.import.excludedFolderEmpty'),
    ).toBeInTheDocument();
  });

  it('does not add a duplicate folder name twice', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));

    const input = screen.getByTestId('import-excluded-folder-input') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'Spam' } });
    await fireEvent.click(screen.getByTestId('import-excluded-folder-add-btn'));
    await fireEvent.input(input, { target: { value: 'Spam' } });
    await fireEvent.click(screen.getByTestId('import-excluded-folder-add-btn'));

    expect(screen.getAllByTestId('excluded-folder-chip')).toHaveLength(1);
  });

  it('removes a chip when its remove button is clicked', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));

    const input = screen.getByTestId('import-excluded-folder-input') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'Spam' } });
    await fireEvent.click(screen.getByTestId('import-excluded-folder-add-btn'));
    expect(screen.getAllByTestId('excluded-folder-chip')).toHaveLength(1);

    await fireEvent.click(screen.getByTestId('excluded-folder-remove'));
    expect(screen.queryByTestId('excluded-folder-chip')).not.toBeInTheDocument();
  });

  it('pre-populates chips from account.excludedFolders when editing', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = { ...configuredAccount };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-edit-btn'));

    const chips = screen.getAllByTestId('excluded-folder-chip');
    expect(chips.map((c) => c.textContent?.replace('x', '').trim())).toEqual([
      'Spam',
      'Promo',
    ]);
  });

  it('forwards excludedFolders to handle.create on save', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = null;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-setup-btn'));

    await fireEvent.input(screen.getByTestId('import-field-host'), {
      target: { value: 'imap.example.com' },
    });
    await fireEvent.input(screen.getByTestId('import-field-credential'), {
      target: { value: 'secret' },
    });
    const folderInput = screen.getByTestId('import-excluded-folder-input');
    await fireEvent.input(folderInput, { target: { value: 'Spam' } });
    await fireEvent.click(screen.getByTestId('import-excluded-folder-add-btn'));

    await fireEvent.click(screen.getByTestId('import-save-btn'));

    expect(mockHandleState.create).toHaveBeenCalledWith(
      expect.objectContaining({ excludedFolders: ['Spam'] }),
    );
  });

  it('forwards excludedFolders to handle.update on save, including removals', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = { ...configuredAccount };
    renderSection();
    await fireEvent.click(screen.getByTestId('import-edit-btn'));

    // Remove "Spam", leaving only "Promo".
    const removeButtons = screen.getAllByTestId('excluded-folder-remove');
    await fireEvent.click(removeButtons[0]!);

    await fireEvent.click(screen.getByTestId('import-save-btn'));

    expect(mockHandleState.update).toHaveBeenCalledWith(
      'acc1',
      expect.objectContaining({ excludedFolders: ['Promo'] }),
    );
  });

  it('shows an excluded-folders summary line on the status card', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = { ...configuredAccount };
    renderSection();

    const summary = screen.getByTestId('import-excluded-folders-summary');
    expect(summary.textContent).toContain('settings.import.excludedFoldersSummary');
  });

  it('omits the summary line when excludedFolders is absent', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = { ...configuredAccount, excludedFolders: [] };
    renderSection();

    expect(
      screen.queryByTestId('import-excluded-folders-summary'),
    ).not.toBeInTheDocument();
  });
});
