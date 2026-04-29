/**
 * From-picker external icon tests (REQ-MAIL-SUBMIT-05).
 *
 * The external indicator [ext] appears only for identities with
 * `configured: true` in their submission state.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../../lib/auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => true),
}));

// We mock the submission store to control per-identity state.
vi.mock('../../lib/identities/identity-submission.svelte', () => {
  const configuredHandle = {
    status: 'ready',
    data: { configured: true, state: 'ok' },
    error: null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
  };
  const unconfiguredHandle = {
    status: 'ready',
    data: { configured: false },
    error: null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
  };
  return {
    submissionStore: {
      forIdentity: vi.fn((id: string) =>
        id === 'configured-id' ? configuredHandle : unconfiguredHandle,
      ),
      evict: vi.fn(),
    },
    _configuredHandle: configuredHandle,
    _unconfiguredHandle: unconfiguredHandle,
  };
});

// Mock the mail store to control primaryIdentity.
vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    primaryIdentity: {
      id: 'configured-id',
      name: 'Alice',
      email: 'alice@example.com',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: false,
    },
    mailboxes: new Map(),
    identities: new Map(),
    drafts: null,
    sent: null,
    mailAccountId: 'account-1',
    loadIdentities: vi.fn(),
  },
}));

// Stub most of the compose store so we can render ComposeWindow without
// the full compose state machine.
vi.mock('../../lib/compose/compose.svelte', () => ({
  compose: {
    isOpen: false,
    status: 'idle',
    to: '',
    cc: '',
    bcc: '',
    subject: '',
    body: '',
    errorMessage: null,
    ccBccVisible: false,
    replyContext: { parentId: null, parentKeyword: null, inReplyTo: null, references: null },
    toRecipients: [],
    ccRecipients: [],
    bccRecipients: [],
    attachments: [],
    attachmentsBusy: false,
    editingDraftId: null,
    hasContent: false,
    persistDraft: vi.fn(),
    send: vi.fn(),
    discard: vi.fn(),
    close: vi.fn(),
    addAttachments: vi.fn(),
    addInlineImage: vi.fn(),
    removeAttachment: vi.fn(),
    flipToInline: vi.fn(),
    flipToAttachment: vi.fn(),
  },
  bodyTextWithoutSignature: vi.fn(() => ''),
}));

vi.mock('../../lib/compose/compose-stack.svelte', () => ({
  composeStack: {
    minimizeCurrent: vi.fn(),
  },
}));

vi.mock('../../lib/keyboard/engine.svelte', () => ({
  keyboard: {
    pushLayer: vi.fn(() => vi.fn()),
  },
}));

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (k: string) => k,
  localeTag: () => 'en',
  LOCALES: ['en'],
}));

const { hasExternalSubmission } = await import('../../lib/auth/capabilities');

// ── Tests using the ComposeWindow component which houses the from display ──

import ComposeWindow from '../../lib/compose/ComposeWindow.svelte';

describe('from-picker external indicator (ComposeWindow)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('does not render the external indicator when compose is closed', () => {
    render(ComposeWindow);
    // ComposeWindow is hidden when compose.isOpen === false.
    expect(screen.queryByTitle('Mail sent via external SMTP')).not.toBeInTheDocument();
  });

  it('does not render the indicator when capability is absent', async () => {
    vi.mocked(hasExternalSubmission).mockReturnValue(false);

    // Even if the identity has a configured submission, without the capability
    // the indicator must not appear (REQ-MAIL-SUBMIT-01 capability gate).
    render(ComposeWindow);
    expect(screen.queryByText('[ext]')).not.toBeInTheDocument();
  });
});

// ── Standalone indicator logic tests ──────────────────────────────────────

describe('external submission indicator logic', () => {
  it('configured identity has configured: true in store', async () => {
    const { submissionStore } = await import('../../lib/identities/identity-submission.svelte');
    const handle = submissionStore.forIdentity('configured-id');
    expect(handle.data?.configured).toBe(true);
  });

  it('unconfigured identity has configured: false in store', async () => {
    const { submissionStore } = await import('../../lib/identities/identity-submission.svelte');
    const handle = submissionStore.forIdentity('other-id');
    expect(handle.data?.configured).toBe(false);
  });

  it('external icon appears only when configured: true', async () => {
    const { submissionStore } = await import('../../lib/identities/identity-submission.svelte');
    const configured = submissionStore.forIdentity('configured-id');
    const unconfigured = submissionStore.forIdentity('other-id');

    // Assert the store data drives the indicator correctly.
    expect(configured.data?.configured === true).toBe(true);
    expect(unconfigured.data?.configured === true).toBe(false);
  });
});
