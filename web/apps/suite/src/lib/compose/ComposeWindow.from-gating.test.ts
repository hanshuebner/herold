/**
 * Send-button gating tests against the wired ComposeWindow (REQ-MAIL-12).
 *
 * Exercises the integration seam between FromPicker, compose.selectedIdentity,
 * and the Send button's disabled state. The pure helpers
 * (composeFromGating, composeFromSort) have their own unit tests; this
 * file verifies that picking an unverified identity in the picker
 * disables Send with the expected inline message.
 *
 * Note: the picker UI lives in FromPicker.svelte; its own tests live
 * next to it. This file focuses on the gating-driven Send-button state
 * and the from-gating-banner.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { Identity } from '../mail/types';

// ── Mocks (factories must inline literals; vi.mock is hoisted) ────────────

vi.mock('../auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
}));

vi.mock('../identities/identity-submission.svelte', () => {
  const handle = {
    status: 'idle',
    data: null,
    error: null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
  };
  return {
    submissionStore: {
      forIdentity: vi.fn(() => handle),
      evict: vi.fn(),
    },
  };
});

vi.mock('./compose.svelte', () => {
  // Inline mock object — vi.mock factory runs before any module-scope
  // bindings are initialised, so external state must live INSIDE the
  // factory closure.
  const compose = {
    isOpen: true,
    status: 'editing' as 'idle' | 'editing' | 'sending',
    to: '',
    cc: '',
    bcc: '',
    subject: '',
    body: '',
    errorMessage: null as string | null,
    ccBccVisible: false,
    replyContext: {
      parentId: null,
      parentKeyword: null,
      inReplyTo: null,
      references: null,
    },
    toRecipients: [],
    ccRecipients: [],
    bccRecipients: [],
    attachments: [],
    attachmentsBusy: false,
    editingDraftId: null,
    hasContent: false,
    selectedIdentity: null as Identity | null,
    persistDraft: vi.fn(),
    send: vi.fn(),
    discard: vi.fn(),
    close: vi.fn(),
    addAttachments: vi.fn(),
    addInlineImage: vi.fn(),
    removeAttachment: vi.fn(),
    flipToInline: vi.fn(),
    flipToAttachment: vi.fn(),
  };
  return {
    compose,
    bodyHasContent: vi.fn(() => false),
    bodyTextWithoutSignature: vi.fn(() => ''),
  };
});

vi.mock('./compose-stack.svelte', () => ({
  composeStack: { minimizeCurrent: vi.fn() },
}));

vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn(() => vi.fn()) },
}));

vi.mock('../dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'compose.from.sendDisabled.unverified':
        'Verify this identity before sending.',
      'compose.from.sendDisabled.verifying':
        'This identity is awaiting verification — cannot send yet.',
      'compose.from.sendDisabled.external':
        'External SMTP submission is not configured for this identity.',
      'compose.from.sendDisabled.noIdentity':
        'No identity available to send from.',
      'compose.from': 'From',
      'compose.from.picker.aria': 'From identity',
      'compose.from.picker.open': 'Change From identity',
      'compose.from.chip.verifying': 'Verification pending',
      'compose.from.chip.unverified': 'Unverified',
      'compose.from.chip.external': 'External SMTP missing',
      'compose.from.disabled.unverified': 'Verify this address.',
      'compose.from.disabled.verifying': 'Awaiting verification.',
      'compose.from.disabled.external': 'External SMTP missing.',
      'compose.title.new': 'New message',
      'compose.title.reply': 'Reply',
      'compose.title.forward': 'Forward',
      'compose.minimize': 'Minimize',
      'compose.close': 'Close',
      'compose.to': 'To',
      'compose.cc': 'Cc',
      'compose.bcc': 'Bcc',
      'compose.subject': 'Subject',
      'compose.body': 'Body',
      'compose.toggleCcBcc': 'Cc / Bcc',
      'compose.send': 'Send',
      'compose.sending': 'Sending...',
      'compose.discard': 'Discard',
      'compose.attach': 'Attach',
      'compose.attached': 'Attached',
      'compose.dropInline': 'Drop image here',
      'compose.dropAttach': 'Drop file here',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
  LOCALES: ['en'],
}));

vi.mock('../mail/store.svelte', () => {
  const DEFAULT_IDENTITY: Identity = {
    id: '1',
    name: 'Alice',
    email: 'alice@example.local',
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
    verifiedAt: '2026-01-01T00:00:00Z',
    isDefault: true,
  };
  const UNVERIFIED_IDENTITY: Identity = {
    id: '2',
    name: '',
    email: 'unverified@example.local',
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    verifiedAt: null,
  };
  const mail = {
    primaryIdentity: DEFAULT_IDENTITY as Identity | null,
    identities: new Map<string, Identity>([
      [DEFAULT_IDENTITY.id, DEFAULT_IDENTITY],
      [UNVERIFIED_IDENTITY.id, UNVERIFIED_IDENTITY],
    ]),
    mailboxes: new Map(),
    drafts: null,
    sent: null,
    mailAccountId: 'account-1',
    loadIdentities: vi.fn(),
  };
  return { mail };
});

// ── Test fixtures ────────────────────────────────────────────────────────

const DEFAULT_IDENTITY: Identity = {
  id: '1',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  verifiedAt: '2026-01-01T00:00:00Z',
  isDefault: true,
};

const UNVERIFIED_IDENTITY: Identity = {
  id: '2',
  name: '',
  email: 'unverified@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
  verifiedAt: null,
};

// Imports after mocks. The compose / mail singletons returned here are
// the mocked stubs; mutating them between tests is how we drive scenarios.
const { compose } = await import('./compose.svelte');
const { mail } = await import('../mail/store.svelte');
const composeMock = compose as unknown as {
  isOpen: boolean;
  status: 'idle' | 'editing' | 'sending';
  errorMessage: string | null;
  selectedIdentity: Identity | null;
};
const mailMock = mail as unknown as {
  primaryIdentity: Identity | null;
  identities: Map<string, Identity>;
};

import ComposeWindow from './ComposeWindow.svelte';

// ── Tests ────────────────────────────────────────────────────────────────

describe('ComposeWindow send-button gating (REQ-MAIL-12)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    composeMock.isOpen = true;
    composeMock.status = 'editing';
    composeMock.errorMessage = null;
    composeMock.selectedIdentity = null;
    mailMock.primaryIdentity = DEFAULT_IDENTITY;
    mailMock.identities = new Map<string, Identity>([
      [DEFAULT_IDENTITY.id, DEFAULT_IDENTITY],
      [UNVERIFIED_IDENTITY.id, UNVERIFIED_IDENTITY],
    ]);
  });

  it('enables Send when the selected From identity is verified', async () => {
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    const { container } = render(ComposeWindow);
    await tick();
    const send = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-send"]',
    );
    expect(send?.disabled).toBe(false);
    expect(
      container.querySelector('[data-testid="from-gating-banner"]'),
    ).toBeNull();
  });

  it('disables Send when the selected From identity is unverified', async () => {
    composeMock.selectedIdentity = UNVERIFIED_IDENTITY;
    const { container } = render(ComposeWindow);
    await tick();
    const send = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-send"]',
    );
    expect(send?.disabled).toBe(true);
    expect(send?.title).toMatch(/Verify this identity/);
    const banner = container.querySelector(
      '[data-testid="from-gating-banner"]',
    );
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toMatch(/Verify this identity/);
  });

  it('disables Send with the verifying message when token is live but not confirmed', async () => {
    const VERIFYING: Identity = {
      id: '3',
      name: '',
      email: 'pending@example.local',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: true,
      verifiedAt: null,
      verificationPendingSince: '2026-05-09T00:00:00Z',
    };
    mailMock.identities = new Map<string, Identity>([
      [DEFAULT_IDENTITY.id, DEFAULT_IDENTITY],
      [VERIFYING.id, VERIFYING],
    ]);
    composeMock.selectedIdentity = VERIFYING;
    const { container } = render(ComposeWindow);
    await tick();
    const send = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-send"]',
    );
    expect(send?.disabled).toBe(true);
    const banner = container.querySelector(
      '[data-testid="from-gating-banner"]',
    );
    expect(banner?.textContent).toMatch(/awaiting verification/);
  });

  it('falls back to primaryIdentity when compose.selectedIdentity is null', async () => {
    composeMock.selectedIdentity = null;
    const { container } = render(ComposeWindow);
    await tick();
    const send = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-send"]',
    );
    expect(send?.disabled).toBe(false);
  });

  it('renders the From picker when there are >= 2 identities', async () => {
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    const { container } = render(ComposeWindow);
    await tick();
    const trigger = container.querySelector(
      '[data-testid="from-picker-trigger"]',
    );
    expect(trigger).not.toBeNull();
    expect(container.querySelector('[data-testid="from-static"]')).toBeNull();
  });

  it('renders the static From text when only one identity exists', async () => {
    mailMock.identities = new Map<string, Identity>([
      [DEFAULT_IDENTITY.id, DEFAULT_IDENTITY],
    ]);
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    const { container } = render(ComposeWindow);
    await tick();
    expect(
      container.querySelector('[data-testid="from-picker-trigger"]'),
    ).toBeNull();
    const staticEl = container.querySelector('[data-testid="from-static"]');
    expect(staticEl).not.toBeNull();
    expect(staticEl?.textContent).toMatch(/alice@example\.local/);
  });
});
