/**
 * Send + Archive coverage for #376: the created reply must be archived
 * alongside the thread's pre-existing Inbox members, not just the members
 * the composer already knew about.
 *
 * Before the fix, sendAndArchive() called compose.send() with no
 * arguments and only ever archived inboxEmailIds (the thread's
 * pre-existing Inbox members) via mail.bulkArchive. The just-sent reply
 * is never an Inbox member at send time, so it was silently skipped —
 * confirmed here by asserting compose.send() is called with
 * `{ archiveOnSend: true }` so the reply is filed into Archive as part
 * of the send's own onSuccessUpdateEmail patch (compose.svelte.ts), while
 * mail.bulkArchive keeps handling the pre-existing Inbox members exactly
 * as before.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { Identity } from './types';

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
    send: vi.fn(async () => {
      compose.isOpen = false;
    }),
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
  const mail = {
    primaryIdentity: DEFAULT_IDENTITY as Identity | null,
    identities: new Map<string, Identity>([[DEFAULT_IDENTITY.id, DEFAULT_IDENTITY]]),
    inbox: { id: 'inbox-1' } as { id: string } | null,
    archive: { id: 'archive-1' } as { id: string } | null,
    threadEmails: vi.fn((_tid: string) => []),
    bulkArchive: vi.fn(),
    loadIdentities: vi.fn(),
  };
  return { mail };
});

const { compose } = await import('../compose/compose.svelte');
const { mail } = await import('./store.svelte');
const composeMock = compose as unknown as {
  isOpen: boolean;
  inlineMode: boolean;
  status: 'idle' | 'editing' | 'sending';
  selectedIdentity: Identity | null;
  send: ReturnType<typeof vi.fn>;
};
const mailMock = mail as unknown as {
  threadEmails: ReturnType<typeof vi.fn>;
  bulkArchive: ReturnType<typeof vi.fn>;
};

import ThreadInlineComposer from './ThreadInlineComposer.svelte';

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

describe('ThreadInlineComposer Send + Archive (re #376)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    composeMock.isOpen = true;
    composeMock.inlineMode = true;
    composeMock.status = 'editing';
    composeMock.selectedIdentity = DEFAULT_IDENTITY;
    composeMock.send = vi.fn(async () => {
      composeMock.isOpen = false;
    });
    mailMock.threadEmails.mockReturnValue([
      { id: 'e-inbox', mailboxIds: { 'inbox-1': true } },
      { id: 'e-other', mailboxIds: { 'other-1': true } },
    ]);
  });

  it('sends with archiveOnSend and archives the pre-existing Inbox members', async () => {
    const { container } = render(ThreadInlineComposer, {
      props: { threadId: 'tid-1' },
    });
    await tick();

    const btn = container.querySelector<HTMLButtonElement>('[data-testid="inline-send-archive"]');
    expect(btn).not.toBeNull();
    await fireEvent.click(btn!);
    await tick();
    await tick();

    // The created reply is archived as part of the send itself.
    expect(composeMock.send).toHaveBeenCalledWith({ archiveOnSend: true });

    // The pre-existing Inbox member is still archived via bulkArchive,
    // exactly as before -- the non-Inbox thread email is excluded.
    expect(mailMock.bulkArchive).toHaveBeenCalledWith(['e-inbox']);
    expect(mailMock.bulkArchive).not.toHaveBeenCalledWith(
      expect.arrayContaining(['e-other']),
    );
  });
});
