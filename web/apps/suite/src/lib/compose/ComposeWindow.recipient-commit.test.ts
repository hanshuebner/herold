/**
 * Regression test for issue #419: Send must commit any pending,
 * structurally-complete recipient text into a chip before validating
 * recipients, so an address typed into the To/Cc/Bcc input but never
 * blurred (e.g. a Send click that fires before the field's own deferred
 * 120ms blur-commit lands) is not dropped with "At least one recipient is
 * required".
 *
 * Mirrors the mocking setup in ComposeWindow.from-gating.test.ts, which
 * exercises the same wired ComposeWindow + real RecipientField pairing.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { Identity } from '../mail/types';

// ── Mocks (factories must inline literals; vi.mock is hoisted) ────────────

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

vi.mock('./compose.svelte', () => {
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
    toRecipients: [] as Array<{ name?: string; email: string }>,
    ccRecipients: [] as Array<{ name?: string; email: string }>,
    bccRecipients: [] as Array<{ name?: string; email: string }>,
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
    moveRecipient: vi.fn(),
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

// Mock file-shares so ComposeWindow's $effect does not touch the live jmap client.
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
      'compose.from.picker.aria': 'From identity',
      'compose.from.picker.open': 'Change From identity',
      'compose.from.chip.verifying': 'Verification pending',
      'compose.from.chip.unverified': 'Unverified',
      'compose.from.chip.external': 'External SMTP missing',
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

// Stub the contacts/seen-addresses/jmap client seams RecipientField touches
// on focus/typing so the real component can render without network calls.
vi.mock('../contacts/store.svelte', () => ({
  contacts: {
    status: 'idle',
    suggestions: [],
    filter: () => [],
    filterAsync: vi.fn(async () => []),
    load: vi.fn(),
  },
}));
vi.mock('../contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'idle',
    entries: [],
    load: vi.fn(),
  },
}));
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn() },
  strict: vi.fn(),
}));
vi.mock('../auth/auth.svelte', () => ({
  auth: { session: { primaryAccounts: {} } },
  registerAccountResetCallback: vi.fn(),
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

// Imports after mocks. The compose singleton returned here is the mocked
// stub; mutating it between tests is how we drive scenarios.
const { compose } = await import('./compose.svelte');
const composeMock = compose as unknown as {
  isOpen: boolean;
  status: 'idle' | 'editing' | 'sending';
  errorMessage: string | null;
  selectedIdentity: Identity | null;
  toRecipients: Array<{ name?: string; email: string }>;
  to: string;
  send: ReturnType<typeof vi.fn>;
};

import ComposeWindow from './ComposeWindow.svelte';

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

describe('ComposeWindow commits a pending recipient on Send without blur (issue #419)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    composeMock.isOpen = true;
    composeMock.status = 'editing';
    composeMock.errorMessage = null;
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    composeMock.toRecipients = [];
    composeMock.to = '';
  });

  it('flushes a typed-but-not-blurred To address into a chip and sends', async () => {
    const { container } = render(ComposeWindow);
    await tick();

    const input = container.querySelector<HTMLInputElement>(
      '.recipient-field input[type="text"]',
    );
    expect(input).not.toBeNull();

    // Type the address via a real input event, without ever blurring the
    // field — the exact sequence a fast Send click can produce.
    await fireEvent.input(input as HTMLInputElement, {
      target: { value: 'dest@remote.test' },
    });
    await tick();

    // No chip yet: the buffer has not been committed.
    expect(container.querySelector('.chip')).toBeNull();

    const send = container.querySelector<HTMLButtonElement>(
      '[data-testid="compose-send"]',
    );
    expect(send).not.toBeNull();
    await fireEvent.click(send as HTMLButtonElement);
    await tick();

    // The pending text must have been committed into a chip and the send
    // must have gone through -- not blocked by "At least one recipient is
    // required".
    expect(composeMock.errorMessage).toBeNull();
    expect(composeMock.toRecipients).toHaveLength(1);
    expect(composeMock.toRecipients[0]).toMatchObject({ email: 'dest@remote.test' });
    expect(composeMock.to).toBe('dest@remote.test');
    expect(composeMock.send).toHaveBeenCalledTimes(1);
  });
});
