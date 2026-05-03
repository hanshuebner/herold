/**
 * Unit tests for MessageAccordion.
 *
 * - Attachment indicator in the header: verifies that the paperclip icon
 *   appears when the email has at least one non-inline attachment, and is
 *   suppressed otherwise.
 * - Label badges in the expanded message header (re #66): badges appear for
 *   custom-mailbox membership when the message is expanded, are absent when
 *   collapsed, and are suppressed for the active list folder.
 * - Restore from trash navigation (re #29): clicking the Restore button
 *   calls restoreFromTrash and then navigates back.
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

vi.mock('../compose/compose.svelte', () => ({
  compose: {
    openReply: vi.fn(),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
  },
}));

vi.mock('./move-picker.svelte', () => ({
  movePicker: { open: vi.fn() },
}));

vi.mock('./label-picker.svelte', () => ({
  labelPicker: { open: vi.fn() },
}));

vi.mock('./snooze-picker.svelte', () => ({
  snoozePicker: { open: vi.fn() },
}));

vi.mock('./reaction-confirm.svelte', () => ({
  reactionConfirm: { needsConfirm: () => false },
}));

vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: () => () => undefined },
}));

vi.mock('../settings/managed-rules.svelte', () => ({
  managedRules: {
    isThreadMuted: () => false,
    muteThread: vi.fn(),
    unmuteThread: vi.fn(),
    blockSender: vi.fn(),
  },
}));

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

describe('MessageAccordion: label badges in expanded message header (re #66)', () => {
  beforeEach(() => {
    // Reset mailMock state that other describe blocks may have mutated.
    mailMock.listFolder = 'inbox';
    mailMock.trash = null;
  });

  it('shows a label badge for a custom mailbox when the message is expanded', () => {
    const email = makeEmail({ mailboxIds: { 'mbx-work': true } });
    renderAccordion(email, true);
    const badge = screen.getByText('Work');
    expect(badge).toBeInTheDocument();
    expect(badge.classList.contains('label-badge')).toBe(true);
  });

  it('does not show label badges when the message is collapsed', () => {
    const email = makeEmail({ mailboxIds: { 'mbx-work': true } });
    renderAccordion(email, false);
    // Collapsed: the labels row must not be rendered at all.
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();
  });

  it('does not show a badge when the email belongs to no custom mailbox', () => {
    const email = makeEmail({ mailboxIds: {} });
    renderAccordion(email, true);
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();
  });

  it('suppresses the badge for the active list folder', () => {
    // When listFolder matches the custom mailbox id, no badge should appear
    // (the user is already browsing that label; showing it is redundant).
    mailMock.listFolder = WORK_MBX.id;
    const email = makeEmail({ mailboxIds: { 'mbx-work': true } });
    renderAccordion(email, true);
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();
  });
});

describe('MessageAccordion: restore from trash navigates back (re #29)', () => {
  beforeEach(() => {
    mailMock.trash = TRASH_MBX;
    mailMock.listFolder = 'trash';
    mailMock.restoreFromTrash.mockClear();
    mailMock.restoreFromTrash.mockResolvedValue(undefined);
    vi.spyOn(window.history, 'back').mockImplementation(() => undefined);
  });

  it('calls restoreFromTrash then history.back when history is available', async () => {
    // Simulate a real browser with navigation history (length > 1).
    Object.defineProperty(window.history, 'length', { value: 3, configurable: true });

    const email = makeEmail({ mailboxIds: { [TRASH_MBX.id]: true } });
    renderAccordion(email, /* expanded */ true);

    const btn = screen.getByLabelText('msg.restore');
    await fireEvent.click(btn);

    expect(mailMock.restoreFromTrash).toHaveBeenCalledWith('e1');
    expect(window.history.back).toHaveBeenCalled();
  });

  it('falls back to router.navigate when history length is 1', async () => {
    const { router } = await import('../router/router.svelte');
    // No history to go back to — the fallback path uses router.navigate.
    Object.defineProperty(window.history, 'length', { value: 1, configurable: true });

    const email = makeEmail({ mailboxIds: { [TRASH_MBX.id]: true } });
    renderAccordion(email, /* expanded */ true);

    const btn = screen.getByLabelText('msg.restore');
    await fireEvent.click(btn);

    expect(mailMock.restoreFromTrash).toHaveBeenCalledWith('e1');
    expect(router.navigate).toHaveBeenCalledWith('/mail/folder/trash');
  });
});
