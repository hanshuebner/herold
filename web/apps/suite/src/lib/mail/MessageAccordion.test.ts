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
 * - Restore from trash via the per-message header kebab (re #29, re #98):
 *   the bottom action row was removed; restore now lives in the kebab.
 *   Test opens the kebab then clicks the menu item.
 * - No bottom action row (re #98): the per-message action toolbar was
 *   removed in favour of thread-level actions plus reply / reply-all /
 *   forward in the fixed reply bar.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import MessageAccordion from './MessageAccordion.svelte';
import type { Email, EmailBodyPart } from './types';

// ── module mocks ──────────────────────────────────────────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
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

vi.mock('../settings/managed-rules.svelte', () => ({}));

vi.mock('../settings/filter-like.svelte', () => ({
  filterLike: { set: vi.fn() },
}));

vi.mock('../router/router.svelte', () => ({
  router: { navigate: vi.fn() },
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
}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: overrides.mailboxIds ?? {},
    keywords: {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
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

describe('MessageAccordion: restore from trash via header kebab (re #29, re #98)', () => {
  beforeEach(() => {
    mailMock.trash = TRASH_MBX;
    mailMock.listFolder = 'trash';
    mailMock.restoreFromTrash.mockClear();
    mailMock.restoreFromTrash.mockResolvedValue(undefined);
    vi.spyOn(window.history, 'back').mockImplementation(() => undefined);
  });

  it('opens the kebab and calls restoreFromTrash then history.back when history is available', async () => {
    Object.defineProperty(window.history, 'length', { value: 3, configurable: true });

    const email = makeEmail({ mailboxIds: { [TRASH_MBX.id]: true } });
    renderAccordion(email, /* expanded */ true);

    // Open the per-message overflow menu.
    const trigger = screen.getByLabelText('actions.moreActions');
    await fireEvent.click(trigger);

    // Menu items render after open; click the restore item.
    const btn = screen.getByText('msg.restore');
    await fireEvent.click(btn);

    expect(mailMock.restoreFromTrash).toHaveBeenCalledWith('e1');
    expect(window.history.back).toHaveBeenCalled();
  });

  it('falls back to router.navigate when history length is 1', async () => {
    const { router } = await import('../router/router.svelte');
    Object.defineProperty(window.history, 'length', { value: 1, configurable: true });

    const email = makeEmail({ mailboxIds: { [TRASH_MBX.id]: true } });
    renderAccordion(email, /* expanded */ true);

    const trigger = screen.getByLabelText('actions.moreActions');
    await fireEvent.click(trigger);

    const btn = screen.getByText('msg.restore');
    await fireEvent.click(btn);

    expect(mailMock.restoreFromTrash).toHaveBeenCalledWith('e1');
    expect(router.navigate).toHaveBeenCalledWith('/mail/folder/trash');
  });
});

// ── No per-message bottom action toolbar (re #98) ────────────────────────────
//
// The per-message action row was removed: reply / reply-all / forward live
// in the fixed reply bar; mute / spam / phishing / block / archive / etc.
// live in ThreadToolbar; rare per-message verbs (filterLike, viewOriginal,
// restore-in-trash) live in the small header kebab.

describe('MessageAccordion: no bottom action row (re #98)', () => {
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

  it('exposes a per-message kebab in the header when expanded', () => {
    // filterLike is always available; viewOriginal needs blobId; restore
    // needs trash-membership. The kebab should always render when at
    // least one item applies — filterLike alone is enough.
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ true);

    expect(screen.getByLabelText('actions.moreActions')).toBeInTheDocument();
  });

  it('does not render the kebab when collapsed', () => {
    const email = makeEmail({});
    renderAccordion(email, /* expanded */ false);

    expect(screen.queryByLabelText('actions.moreActions')).not.toBeInTheDocument();
  });
});
