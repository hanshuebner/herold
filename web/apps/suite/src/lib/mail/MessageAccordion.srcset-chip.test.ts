/**
 * Issue #306, second round -- re-check of the #269/#270 chip paths.
 *
 * The attachment chip strip's undecodable/unnamed handling
 * (`staticUndecodableCids` in MessageAccordion.svelte, `(unnamed)` display
 * name in AttachmentList.svelte) is derived entirely from `email.attachments`
 * metadata (`part.type`, `part.cid`, `part.name`, `part.disposition`) --
 * never from which HTML attribute the message body happens to reference the
 * cid through. Adding `srcset` resolution to `sanitize.ts` must not change
 * that: an inline part referenced only inside `srcset` (never `src`) still
 * gets its download chip when it is undecodable (#269) or unnamed (#270).
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import MessageAccordion from './MessageAccordion.svelte';
import type { Email, EmailBodyPart } from './types';

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
    downloadUrl: (args: { accountId: string; blobId: string; name?: string }) =>
      `/jmap/download/${args.accountId}/${args.blobId}/${encodeURIComponent(args.name ?? '')}`,
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

// happy-dom does not fire real iframe onload events, so the rendered
// srcdoc content is not actually parsed/decoded here (see
// HtmlBody.gating.test.ts) -- the chip assertions below exercise only the
// static (part.type-based) signal, which is computed independent of the
// iframe. A trivial passthrough is enough; real per-candidate srcset
// resolution is covered by sanitize.test.ts.
vi.mock('./sanitize', () => ({
  sanitizeHtml: (html: string) => `<!doctype html><html><body>${html}</body></html>`,
  htmlHasExternalImages: () => false,
}));

vi.mock('./quoted', () => ({
  splitQuotedText: (t: string) => ({ head: t, collapsed: '', tail: '' }),
}));

function makePart(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: 'p1',
    blobId: 'b1',
    size: 512,
    type: 'image/png',
    charset: null,
    disposition: 'inline',
    name: 'inline.png',
    cid: null,
    ...overrides,
  } as EmailBodyPart;
}

function makeEmail(
  id: string,
  attachments: Partial<EmailBodyPart>[],
  htmlValue: string,
): Email {
  return {
    id,
    threadId: 't1',
    blobId: 'root-blob',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Forum', email: 'forum@classic-computing.de' }],
    to: null,
    cc: null,
    subject: 'Reply notification',
    preview: 'preview',
    receivedAt: '2026-09-18T10:52:00Z',
    hasAttachment: true,
    snoozedUntil: null,
    'header:List-ID:asText': null,
    reactions: [],
    htmlBody: [
      {
        partId: 'p-html',
        blobId: 'html-blob-id',
        size: htmlValue.length,
        type: 'text/html',
        charset: 'utf-8',
        disposition: null,
        name: null,
        cid: null,
      },
    ],
    textBody: [],
    attachments: attachments.map(makePart),
    bodyValues: {
      'p-html': {
        value: htmlValue,
        isEncodingProblem: false,
        isTruncated: false,
      },
    },
  } as unknown as Email;
}

describe('MessageAccordion: #269 undecodable inline chip, srcset fixture (re #306)', () => {
  it('chips a TIFF inline part referenced only via srcset (never src)', () => {
    const email = makeEmail(
      'e-269-srcset',
      [
        {
          disposition: 'inline',
          name: 'PastedGraphic-2.tiff',
          cid: 'sig@h.test',
          type: 'image/tiff',
          blobId: 'b0',
        },
      ],
      // The cid is referenced only inside a 2x srcset candidate, never src --
      // exactly the shape that reproduced issue #306's second round.
      '<img alt=":)" srcset="cid:sig@h.test 2x">',
    );
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });
    expect(screen.getByText('PastedGraphic-2.tiff')).toBeInTheDocument();
  });
});

describe('MessageAccordion: #270 unnamed inline chip, srcset fixture (re #306)', () => {
  it('chips an undecodable inline part with no filename referenced only via srcset', () => {
    const email = makeEmail(
      'e-270-srcset',
      [
        {
          disposition: 'inline',
          name: null,
          cid: 'unnamed@h.test',
          type: 'image/tiff',
          blobId: 'b0',
        },
      ],
      '<img alt=":)" srcset="cid:unnamed@h.test 2x">',
    );
    render(MessageAccordion, { props: { email, expanded: true, onToggle: vi.fn() } });
    expect(screen.getByText('(unnamed)')).toBeInTheDocument();
  });
});
