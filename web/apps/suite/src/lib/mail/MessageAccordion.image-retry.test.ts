/**
 * Component tests for the failed-external-image retry affordance (issue
 * #162, REQ-EXTIMG-71/73).
 *
 * `Email.failedImageCount > 0` renders a badge/banner offering to retry;
 * clicking it calls `mail.retryEmailImages(email.id)`. A `retriedCount > 0`
 * result means the images now render (no origin URL involved on the client
 * side at any point -- the store's retry call and refetch are mocked here);
 * `retriedCount === 0` surfaces a "still unavailable" note alongside the
 * unchanged badge.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/svelte';
import MessageAccordion from './MessageAccordion.svelte';
import type { Email } from './types';

// ── module mocks ──────────────────────────────────────────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>) => {
    if (args?.count !== undefined) return `${key}:${args.count}`;
    return key;
  },
  localeTag: () => 'en',
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../jmap/client', () => ({
  jmap: {
    downloadUrl: (args: { accountId: string; blobId: string; type: string; name: string }) =>
      `/jmap/download/${args.accountId}/${args.blobId}`,
  },
}));

const { mailMock } = vi.hoisted(() => {
  const mailMock = {
    mailboxes: new Map(),
    get customMailboxes(): import('./types').Mailbox[] {
      return [];
    },
    listFolder: 'inbox' as string,
    identities: new Map(),
    trash: null as import('./types').Mailbox | null,
    setSeen: vi.fn(),
    markUnreadFromHere: vi.fn(),
    deleteEmail: vi.fn(),
    toggleImportant: vi.fn(),
    unsnoozeEmail: vi.fn(),
    restoreFromTrash: vi.fn().mockResolvedValue(undefined),
    toggleReaction: vi.fn(),
    reportSpam: vi.fn(),
    reportPhishing: vi.fn(),
    retryEmailImages: vi.fn(),
  };
  return { mailMock };
});

vi.mock('./store.svelte', () => ({ mail: mailMock }));

vi.mock('./avatar-resolver.svelte', () => ({
  resolve: vi.fn().mockResolvedValue(null),
  avatarEmailMetadataEnabled: () => false,
  setAvatarEmailMetadataEnabled: vi.fn(),
  clearAvatarCache: vi.fn(),
}));

vi.mock('./identity-avatar', () => ({
  identityAvatarUrl: () => null,
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: {
    isImageAllowed: () => false,
    addImageAllowedSender: vi.fn(),
  },
}));

vi.mock('./reaction-confirm.svelte', () => ({
  reactionConfirm: { needsConfirm: () => false },
}));

vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: () => () => undefined },
}));

vi.mock('../llm/transparency.svelte', () => ({
  llmTransparency: { available: false },
}));

vi.mock('./sanitize', () => ({
  sanitizeHtml: (html: string) => `<!doctype html><html><body>${html}</body></html>`,
  htmlHasExternalImages: () => false,
}));

vi.mock('./quoted', () => ({
  splitQuotedText: (t: string) => ({ head: t, collapsed: '', tail: '' }),
}));

// ── helpers ───────────────────────────────────────────────────────────────────

function makeEmailWithFailedImages(failedImageCount: number, emailId = 'e1'): Email {
  return {
    id: emailId,
    threadId: 't1',
    blobId: 'root-blob',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'GLS', email: 'no-reply@gls.example' }],
    to: null,
    cc: null,
    subject: 'Your package is arriving',
    preview: 'preview',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    snoozedUntil: null,
    'header:List-ID:asText': null,
    reactions: null,
    failedImageCount,
    htmlBody: [
      {
        partId: 'p-html',
        blobId: 'html-blob-id',
        size: 128,
        type: 'text/html',
        charset: 'utf-8',
        disposition: null,
        name: null,
        cid: null,
      },
    ],
    textBody: [],
    attachments: [],
    bodyValues: {
      'p-html': {
        value: '<p>Track your package</p>',
        isEncodingProblem: false,
        isTruncated: false,
      },
    },
  } as unknown as Email;
}

// ── tests ─────────────────────────────────────────────────────────────────────

describe('MessageAccordion: failed-image retry (issue #162)', () => {
  beforeEach(() => {
    mailMock.retryEmailImages.mockReset();
  });

  it('does not render the retry banner when failedImageCount is 0/absent', () => {
    const email = makeEmailWithFailedImages(0, 'e-none-' + Math.random());
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });
    expect(screen.queryByText(/msg\.imagesFailed/)).not.toBeInTheDocument();
  });

  it('renders the failed-image count and a retry button when failedImageCount > 0', () => {
    const email = makeEmailWithFailedImages(3, 'e-badge-' + Math.random());
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });
    expect(screen.getByText('msg.imagesFailed:3')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'msg.retryImages' })).toBeInTheDocument();
  });

  it('clicking retry calls mail.retryEmailImages with the message id', async () => {
    mailMock.retryEmailImages.mockResolvedValue({ retriedCount: 3, failedImageCount: 0 });
    const email = makeEmailWithFailedImages(3, 'e-click-' + Math.random());
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });

    await fireEvent.click(screen.getByRole('button', { name: 'msg.retryImages' }));

    expect(mailMock.retryEmailImages).toHaveBeenCalledWith(email.id);
  });

  it('shows "still unavailable" when a retry improves nothing (retriedCount 0)', async () => {
    mailMock.retryEmailImages.mockResolvedValue({ retriedCount: 0, failedImageCount: 3 });
    const email = makeEmailWithFailedImages(3, 'e-stillfailed-' + Math.random());
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });

    await fireEvent.click(screen.getByRole('button', { name: 'msg.retryImages' }));

    await waitFor(() => {
      expect(screen.getByText('msg.imagesStillFailed')).toBeInTheDocument();
    });
    // The badge is still shown -- the count did not clear.
    expect(screen.getByText('msg.imagesFailed:3')).toBeInTheDocument();
  });

  it('does not show "still unavailable" when the retry succeeds', async () => {
    mailMock.retryEmailImages.mockResolvedValue({ retriedCount: 2, failedImageCount: 1 });
    const email = makeEmailWithFailedImages(3, 'e-partial-' + Math.random());
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });

    await fireEvent.click(screen.getByRole('button', { name: 'msg.retryImages' }));

    await waitFor(() => {
      expect(mailMock.retryEmailImages).toHaveBeenCalled();
    });
    expect(screen.queryByText('msg.imagesStillFailed')).not.toBeInTheDocument();
  });
});
