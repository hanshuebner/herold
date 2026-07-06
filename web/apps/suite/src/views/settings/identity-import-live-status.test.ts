/**
 * IdentityImportSection — live status panel tests (re #138).
 *
 * Tests the live worker status sub-panel rendered inside the status card
 * when the user has a configured IMAP import account. The panel reads
 * from the imapImportLiveStatus singleton which wraps
 * GET /api/v1/me/imap-imports/status.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';

// ── Hoisted mock state (must be hoisted before vi.mock is called) ─────────────

type MockItem = {
  account_id: string;
  account_name: string;
  connected: boolean;
  phase: string;
  watch_mode?: string;
  last_sync_at?: string;
  messages_fetched: number;
  last_error?: string;
};

// vi.hoisted runs before any imports and module initialisation, making the
// returned reference available inside vi.mock factory closures without TDZ.
const mockLiveStatus = vi.hoisted(() => {
  const state = {
    status: 'idle' as 'idle' | 'loading' | 'ready' | 'error',
    items: [] as MockItem[],
    error: null as string | null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
    // forAccount delegates to state.items so mutations in beforeEach are visible
    forAccount: vi.fn((accountId: string) =>
      state.items.find((i) => i.account_id === accountId) ?? null,
    ),
  };
  return state;
});

vi.mock('../../lib/api/imap-import-live-status.svelte', () => ({
  imapImportLiveStatus: mockLiveStatus,
}));

// ── JMAP import store mock ────────────────────────────────────────────────────

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

// ── Real translations ─────────────────────────────────────────────────────────

import { en } from '../../lib/i18n/en';

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string>): string => {
    const raw = (en as Record<string, string>)[key] ?? key;
    if (!params) return raw;
    return Object.entries(params).reduce(
      (s, [k, v]) => s.replace(`{${k}}`, v),
      raw,
    );
  },
}));

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

// ── Account fixture ───────────────────────────────────────────────────────────

const enabledAccount = {
  id: 'acc1',
  identityId: 'id1',
  accountName: 'Gmail Import',
  host: 'imap.gmail.com',
  port: 993,
  tlsMode: 'implicit',
  username: 'test@external.example',
  authMethod: 'app_password',
  backfillHorizon: '2026-01-01',
  state: 'enabled',
  lastSuccessAt: '2026-07-01T12:00:00Z',
  lastError: '',
  deletePropagates: true,
  hasCredential: true,
};

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('IdentityImportSection — live status panel (re #138)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockHandleState.status = 'idle';
    mockHandleState.account = null;
    mockHandleState.error = null;
    mockLiveStatus.status = 'idle';
    mockLiveStatus.items = [];
    mockLiveStatus.error = null;
    // Restore forAccount to use the current items array after mock clear.
    mockLiveStatus.forAccount.mockImplementation((accountId: string) =>
      mockLiveStatus.items.find((i) => i.account_id === accountId) ?? null,
    );
  });

  it('renders live-status panel when account is configured', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: true,
        phase: 'idle',
        watch_mode: 'idle',
        messages_fetched: 42,
      },
    ];
    renderSection();
    expect(screen.getByTestId('import-live-status')).toBeInTheDocument();
  });

  it('shows "Connected" badge when worker is connected', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: true,
        phase: 'idle',
        messages_fetched: 0,
      },
    ];
    renderSection();
    expect(screen.getByTestId('live-status-connected')).toHaveTextContent('Connected');
  });

  it('shows "Disconnected" badge when worker is not connected', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: false,
        phase: 'backoff',
        messages_fetched: 0,
        last_error: 'connection refused',
      },
    ];
    renderSection();
    expect(screen.getByTestId('live-status-connected')).toHaveTextContent('Disconnected');
  });

  it('shows phase label', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: false,
        phase: 'connecting',
        messages_fetched: 0,
      },
    ];
    renderSection();
    expect(screen.getByTestId('live-status-phase')).toHaveTextContent('Phase: connecting');
  });

  it('shows watch mode when present', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: true,
        phase: 'idle',
        watch_mode: 'notify',
        messages_fetched: 17,
      },
    ];
    renderSection();
    expect(screen.getByTestId('live-status-watch-mode')).toHaveTextContent('Watch mode: notify');
  });

  it('shows messages fetched count', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: true,
        phase: 'idle',
        messages_fetched: 123,
      },
    ];
    renderSection();
    expect(screen.getByTestId('live-status-messages-fetched')).toHaveTextContent(
      '123 messages fetched this session',
    );
  });

  it('shows last error when present', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: false,
        phase: 'errored',
        messages_fetched: 0,
        last_error: 'authentication failed',
      },
    ];
    renderSection();
    expect(screen.getByTestId('live-status-last-error')).toHaveTextContent(
      'Error: authentication failed',
    );
  });

  it('shows no-worker message when status is ready but no matching worker', () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [];
    renderSection();
    expect(screen.getByTestId('live-status-no-worker')).toBeInTheDocument();
  });

  it('calls refresh on refresh button click', async () => {
    mockHandleState.status = 'ready';
    mockHandleState.account = enabledAccount;
    mockLiveStatus.status = 'ready';
    mockLiveStatus.items = [
      {
        account_id: 'acc1',
        account_name: 'Gmail Import',
        connected: true,
        phase: 'idle',
        messages_fetched: 0,
      },
    ];
    renderSection();
    const refreshBtn = screen.getByRole('button', { name: 'Refresh' });
    await fireEvent.click(refreshBtn);
    expect(mockLiveStatus.refresh).toHaveBeenCalledOnce();
  });
});
