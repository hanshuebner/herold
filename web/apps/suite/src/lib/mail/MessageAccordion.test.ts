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
 * - Per-message kebab menu (conservative-slice restore of re #98): the
 *   kebab is back inside the message header, expanded-only, and carries
 *   the verbs documented in 02-mail-basics.md § Per-message context menu.
 *   Reply / reply-all / forward stay in the fixed reply bar; thread-only
 *   verbs (archive, snooze, move, label, mute, block sender, restore)
 *   live in ThreadToolbar.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
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

// ── Per-message kebab menu (conservative slice; re #98) ────────────────────
//
// The kebab is back inside the message header (expanded-only). The
// conservative slice in v1 — see docs/design/web/requirements/02-mail-basics.md
// § Per-message context menu — carries verbs that have an inherently
// per-message use case (download .eml, show original, print this message)
// or whose thread-scoped variant is wrong for multi-sender threads
// (delete one msg, mark one msg unread, mark unread from here, report
// spam, report phishing, filter messages like this). Reply / reply-all /
// forward stay in the fixed reply bar; block sender + mute thread stay
// in ThreadToolbar.

describe('MessageAccordion: per-message kebab menu (conservative slice)', () => {
  beforeEach(() => {
    mailMock.trash = null;
    mailMock.listFolder = 'inbox';
    mailMock.setSeen.mockClear();
  });

  it('does not render reply / reply-all / forward inside the message body', () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    // Reply / forward live in the ThreadReplyBar, not under each message.
    expect(screen.queryByLabelText('msg.reply')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.replyAll')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.forward')).not.toBeInTheDocument();
  });

  it('does not render thread-only verbs anywhere on the message', () => {
    // Block sender + mute thread are ThreadToolbar-only per the v1
    // conservative slice; they must not surface on the message header
    // or inside the kebab menu.
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);
    expect(screen.queryByLabelText('msg.muteThread')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('msg.blockSender')).not.toBeInTheDocument();
  });

  it('does not render the kebab when the message is collapsed', () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ false);
    expect(screen.queryByLabelText('msg.kebab.openLabel')).not.toBeInTheDocument();
  });

  it('renders the kebab trigger when the message is expanded', () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);
    expect(screen.getByLabelText('msg.kebab.openLabel')).toBeInTheDocument();
  });

  it('opens the menu on trigger click and shows the Mark unread item', async () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    // Menu is closed by default; items are absent until the trigger is clicked.
    expect(screen.queryByText('msg.kebab.markUnread')).not.toBeInTheDocument();

    await fireEvent.click(screen.getByLabelText('msg.kebab.openLabel'));
    expect(screen.getByText('msg.kebab.markUnread')).toBeInTheDocument();
  });

  it('Mark unread item calls mail.setSeen(emailId, false)', async () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    await fireEvent.click(screen.getByLabelText('msg.kebab.openLabel'));
    await fireEvent.click(screen.getByText('msg.kebab.markUnread'));

    expect(mailMock.setSeen).toHaveBeenCalledWith(email.id, false);
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

// ── auto-read latch (issue #102) ─────────────────────────────────────────
describe('MessageAccordion auto-read latch', () => {
  beforeEach(() => {
    mailMock.setSeen.mockClear();
  });

  it('does not call setSeen when opening an already-seen message', async () => {
    const email = makeEmail({});
    (email as { keywords: Record<string, true> }).keywords = { $seen: true };
    renderAccordion(email, /* expanded */ true);
    // Allow the $effect to flush.
    await Promise.resolve();
    expect(mailMock.setSeen).not.toHaveBeenCalled();
  });

  it('calls setSeen exactly once when opening an unread message', async () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);
    await Promise.resolve();
    expect(mailMock.setSeen).toHaveBeenCalledTimes(1);
    expect(mailMock.setSeen).toHaveBeenCalledWith('e1', true);
  });

  it('latches on first expansion so a later $seen flip cannot re-trigger', async () => {
    // The bug: open already-seen → effect short-circuits without
    // setting the latch → user marks unread → keywords change re-runs
    // the effect → autoReadDone is still false → fires setSeen(_, true)
    // racing the user's setSeen(_, false). The fix sets the latch
    // unconditionally on first expansion.
    const email = makeEmail({});
    (email as { keywords: Record<string, true> }).keywords = { $seen: true };
    const { rerender } = renderAccordion(email, /* expanded */ true);
    await Promise.resolve();
    expect(mailMock.setSeen).not.toHaveBeenCalled();

    // Simulate Mark unread: keywords change from { $seen: true } to {}.
    const flippedEmail = { ...email, keywords: {} };
    await rerender({ email: flippedEmail, expanded: true, onToggle: vi.fn() });
    await Promise.resolve();
    expect(mailMock.setSeen).not.toHaveBeenCalled();
  });
});
