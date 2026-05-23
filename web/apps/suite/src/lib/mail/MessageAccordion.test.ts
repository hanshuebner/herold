/**
 * Unit tests for MessageAccordion.
 *
 * - Attachment indicator in the header: verifies that the paperclip icon
 *   appears when the email has at least one non-inline attachment, and is
 *   suppressed otherwise.
 * - No per-message label badges (re #66, re #70): label display lives on
 *   ThreadReader.svelte's thread-level header so badges are always visible
 *   regardless of accordion expansion state. MessageAccordion no longer
 *   renders label badges at all.
 * - No per-message action surface (re #98): the per-message action
 *   toolbar AND the per-message header kebab were both removed. Reply /
 *   reply-all / forward live in the fixed reply bar; thread-scoped verbs
 *   (archive, delete, mark unread, snooze, move, label, mute, spam,
 *   phishing, block, restore, print) live in ThreadToolbar; reactions
 *   live inline with the message title row.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import MessageAccordion from './MessageAccordion.svelte';
import type { Email, EmailBodyPart } from './types';

// ── module mocks ──────────────────────────────────────────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  // Return the key unchanged so tests can assert on the i18n key, not a
  // hardcoded English string. This validates the correct key is used.
  t: (key: string) => key,
  localeTag: () => 'en',
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
    },
    principalId: 'p1',
  },
}));

vi.mock('../jmap/client', () => ({
  jmap: {
    downloadUrl: () => null,
  },
}));

// vi.mock() factories are hoisted by vitest so they run before module-level
// variable initialisers.  Use vi.hoisted() to define shared state that both
// the factory and the test body can access.
const { mailMock, WORK_MBX, TRASH_MBX } = vi.hoisted(() => {
  const WORK_MBX = {
    id: 'mbx-work',
    name: 'Work',
    role: null,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  } as import('./types').Mailbox;

  const TRASH_MBX = {
    id: 'mbx-trash',
    name: 'Trash',
    role: 'trash',
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  } as import('./types').Mailbox;

  // The mail store mock exposes customMailboxes and listFolder so that the
  // emailLabels derived value in MessageAccordion can compute badge names.
  // listFolder and trash are mutable so individual tests can override them.
  const mailMock = {
    mailboxes: new Map([['mbx-work', WORK_MBX]]),
    get customMailboxes(): import('./types').Mailbox[] {
      return [WORK_MBX];
    },
    listFolder: 'inbox' as string,
    identities: new Map(),
    trash: null as import('./types').Mailbox | null,
    setSeen: vi.fn(),
    toggleImportant: vi.fn(),
    unsnoozeEmail: vi.fn(),
    restoreFromTrash: vi.fn().mockResolvedValue(undefined),
    toggleReaction: vi.fn(),
    reportSpam: vi.fn(),
  };

  return { mailMock, WORK_MBX, TRASH_MBX };
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
  htmlHasExternalImages: () => false,
}));

vi.mock('./quoted', () => ({
  splitQuotedText: (t: string) => ({ fresh: t, quoted: null }),
}));

vi.mock('./types', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./types')>();
  return {
    ...actual,
    emailHtmlBody: () => null,
    emailTextBody: () => null,
  };
});

// ── test helpers ──────────────────────────────────────────────────────────────

function makePart(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: 'p1',
    blobId: 'b1',
    size: 512,
    type: 'application/pdf',
    charset: null,
    disposition: 'attachment',
    name: 'file.pdf',
    cid: null,
    ...overrides,
  };
}

function makeEmail(overrides: {
  hasAttachment?: boolean;
  attachments?: Partial<EmailBodyPart>[];
  mailboxIds?: Record<string, true>;
  from?: Array<{ name: string | null; email: string }>;
}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: overrides.mailboxIds ?? {},
    keywords: {},
    from: overrides.from ?? [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    subject: 'Test subject',
    preview: 'preview text',
    receivedAt: '2026-04-30T10:00:00Z',
    hasAttachment: overrides.hasAttachment ?? false,
    attachments: overrides.attachments?.map(makePart),
    reactions: [],
    snoozedUntil: null,
    'header:List-ID:asText': null,
  } as unknown as Email;
}

function renderAccordion(email: Email, expanded = false) {
  return render(MessageAccordion, {
    props: { email, expanded, onToggle: vi.fn() },
  });
}

// ── tests ─────────────────────────────────────────────────────────────────────

describe('MessageAccordion: attachment indicator in header', () => {
  it('renders the icon when attachments contains a non-inline part', () => {
    const email = makeEmail({
      hasAttachment: true,
      attachments: [{ disposition: 'attachment', name: 'report.pdf' }],
    });
    renderAccordion(email);
    const icon = screen.getByLabelText('att.headerIcon.label');
    expect(icon).toBeInTheDocument();
  });

  it('does not render the icon when all attachment parts are inline', () => {
    const email = makeEmail({
      hasAttachment: false,
      attachments: [{ disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' }],
    });
    renderAccordion(email);
    expect(screen.queryByLabelText('att.headerIcon.label')).not.toBeInTheDocument();
  });

  it('does not render the icon when attachments is empty and hasAttachment is false', () => {
    const email = makeEmail({
      hasAttachment: false,
      attachments: [],
    });
    renderAccordion(email);
    expect(screen.queryByLabelText('att.headerIcon.label')).not.toBeInTheDocument();
  });

  it('falls back to hasAttachment flag when attachments is undefined', () => {
    // makeEmail passes attachments: undefined when omitted from overrides
    const base = makeEmail({ hasAttachment: true });
    // Explicitly strip the attachments field to test the fallback path.
    const email = { ...base, attachments: undefined } as unknown as Email;
    renderAccordion(email);
    const icon = screen.getByLabelText('att.headerIcon.label');
    expect(icon).toBeInTheDocument();
  });

  it('does not render the icon when attachments is undefined and hasAttachment is false', () => {
    const base = makeEmail({ hasAttachment: false });
    const email = { ...base, attachments: undefined } as unknown as Email;
    renderAccordion(email);
    expect(screen.queryByLabelText('att.headerIcon.label')).not.toBeInTheDocument();
  });
});

describe('MessageAccordion: no per-message label badges (re #66, re #70)', () => {
  beforeEach(() => {
    // Reset mailMock state that other describe blocks may have mutated.
    mailMock.listFolder = 'inbox';
    mailMock.trash = null;
  });

  it('does not render a label badge even when expanded and email is in a custom mailbox', () => {
    // Label badges were moved to ThreadReader.svelte (thread-level header).
    // MessageAccordion must not render any label badge row.
    const email = makeEmail({ mailboxIds: { 'mbx-work': true } });
    renderAccordion(email, true);
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();
  });

  it('does not render a label badge when collapsed', () => {
    const email = makeEmail({ mailboxIds: { 'mbx-work': true } });
    renderAccordion(email, false);
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();
  });
});

// ── No per-message action surface (re #98) ──────────────────────────────────
//
// The per-message action row AND the per-message header kebab were both
// removed: reply / reply-all / forward live in the fixed reply bar; thread
// verbs (mute, spam, phishing, block, archive, mark-unread, snooze, move,
// label, restore, print) live in ThreadToolbar; reactions live in the
// message title row above.

describe('MessageAccordion: no per-message action surface (re #98)', () => {
  beforeEach(() => {
    mailMock.trash = null;
    mailMock.listFolder = 'inbox';
  });

  it('does not render reply / reply-all / forward inside the message body', () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    // Reply / forward live in the ThreadReplyBar, not under each message.
    expect(screen.queryByLabelText('msg.reply')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.replyAll')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.forward')).not.toBeInTheDocument();
  });

  it('does not render thread-level actions under the message', () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    // These all live in ThreadToolbar.
    expect(screen.queryByLabelText('msg.muteThread')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.reportSpam')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.reportPhishing')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.blockSender')).not.toBeInTheDocument();
  });

  it('does not render a per-message kebab when expanded', () => {
    // The kebab was removed entirely (re #98). filterLike / viewOriginal /
    // restore-in-trash were the kebab items; restore is now thread-only,
    // and filterLike / viewOriginal were judged not worth a per-message
    // surface.
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    expect(screen.queryByLabelText('actions.moreActions')).not.toBeInTheDocument();
  });

  it('does not render the kebab when in trash either', () => {
    // Even in trash there is no per-message kebab — restore is offered at
    // thread scope by ThreadToolbar.
    mailMock.trash = TRASH_MBX;
    mailMock.listFolder = 'trash';
    const email = makeEmail({ mailboxIds: { [TRASH_MBX.id]: true } });
    renderAccordion(email, /* expanded */ true);

    expect(screen.queryByLabelText('actions.moreActions')).not.toBeInTheDocument();
  });
});

