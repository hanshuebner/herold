/**
 * Component tests for the snooze reader banners (issue #469).
 *
 * While-snoozed banner (work item 1): a message whose `Email.snoozedUntil`
 * is set renders a banner in the reading pane naming the due time, and --
 * only when `Email.snoozeWakeMailboxId` names a mailbox other than the
 * account's Inbox -- the wake destination mailbox's name. The banner's
 * Cancel button calls `mail.unsnoozeEmail(emailId)`; no new wire data is
 * fetched, everything the banner needs is already on the Email object.
 *
 * A message that was never snoozed (`snoozedUntil` null) renders neither
 * the banner nor the Cancel button.
 *
 * On-wake banner (work item 3): a message whose `Email.snoozeWokeAt` is
 * set renders a different, cancel-less banner naming the time
 * `snoozeWokeFor` fell due. The two banners are mutually exclusive by
 * wire contract (a woken message carries `snoozedUntil: null`).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import MessageAccordion from './MessageAccordion.svelte';
import type { Email } from './types';

// ── module mocks ──────────────────────────────────────────────────────────────

// t() renders "<key>:<arg1>|<arg2>|..." when called with params, and the
// bare key otherwise, so assertions can check both which key rendered and
// which values were interpolated without hardcoding English/German prose.
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>) => {
    if (!args) return key;
    return `${key}:${Object.values(args).join('|')}`;
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

const { mailMock, INBOX_MBX, WORK_MBX } = vi.hoisted(() => {
  const INBOX_MBX = {
    id: 'mbx-inbox',
    name: 'Inbox',
    role: 'inbox',
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  } as import('./types').Mailbox;

  const WORK_MBX = {
    id: 'mbx-work',
    name: 'Work',
    role: null,
    parentId: null,
    sortOrder: 1,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  } as import('./types').Mailbox;

  const mailMock = {
    mailboxes: new Map([
      ['mbx-inbox', INBOX_MBX],
      ['mbx-work', WORK_MBX],
    ]),
    get customMailboxes(): import('./types').Mailbox[] {
      return [WORK_MBX];
    },
    get inbox(): import('./types').Mailbox | null {
      return INBOX_MBX;
    },
    listFolder: 'inbox' as string,
    identities: new Map(),
    trash: null as import('./types').Mailbox | null,
    setSeen: vi.fn(),
    markUnreadFromHere: vi.fn(),
    deleteEmail: vi.fn(),
    toggleImportant: vi.fn(),
    unsnoozeEmail: vi.fn().mockResolvedValue(undefined),
    restoreFromTrash: vi.fn().mockResolvedValue(undefined),
    toggleReaction: vi.fn(),
    reportSpam: vi.fn(),
    reportPhishing: vi.fn(),
    retryEmailImages: vi.fn(),
  };
  return { mailMock, INBOX_MBX, WORK_MBX };
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

vi.mock('./types', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./types')>();
  return {
    ...actual,
    emailHtmlBody: () => null,
    emailTextBody: () => '',
  };
});

// ── helpers ───────────────────────────────────────────────────────────────────

function makeEmail(overrides: {
  id?: string;
  snoozedUntil?: string | null;
  snoozeWakeMailboxId?: string | null;
  snoozeWokeAt?: string | null;
  snoozeWokeFor?: string | null;
}): Email {
  return {
    id: overrides.id ?? 'e1',
    threadId: 't1',
    blobId: 'root-blob',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    subject: 'Test subject',
    preview: 'preview text',
    receivedAt: '2026-04-30T10:00:00Z',
    hasAttachment: false,
    snoozedUntil: overrides.snoozedUntil ?? null,
    snoozeWakeMailboxId: overrides.snoozeWakeMailboxId ?? null,
    snoozeWokeAt: overrides.snoozeWokeAt ?? null,
    snoozeWokeFor: overrides.snoozeWokeFor ?? null,
    'header:List-ID:asText': null,
    reactions: [],
    htmlBody: [],
    textBody: [],
    attachments: [],
    bodyValues: {},
  } as unknown as Email;
}

function renderAccordion(email: Email, expanded = true) {
  return render(MessageAccordion, {
    props: { email, expanded, onToggle: vi.fn() },
  });
}

// A future ISO instant, computed at test time so the case never sails
// into the past as the suite ages.
const FUTURE_ISO = new Date(Date.now() + 3 * 60 * 60 * 1000).toISOString();

// ── tests ─────────────────────────────────────────────────────────────────────

describe('MessageAccordion: while-snoozed banner (issue #469)', () => {
  beforeEach(() => {
    mailMock.unsnoozeEmail.mockClear();
  });

  it('renders neither banner nor Cancel on a message that was never snoozed', () => {
    const email = makeEmail({ snoozedUntil: null });
    renderAccordion(email);
    expect(screen.queryByText(/^msg\.snooze\.banner:/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'msg.snooze.cancel' })).not.toBeInTheDocument();
  });

  it('renders the plain banner (no destination clause) when no wake mailbox is set', () => {
    const email = makeEmail({ snoozedUntil: FUTURE_ISO, snoozeWakeMailboxId: null });
    renderAccordion(email);
    expect(screen.getByText(/^msg\.snooze\.banner:/)).toBeInTheDocument();
    expect(screen.queryByText(/^msg\.snooze\.bannerToMailbox:/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'msg.snooze.cancel' })).toBeInTheDocument();
  });

  it('renders the plain banner when the wake mailbox is the Inbox itself', () => {
    const email = makeEmail({ snoozedUntil: FUTURE_ISO, snoozeWakeMailboxId: 'mbx-inbox' });
    renderAccordion(email);
    expect(screen.getByText(/^msg\.snooze\.banner:/)).toBeInTheDocument();
    expect(screen.queryByText(/^msg\.snooze\.bannerToMailbox:/)).not.toBeInTheDocument();
  });

  it('names the destination mailbox when the wake target is not the Inbox', () => {
    const email = makeEmail({ snoozedUntil: FUTURE_ISO, snoozeWakeMailboxId: 'mbx-work' });
    renderAccordion(email);
    const banner = screen.getByText(/^msg\.snooze\.bannerToMailbox:/);
    expect(banner).toBeInTheDocument();
    expect(banner.textContent).toContain('Work');
  });

  it('clicking Cancel calls mail.unsnoozeEmail with the message id', async () => {
    const email = makeEmail({ id: 'e-cancel', snoozedUntil: FUTURE_ISO });
    renderAccordion(email);
    await fireEvent.click(screen.getByRole('button', { name: 'msg.snooze.cancel' }));
    expect(mailMock.unsnoozeEmail).toHaveBeenCalledWith('e-cancel');
  });

  it('does not render the banner when the accordion is collapsed', () => {
    const email = makeEmail({ snoozedUntil: FUTURE_ISO });
    renderAccordion(email, false);
    expect(screen.queryByText(/^msg\.snooze\.banner:/)).not.toBeInTheDocument();
  });
});

describe('MessageAccordion: on-wake banner (issue #469, work item 3)', () => {
  it('renders the on-wake banner naming the reminder due time', () => {
    const email = makeEmail({ snoozeWokeAt: '2026-05-01T09:00:00Z', snoozeWokeFor: '2026-05-01T09:00:00Z' });
    renderAccordion(email);
    const banner = screen.getByText(/^msg\.snoozeWoke\.banner:/);
    expect(banner).toBeInTheDocument();
  });

  it('renders neither banner nor Cancel on a message that never had a reminder', () => {
    const email = makeEmail({});
    renderAccordion(email);
    expect(screen.queryByText(/^msg\.snoozeWoke\.banner:/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'msg.snooze.cancel' })).not.toBeInTheDocument();
  });

  it('does not render the on-wake banner while the message is still snoozed', () => {
    // Should not happen per the wire contract (a woken message carries
    // snoozedUntil: null), but the while-snoozed banner still wins if the
    // server ever sends both -- the two are mutually exclusive UI.
    const email = makeEmail({
      snoozedUntil: FUTURE_ISO,
      snoozeWokeAt: '2026-05-01T09:00:00Z',
      snoozeWokeFor: '2026-05-01T09:00:00Z',
    });
    renderAccordion(email);
    expect(screen.getByText(/^msg\.snooze\.banner:/)).toBeInTheDocument();
    expect(screen.queryByText(/^msg\.snoozeWoke\.banner:/)).not.toBeInTheDocument();
  });

  it('does not render the on-wake banner when the accordion is collapsed', () => {
    const email = makeEmail({ snoozeWokeAt: '2026-05-01T09:00:00Z', snoozeWokeFor: '2026-05-01T09:00:00Z' });
    renderAccordion(email, false);
    expect(screen.queryByText(/^msg\.snoozeWoke\.banner:/)).not.toBeInTheDocument();
  });

  it('keeps showing the on-wake banner for this viewing session even after the store clears the props', async () => {
    // The reader's own auto-read effect marks $seen the instant an unread
    // message's accordion mounts expanded -- the common way a just-woken
    // message gets opened -- and the store's setSeen optimistically clears
    // snoozeWokeAt/snoozeWokeFor in that same tick (issue #469's wire
    // contract: gaining $seen ends the indication). A banner read live off
    // `email` would flip to hidden before a human could ever see it. The
    // banner is snapshotted at mount instead, so it survives the `email`
    // prop being replaced by a fresher, already-cleared object -- exactly
    // what happens when the store's own change lands.
    const email = makeEmail({
      id: 'e-viewing',
      snoozeWokeAt: '2026-05-01T09:00:00Z',
      snoozeWokeFor: '2026-05-01T09:00:00Z',
    });
    const { rerender } = renderAccordion(email);
    expect(screen.getByText(/^msg\.snoozeWoke\.banner:/)).toBeInTheDocument();

    const clearedEmail = makeEmail({ id: 'e-viewing', snoozeWokeAt: null, snoozeWokeFor: null });
    await rerender({ email: clearedEmail, expanded: true, onToggle: vi.fn() });

    expect(screen.getByText(/^msg\.snoozeWoke\.banner:/)).toBeInTheDocument();
  });
});
