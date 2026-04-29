/**
 * SettingsView.svelte — external-submission capability hint tests.
 *
 * REQ-MAIL-SUBMIT-01: when the server does not advertise the external-
 * submission capability, an informational hint appears in the
 * "Identities & signatures" subsection so the feature is discoverable.
 *
 * Branch (a): capability present — badge/link visible, hint absent.
 * Branch (b): capability absent  — hint visible, badge/link absent.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// ── Capabilities mock (the single knob for these tests) ───────────────────

vi.mock('../lib/auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
}));

// ── Singleton store mocks ─────────────────────────────────────────────────

vi.mock('../lib/settings/settings.svelte', () => ({
  settings: {
    theme: 'system',
    locale: 'en',
    undoWindowSec: 5,
    swipeLeft: 'archive',
    swipeRight: 'archive',
    coachEnabled: true,
    imageLoadDefault: 'never',
    imageAllowList: [],
    hydrate: vi.fn(),
    setTheme: vi.fn(),
    setLocale: vi.fn(),
    setUndoWindowSec: vi.fn(),
    setSwipeLeft: vi.fn(),
    setSwipeRight: vi.fn(),
    setCoachEnabled: vi.fn(),
    setImageLoadDefault: vi.fn(),
    removeImageAllowedSender: vi.fn(),
  },
}));

vi.mock('../lib/auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    principalId: '1',
    session: {
      username: 'alice@example.com',
      capabilities: {},
      primaryAccounts: {},
      apiUrl: 'https://example.com/jmap',
      eventSourceUrl: 'https://example.com/jmap/eventsource',
      state: 'session-state-1',
    },
  },
}));

vi.mock('../lib/mail/store.svelte', () => ({
  mail: {
    identities: new Map([
      [
        'ident-1',
        {
          id: 'ident-1',
          name: 'Alice',
          email: 'alice@example.com',
          replyTo: null,
          bcc: null,
          textSignature: '',
          htmlSignature: '',
          mayDelete: false,
        },
      ],
    ]),
    mailboxes: new Map(),
    loadIdentities: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    parts: ['settings', 'account'],
    navigate: vi.fn(),
    getParam: vi.fn().mockReturnValue(null),
    setParam: vi.fn(),
  },
}));

vi.mock('../lib/jmap/client', () => ({
  jmap: {
    hasCapability: vi.fn(() => false),
    session: null,
  },
  strict: vi.fn(() => ({ request: vi.fn() })),
}));

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  LOCALES: ['en', 'de'],
  localeTag: () => 'en',
}));

vi.mock('../lib/llm/transparency.svelte', () => ({
  llmTransparency: {
    loadStatus: 'idle',
    data: null,
    loadError: null,
    load: vi.fn(),
  },
}));

vi.mock('../lib/push/push-subscription.svelte', () => ({
  pushSubscription: {
    permissionState: 'default',
    subscribed: false,
    busy: false,
    errorMessage: null,
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroyAll: vi.fn(),
    forgetDenial: vi.fn(),
  },
}));

vi.mock('../lib/notifications/sounds.svelte', () => ({
  sounds: {
    enabled: false,
    hydrate: vi.fn(),
    setEnabled: vi.fn(),
  },
}));

vi.mock('../lib/identities/identity-submission.svelte', () => ({
  submissionStore: {
    forIdentity: vi.fn(() => ({
      status: 'ready',
      data: { configured: false },
      error: null,
      load: vi.fn().mockResolvedValue(undefined),
      refresh: vi.fn().mockResolvedValue(undefined),
    })),
    evict: vi.fn(),
  },
}));

vi.mock('../lib/toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

vi.mock('../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('../lib/api/client', () => ({
  get: vi.fn().mockResolvedValue({}),
  post: vi.fn().mockResolvedValue({}),
  put: vi.fn().mockResolvedValue({}),
  patch: vi.fn().mockResolvedValue({}),
  del: vi.fn().mockResolvedValue({}),
  ApiError: class ApiError extends Error {
    status: number;
    body: unknown;
    constructor(status: number, message: string, body?: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.body = body;
    }
  },
  UnauthenticatedError: class UnauthenticatedError extends Error {
    constructor() {
      super('Unauthenticated');
      this.name = 'UnauthenticatedError';
    }
  },
}));

vi.mock('../lib/settings/category-settings.svelte', () => ({
  categorySettings: {
    available: false,
    derivedCategories: [],
    loadStatus: 'idle',
    load: vi.fn().mockResolvedValue(undefined),
  },
  emailMatchesTab: vi.fn().mockReturnValue(true),
  categoryKeyword: vi.fn().mockReturnValue(null),
}));

vi.mock('../lib/settings/managed-rules.svelte', () => ({
  managedRules: {
    status: 'idle',
    rules: [],
    load: vi.fn().mockResolvedValue(undefined),
    create: vi.fn().mockResolvedValue(undefined),
    update: vi.fn().mockResolvedValue(undefined),
    destroy: vi.fn().mockResolvedValue(undefined),
  },
  hasDeleteApplyLabelConflict: vi.fn().mockReturnValue(false),
  isThreadMuteRule: vi.fn().mockReturnValue(false),
  isBlockedSenderRule: vi.fn().mockReturnValue(false),
  blockedSenderAddress: vi.fn().mockReturnValue(null),
  buildEmailQueryFilter: vi.fn().mockReturnValue({}),
}));

vi.mock('../lib/settings/filter-like.svelte', () => ({
  filterLike: {
    senders: [],
    addSender: vi.fn(),
    removeSender: vi.fn(),
  },
}));

vi.mock('../lib/contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'ready',
    entries: [],
    clear: vi.fn(),
    load: vi.fn(),
    destroy: vi.fn(),
  },
}));

vi.mock('../lib/jmap/sync.svelte', () => ({
  sync: {
    status: 'idle',
    start: vi.fn(),
    stop: vi.fn(),
  },
}));

vi.mock('qrcode-svg', () => ({
  default: vi.fn(() => ({ svg: () => '<svg></svg>' })),
}));

// ── Resolve mock handles ───────────────────────────────────────────────────

const { hasExternalSubmission } = await import('../lib/auth/capabilities');

// ── Component under test ───────────────────────────────────────────────────

import SettingsView from './SettingsView.svelte';

// ── Tests ──────────────────────────────────────────────────────────────────

describe('SettingsView external-submission hint', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('(b) when capability is absent: hint text is present', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(false);

    render(SettingsView);

    expect(
      screen.getByText(/External SMTP submission.*is not enabled on this server/),
    ).toBeInTheDocument();
  });

  it('(b) when capability is absent: system.toml reference is present', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(false);

    render(SettingsView);

    expect(screen.getByText('system.toml')).toBeInTheDocument();
    expect(screen.getByText('docs/operator/external-smtp-submission.md')).toBeInTheDocument();
  });

  it('(b) when capability is absent: Configure external SMTP link is absent', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(false);

    render(SettingsView);

    expect(screen.queryByText('Configure external SMTP')).not.toBeInTheDocument();
  });

  it('(a) when capability is present: Configure external SMTP link is visible', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(true);

    render(SettingsView);

    expect(screen.getByText('Configure external SMTP')).toBeInTheDocument();
  });

  it('(a) when capability is present: hint text is absent', () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(true);

    render(SettingsView);

    expect(
      screen.queryByText(/External SMTP submission.*is not enabled on this server/),
    ).not.toBeInTheDocument();
  });
});
