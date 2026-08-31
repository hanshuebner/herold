/**
 * Full-screen toggle tests for ComposeWindow (re #291).
 *
 * Verifies the header control cluster: the full-screen toggle flips
 * compose.fullScreen and swaps its icon/label, the modal picks up the
 * `full-screen` class while toggled on, and the minimize button keeps
 * calling composeStack.minimizeCurrent() regardless of full-screen state.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
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
  const compose = {
    isOpen: true,
    inlineMode: false,
    fullScreen: false,
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
    shares: [],
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
    removeShare: vi.fn(),
    flipToInline: vi.fn(),
    flipToAttachment: vi.fn(),
    setOffloadOfferFn: vi.fn(),
    setShareEditorFns: vi.fn(),
    setSwapInlineImageSrcFn: vi.fn(),
  };
  return {
    compose,
    bodyHasContent: vi.fn(() => false),
    bodyTextWithoutSignature: vi.fn(() => ''),
  };
});

vi.mock('../jmap/file-shares', () => ({
  hasFileShares: vi.fn(() => false),
  offloadThresholdBytes: vi.fn(() => 25 * 1024 * 1024),
  defaultTtlSeconds: vi.fn(() => 30 * 24 * 3600),
  maxTtlSeconds: vi.fn(() => 90 * 24 * 3600),
}));

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
      'compose.from': 'From',
      'compose.title.new': 'New message',
      'compose.title.reply': 'Reply',
      'compose.title.forward': 'Forward',
      'compose.minimize': 'Minimize',
      'compose.fullScreen': 'Full screen',
      'compose.exitFullScreen': 'Exit full screen',
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
  const mail = {
    primaryIdentity: DEFAULT_IDENTITY as Identity | null,
    identities: new Map<string, Identity>([[DEFAULT_IDENTITY.id, DEFAULT_IDENTITY]]),
    mailboxes: new Map(),
    drafts: null,
    sent: null,
    mailAccountId: 'account-1',
    loadIdentities: vi.fn(),
  };
  return { mail };
});

const { compose } = await import('./compose.svelte');
const { composeStack } = await import('./compose-stack.svelte');
const minimizeCurrent = composeStack.minimizeCurrent as ReturnType<typeof vi.fn>;
const composeMock = compose as unknown as {
  isOpen: boolean;
  inlineMode: boolean;
  fullScreen: boolean;
  status: 'idle' | 'editing' | 'sending';
  selectedIdentity: Identity | null;
};

import ComposeWindow from './ComposeWindow.svelte';

describe('ComposeWindow full-screen toggle (re #291)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    composeMock.isOpen = true;
    composeMock.inlineMode = false;
    composeMock.fullScreen = false;
    composeMock.status = 'editing';
    composeMock.selectedIdentity = null;
  });

  it('starts as a pop-up: no full-screen class, expand icon shown', async () => {
    const { container } = render(ComposeWindow);
    await tick();
    const modal = container.querySelector('.modal');
    expect(modal?.classList.contains('full-screen')).toBe(false);
    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-fullscreen-toggle"]',
    );
    expect(toggle?.getAttribute('aria-label')).toBe('Full screen');
  });

  it('renders the modal with the full-screen class and restore icon/label when fullScreen is set', async () => {
    composeMock.fullScreen = true;
    const { container } = render(ComposeWindow);
    await tick();
    const modal = container.querySelector('.modal');
    expect(modal?.classList.contains('full-screen')).toBe(true);
    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-fullscreen-toggle"]',
    );
    expect(toggle?.getAttribute('aria-label')).toBe('Exit full screen');
  });

  it('clicking the toggle flips compose.fullScreen', async () => {
    const { container } = render(ComposeWindow);
    await tick();
    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-fullscreen-toggle"]',
    );
    expect(toggle).not.toBeNull();
    await fireEvent.click(toggle!);
    expect(composeMock.fullScreen).toBe(true);
  });

  it('clicking the toggle a second time flips compose.fullScreen back off', async () => {
    composeMock.fullScreen = true;
    const { container } = render(ComposeWindow);
    await tick();
    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-fullscreen-toggle"]',
    );
    await fireEvent.click(toggle!);
    expect(composeMock.fullScreen).toBe(false);
  });

  it('no backdrop is rendered in full-screen mode', async () => {
    composeMock.fullScreen = true;
    const { container } = render(ComposeWindow);
    await tick();
    expect(container.querySelector('.backdrop')).toBeNull();
  });

  it('the backdrop is rendered in pop-up mode', async () => {
    const { container } = render(ComposeWindow);
    await tick();
    expect(container.querySelector('.backdrop')).not.toBeNull();
  });

  it('minimize calls composeStack.minimizeCurrent() while full screen', async () => {
    composeMock.fullScreen = true;
    const { container } = render(ComposeWindow);
    await tick();
    const minimize = container.querySelector<HTMLButtonElement>('.minimize');
    expect(minimize).not.toBeNull();
    await fireEvent.click(minimize!);
    expect(minimizeCurrent).toHaveBeenCalledTimes(1);
  });

  it('minimize calls composeStack.minimizeCurrent() while pop-up', async () => {
    const { container } = render(ComposeWindow);
    await tick();
    const minimize = container.querySelector<HTMLButtonElement>('.minimize');
    await fireEvent.click(minimize!);
    expect(minimizeCurrent).toHaveBeenCalledTimes(1);
  });
});
