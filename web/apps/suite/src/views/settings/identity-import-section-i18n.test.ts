/**
 * IdentityImportSection — translated string value assertions (re #132).
 *
 * The main test file (identity-import-section.test.ts) uses a key-passthrough
 * i18n mock, so its assertions never touch the actual catalogue values. This
 * file re-renders the component with a mock that returns REAL translated
 * strings (from en.ts) and asserts the values touched by the #132 fix:
 *
 *   settings.import.neverSynced    → "First sync pending"
 *   settings.import.syncInProgress → "Import in progress…"
 *   settings.import.migrateBtn     → "Start full migration"
 *   settings.import.migrateTitle   → "Start full migration from upstream"
 *
 * Sync-status display rules (re #132 follow-up):
 *   lastSuccessAt set               → show last sync date
 *   lastSuccessAt null, count  > 0  → show "Import in progress…"
 *   lastSuccessAt null, count null  → show "First sync pending"
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';

// ── Import real translations ──────────────────────────────────────────────────

import { en } from '../../lib/i18n/en';

// ── Mock dependencies with real i18n ──────────────────────────────────────────

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

/**
 * Mock i18n using the real English catalogue values (not key passthrough).
 * This makes assertions sensitive to the actual translated strings.
 */
vi.mock('../../lib/i18n/i18n.svelte', () => {
  // Deferred to avoid circular import at mock-hoisting time.
  return {
    t: (key: string, params?: Record<string, string>): string => {
      const raw = (en as Record<string, string>)[key] ?? key;
      if (!params) return raw;
      return Object.entries(params).reduce(
        (s, [k, v]) => s.replace(`{${k}}`, v),
        raw,
      );
    },
  };
});

// ── Test identity ─────────────────────────────────────────────────────────────

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

// ── Account fixtures ──────────────────────────────────────────────────────────

/**
 * Account whose name matches the mocked mailbox ("Gmail Import", 42 messages).
 * lastSuccessAt is null but importedCount > 0 — expects "Import in progress…".
 */
const enabledAccountNeverSynced = {
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

/**
 * Account whose name does NOT match any mocked mailbox — importedCount is null.
 * lastSuccessAt is null and importedCount is null — expects "First sync pending".
 */
const enabledAccountNoMessages = {
  ...enabledAccountNeverSynced,
  accountName: 'Other Account',  // no matching mailbox in the mock
};

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('IdentityImportSection — translated string values (re #132)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockHandleState.status = 'idle';
    mockHandleState.account = null;
    mockHandleState.error = null;
  });

  it('renders "Import in progress…" when lastSuccessAt is null but messages are already imported', () => {
    // enabledAccountNeverSynced.accountName matches the mocked mailbox "Gmail Import"
    // with totalEmails=42, so importedCount > 0. The component must show the
    // in-progress label, not the "pending" label.
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccountNeverSynced;
    renderSection();
    expect(screen.getByText('Import in progress…')).toBeInTheDocument();
    expect(screen.queryByText('First sync pending')).not.toBeInTheDocument();
  });

  it('renders "First sync pending" when lastSuccessAt is null and no messages have been imported', () => {
    // enabledAccountNoMessages.accountName does not match any mocked mailbox,
    // so importedCount is null. The component must show the "pending" label.
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccountNoMessages;
    renderSection();
    expect(screen.getByText('First sync pending')).toBeInTheDocument();
    expect(screen.queryByText('Import in progress…')).not.toBeInTheDocument();
  });

  it('renders "Start full migration" on the migrate card button', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccountNeverSynced;
    renderSection();
    const btn = screen.getByTestId('import-migrate-btn');
    expect(btn).toHaveTextContent('Start full migration');
  });

  it('renders "Start full migration from upstream" as the dialog title', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccountNeverSynced;
    renderSection();
    await fireEvent.click(screen.getByTestId('import-migrate-btn'));
    const dialog = screen.getByTestId('import-migrate-dialog');
    expect(dialog).toBeInTheDocument();
    expect(
      screen.getByText('Start full migration from upstream'),
    ).toBeInTheDocument();
  });
});
