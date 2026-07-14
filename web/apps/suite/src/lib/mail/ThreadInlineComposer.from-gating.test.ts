/**
 * Send-button gating tests for ThreadInlineComposer (re #147, REQ-MAIL-12).
 *
 * Mirrors ComposeWindow.from-gating.test.ts: exercises the FromPicker
 * rendering seam and the send-button disabled state driven by
 * composeFromGating. The inline composer mounts only when
 * compose.isOpen && compose.inlineMode, so both are set to true.
 *
 * Testability seam: ThreadInlineComposer.svelte exposes
 * data-testid="from-gating-banner" on the gating-message paragraph,
 * parallel to ComposeWindow's own attribute.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { Identity } from './types';

// ── Mocks (factories must inline literals; vi.mock is hoisted) ────────────────

vi.mock('../auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
  hasDirectoryAutocomplete: vi.fn(() => false),
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

vi.mock('../compose/compose.svelte', () => {
  const compose = {
    isOpen: true,
    inlineMode: true,
    status: 'editing' as 'idle' | 'editing' | 'sending',
    to: '',
    cc: '',
    bcc: '',
    body: '',
    errorMessage: null as string | null,
    ccBccVisible: false,
    replyContext: {
      parentId: 'msg-1',
      parentKeyword: null,
      inReplyTo: null,
      references: null,
    },
    toRecipients: [],
    ccRecipients: [],
    bccRecipients: [],
    attachments: [],
    shares: [],
    attachmentsBusy: false,
    editingDraftId: null,
    hasContent: false,
    selectedIdentity: null as Identity | null,
    send: vi.fn(),
    discard: vi.fn(),
    addAttachments: vi.fn(),
    removeAttachment: vi.fn(),
    removeShare: vi.fn(),
    setShareEditorFns: vi.fn(),
    setSwapInlineImageSrcFn: vi.fn(),
  };
  return { compose };
});

vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn(() => vi.fn()) },
}));

vi.mock('../dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('./navigate-back', () => ({
  navigateBackFromThread: vi.fn(),
}));

vi.mock('./attachment-icon', () => ({
  attachmentBadge: vi.fn(() => ({ bg: '#888', label: 'FILE' })),
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'compose.from': 'From',
      'compose.from.picker.aria': 'From identity',
      'compose.from.picker.open': 'Change From identity',
      'compose.from.chip.verifying': 'Verification pending',
      'compose.from.chip.unverified': 'Unverified',
      'compose.from.chip.external': 'External SMTP missing',
      'compose.from.disabled.unverified': 'Verify this address.',
      'compose.from.disabled.verifying': 'Awaiting verification.',
      'compose.from.disabled.external': 'External SMTP missing.',
      'compose.from.sendDisabled.unverified': 'Verify this identity before sending.',
      'compose.from.sendDisabled.verifying':
        'This identity is awaiting verification — cannot send yet.',
      'compose.from.sendDisabled.external':
        'External SMTP submission is not configured for this identity.',
      'compose.from.sendDisabled.noIdentity': 'No identity available to send from.',
      'compose.from.sendDisabled.broken': 'External SMTP for this identity is misconfigured.',
      'compose.to': 'To',
      'compose.cc': 'Cc',
      'compose.bcc': 'Bcc',
      'compose.toggleCcBcc': 'Cc / Bcc',
      'compose.send': 'Send',
      'compose.sending': 'Sending...',
      'compose.sendAndArchive': 'Send + Archive',
      'compose.attach': 'Attach',
      'compose.discard': 'Discard',
      'compose.close': 'Close',
      'compose.popOut': 'Pop out',
      'compose.discardConfirm.title': 'Discard draft?',
      'compose.discardConfirm.message': 'This draft will be lost.',
      'compose.discardConfirm.confirm': 'Discard',
      'compose.discardConfirm.cancel': 'Cancel',
      'compose.title.reply': 'Reply',
      'compose.title.forward': 'Forward',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
  LOCALES: ['en'],
}));

vi.mock('./store.svelte', () => {
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
    inbox: null as { id: string } | null,
    threadEmails: vi.fn((_tid: string) => []),
    bulkArchive: vi.fn(),
    loadIdentities: vi.fn(),
  };
  return { mail };
});

// ── Test fixtures ─────────────────────────────────────────────────────────────

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

// Dynamic imports AFTER mocks so we get the mocked singletons.
const { compose } = await import('../compose/compose.svelte');
const { mail } = await import('./store.svelte');
const composeMock = compose as unknown as {
  isOpen: boolean;
  inlineMode: boolean;
  status: 'idle' | 'editing' | 'sending';
  selectedIdentity: Identity | null;
};
const mailMock = mail as unknown as {
  primaryIdentity: Identity | null;
  identities: Map<string, Identity>;
};

import ThreadInlineComposer from './ThreadInlineComposer.svelte';

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('ThreadInlineComposer send-button gating (re #147, REQ-MAIL-12)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    composeMock.isOpen = true;
    composeMock.inlineMode = true;
    composeMock.status = 'editing';
    composeMock.selectedIdentity = null;
    mailMock.primaryIdentity = DEFAULT_IDENTITY;
    mailMock.identities = new Map<string, Identity>([
      [DEFAULT_IDENTITY.id, DEFAULT_IDENTITY],
      [UNVERIFIED_IDENTITY.id, UNVERIFIED_IDENTITY],
    ]);
  });

  it('renders the FromPicker (from-picker-trigger) in the inline composer when identities are available', async () => {
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    const { container } = render(ThreadInlineComposer, {
      props: { threadId: 'tid-1' },
    });
    await tick();
    const trigger = container.querySelector('[data-testid="from-picker-trigger"]');
    expect(trigger).not.toBeNull();
  });

  it('enables send buttons when the selected From identity is verified', async () => {
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    const { container } = render(ThreadInlineComposer, {
      props: { threadId: 'tid-1' },
    });
    await tick();
    const sendBtn = container.querySelector<HTMLButtonElement>('[data-testid="inline-send"]');
    const sendArchiveBtn = container.querySelector<HTMLButtonElement>(
      '[data-testid="inline-send-archive"]',
    );
    expect(sendBtn?.disabled).toBe(false);
    expect(sendArchiveBtn?.disabled).toBe(false);
    expect(container.querySelector('[data-testid="from-gating-banner"]')).toBeNull();
  });

  it('disables send buttons when the selected From identity is unverified', async () => {
    composeMock.selectedIdentity = UNVERIFIED_IDENTITY;
    const { container } = render(ThreadInlineComposer, {
      props: { threadId: 'tid-1' },
    });
    await tick();
    const sendBtn = container.querySelector<HTMLButtonElement>('[data-testid="inline-send"]');
    const sendArchiveBtn = container.querySelector<HTMLButtonElement>(
      '[data-testid="inline-send-archive"]',
    );
    expect(sendBtn?.disabled).toBe(true);
    expect(sendArchiveBtn?.disabled).toBe(true);
    const banner = container.querySelector('[data-testid="from-gating-banner"]');
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toMatch(/Verify this identity/);
  });

  it('falls back to primaryIdentity when compose.selectedIdentity is null', async () => {
    composeMock.selectedIdentity = null;
    const { container } = render(ThreadInlineComposer, {
      props: { threadId: 'tid-1' },
    });
    await tick();
    const sendBtn = container.querySelector<HTMLButtonElement>('[data-testid="inline-send"]');
    expect(sendBtn?.disabled).toBe(false);
  });

  it('renders the FromPicker even with a single identity (REQ-MAIL-12)', async () => {
    mailMock.identities = new Map<string, Identity>([
      [DEFAULT_IDENTITY.id, DEFAULT_IDENTITY],
    ]);
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    const { container } = render(ThreadInlineComposer, {
      props: { threadId: 'tid-1' },
    });
    await tick();
    const trigger = container.querySelector('[data-testid="from-picker-trigger"]');
    expect(trigger).not.toBeNull();
  });
});
