/**
 * Unit tests for ThreadReader label badges (re #66, re #70).
 *
 * Label badges are rendered in the thread-level header (under the subject)
 * as the union of custom-mailbox memberships across all messages in the
 * thread. They are always visible regardless of which messages are expanded
 * or collapsed.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ThreadReader from './ThreadReader.svelte';

// ── shared hoisted fixtures ────────────────────────────────────────────────────

const { mailMock, WORK_MBX, PERSONAL_MBX } = vi.hoisted(() => {
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

  const PERSONAL_MBX = {
    id: 'mbx-personal',
    name: 'Personal',
    role: null,
    parentId: null,
    sortOrder: 1,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  } as import('./types').Mailbox;

  // Thread data for tid-1: two emails.
  const EMAIL_A = {
    id: 'e-a',
    threadId: 'tid-1',
    mailboxIds: { 'mbx-work': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    subject: 'Test thread',
    preview: 'first message',
    receivedAt: '2026-04-01T10:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
  };

  const EMAIL_B = {
    id: 'e-b',
    threadId: 'tid-1',
    mailboxIds: { 'mbx-personal': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Bob', email: 'bob@example.test' }],
    to: null,
    cc: null,
    subject: 'Test thread',
    preview: 'second message',
    receivedAt: '2026-04-01T11:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
  };

  const mailMock = {
    mailboxes: new Map([
      ['mbx-work', WORK_MBX],
      ['mbx-personal', PERSONAL_MBX],
    ]),
    emails: new Map([
      ['e-a', EMAIL_A],
      ['e-b', EMAIL_B],
    ]),
    threads: new Map([
      ['tid-1', { id: 'tid-1', emailIds: ['e-a', 'e-b'] }],
    ]),
    identities: new Map(),
    listFolder: 'inbox' as string,
    get customMailboxes() {
      return [WORK_MBX, PERSONAL_MBX];
    },
    threadStatus: (tid: string) => (tid === 'tid-1' ? 'ready' : 'idle'),
    threadError: () => null,
    threadEmails: (tid: string) => {
      if (tid !== 'tid-1') return [];
      return [EMAIL_A, EMAIL_B];
    },
    loadThread: vi.fn().mockResolvedValue(undefined),
  };

  return { mailMock, WORK_MBX, PERSONAL_MBX };
});

// ── module mocks ───────────────────────────────────────────────────────────────

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));
vi.mock('../router/router.svelte', () => ({
  router: { navigate: vi.fn() },
}));
vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn().mockReturnValue(() => undefined) },
}));
// Stub child Svelte components: must be functions in Svelte 5.
vi.mock('./ThreadToolbar.svelte', () => ({ default: () => null }));
vi.mock('./ThreadReplyBar.svelte', () => ({ default: () => null }));
vi.mock('./MessageAccordion.svelte', () => ({ default: () => null }));

// ── helpers ────────────────────────────────────────────────────────────────────

function renderReader() {
  return render(ThreadReader, { props: { threadId: 'tid-1' } });
}

// ── tests ──────────────────────────────────────────────────────────────────────

describe('ThreadReader: label badges in thread header (re #66, re #70)', () => {
  beforeEach(() => {
    mailMock.listFolder = 'inbox';
  });

  it('renders label badges for custom mailboxes belonging to any thread email', () => {
    renderReader();
    // EMAIL_A is in Work, EMAIL_B is in Personal.
    // Both should appear as thread-level badges.
    const workBadge = screen.getByText('Work');
    const personalBadge = screen.getByText('Personal');
    expect(workBadge).toBeInTheDocument();
    expect(personalBadge).toBeInTheDocument();
    expect(workBadge.classList.contains('label-badge')).toBe(true);
    expect(personalBadge.classList.contains('label-badge')).toBe(true);
  });

  it('renders no badge when no thread email belongs to any custom mailbox', () => {
    const originalEmails = mailMock.emails;
    const originalThreadEmails = mailMock.threadEmails;
    // Override to return emails with no custom-mailbox membership.
    const EMAIL_NONE = {
      id: 'e-none',
      threadId: 'tid-1',
      mailboxIds: {} as Record<string, true>,
      keywords: { $seen: true } as Record<string, true | undefined>,
      from: [{ name: 'Carol', email: 'carol@example.test' }],
      to: null,
      cc: null,
      subject: 'No label',
      preview: 'no labels',
      receivedAt: '2026-04-01T12:00:00Z',
      hasAttachment: false,
      attachments: [],
      reactions: null,
      snoozedUntil: null,
      'header:List-ID:asText': null,
    };
    mailMock.emails = new Map([['e-none', EMAIL_NONE]]);
    (mailMock as { threadEmails: (tid: string) => unknown[] }).threadEmails = (tid: string) =>
      tid === 'tid-1' ? [EMAIL_NONE] : [];

    renderReader();
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();

    // Restore.
    mailMock.emails = originalEmails;
    (mailMock as { threadEmails: (tid: string) => unknown[] }).threadEmails = originalThreadEmails;
  });

  it('suppresses the badge for the currently-viewed folder', () => {
    // When listFolder matches the custom mailbox id, that badge is omitted.
    mailMock.listFolder = WORK_MBX.id;
    renderReader();
    // Work badge suppressed; Personal still shows.
    expect(screen.queryByText('Work')).not.toBeInTheDocument();
    const personalBadge = screen.getByText('Personal');
    expect(personalBadge).toBeInTheDocument();
  });

  it('renders no Labels landmark when the thread labels list is empty', () => {
    // Simulate all thread emails being in the currently-viewed folder only.
    const originalListFolder = mailMock.listFolder;
    const originalEmails = mailMock.emails;
    const originalThreadEmails = mailMock.threadEmails;

    const EMAIL_ONLY_ACTIVE = {
      id: 'e-active',
      threadId: 'tid-1',
      mailboxIds: { 'mbx-work': true } as Record<string, true>,
      keywords: { $seen: true } as Record<string, true | undefined>,
      from: [{ name: 'Dan', email: 'dan@example.test' }],
      to: null,
      cc: null,
      subject: 'Active folder only',
      preview: 'in active folder',
      receivedAt: '2026-04-01T13:00:00Z',
      hasAttachment: false,
      attachments: [],
      reactions: null,
      snoozedUntil: null,
      'header:List-ID:asText': null,
    };
    mailMock.listFolder = WORK_MBX.id; // active folder = Work
    mailMock.emails = new Map([['e-active', EMAIL_ONLY_ACTIVE]]);
    (mailMock as { threadEmails: (tid: string) => unknown[] }).threadEmails = (tid: string) =>
      tid === 'tid-1' ? [EMAIL_ONLY_ACTIVE] : [];

    renderReader();
    expect(screen.queryByLabelText('Labels')).not.toBeInTheDocument();

    // Restore.
    mailMock.listFolder = originalListFolder;
    mailMock.emails = originalEmails;
    (mailMock as { threadEmails: (tid: string) => unknown[] }).threadEmails = originalThreadEmails;
  });
});