// ── Self-authored card treatment ─────────────────────────────────────────────
//
// When the message From address is one of the user's own identities:
//   A) The <article> carries the .self class (visual tint + border).
//   B) The sender label shows the i18n key 'mail.thread.fromYou' (not
//      a hardcoded "You" — the mock t() returns the key so we assert on
//      the key, proving the translation path is exercised).

describe('MessageAccordion: self-authored card treatment', () => {
  const SELF_EMAIL = 'self@example.test';

  const selfIdentity: import('./types').Identity = {
    id: 'ident-self',
    name: 'Self User',
    email: SELF_EMAIL,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
  };

  beforeEach(() => {
    mailMock.trash = null;
    mailMock.listFolder = 'inbox';
    // Reset identities to empty between tests.
    mailMock.identities = new Map();
  });

  afterEach(() => {
    mailMock.identities = new Map();
  });

  it('does NOT add .self class when From is not in the identity set', () => {
    // No identities loaded — alice@example.test is not self.
    const email = makeEmail({
      from: [{ name: 'Alice', email: 'alice@example.test' }],
    });
    const { container } = renderAccordion(email);
    const article = container.querySelector('article.message');
    expect(article).not.toHaveClass('self');
  });

  it('adds .self class when From matches a user identity', () => {
    mailMock.identities = new Map([[selfIdentity.id, selfIdentity]]);
    const email = makeEmail({
      from: [{ name: 'Self User', email: SELF_EMAIL }],
    });
    const { container } = renderAccordion(email);
    const article = container.querySelector('article.message');
    expect(article).toHaveClass('self');
  });

  it('adds .self class for a case-insensitive From match', () => {
    mailMock.identities = new Map([[selfIdentity.id, selfIdentity]]);
    const email = makeEmail({
      from: [{ name: 'Self', email: 'SELF@EXAMPLE.TEST' }],
    });
    const { container } = renderAccordion(email);
    expect(container.querySelector('article.message')).toHaveClass('self');
  });

  it('shows the fromYou i18n key in the collapsed sender label when self', () => {
    // The t() mock returns the key verbatim; asserting on the key proves
    // the component uses the translation path rather than a hardcoded literal.
    mailMock.identities = new Map([[selfIdentity.id, selfIdentity]]);
    const email = makeEmail({
      from: [{ name: 'Self User', email: SELF_EMAIL }],
    });
    renderAccordion(email, /* expanded */ false);
    expect(screen.getByText('mail.thread.fromYou')).toBeInTheDocument();
  });

  it('shows the fromYou i18n key in the expanded sender label when self', () => {
    mailMock.identities = new Map([[selfIdentity.id, selfIdentity]]);
    const email = makeEmail({
      from: [{ name: 'Self User', email: SELF_EMAIL }],
    });
    renderAccordion(email, /* expanded */ true);
    expect(screen.getByText('mail.thread.fromYou')).toBeInTheDocument();
  });

  it('does NOT show the sender name when self (collapsed)', () => {
    mailMock.identities = new Map([[selfIdentity.id, selfIdentity]]);
    const email = makeEmail({
      from: [{ name: 'Self User', email: SELF_EMAIL }],
    });
    renderAccordion(email, /* expanded */ false);
    expect(screen.queryByText('Self User')).not.toBeInTheDocument();
  });

  it('shows normal sender name when From is not self', () => {
    const email = makeEmail({
      from: [{ name: 'Alice', email: 'alice@example.test' }],
    });
    renderAccordion(email, /* expanded */ false);
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.queryByText('mail.thread.fromYou')).not.toBeInTheDocument();
  });
});
