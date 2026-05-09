/**
 * REQ-EXTIMG-BG-31 / REQ-EXTIMG-BG-65 — non-modal banner above the thread
 * body when any email in the rendered thread has `internalizePending=true`.
 *
 * The body itself already carries placeholder data URIs in place of
 * external images (the backend rewrites them in renderFullWithProperties
 * — REQ-EXTIMG-BG-10..13). The SPA only needs to surface the notice; no
 * client-side image rewriting happens here.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ThreadReader from './ThreadReader.svelte';

const { mailMock } = vi.hoisted(() => {
  const EMAIL_PENDING = {
    id: 'e-pending',
    threadId: 'tid-pending',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    subject: 'Pending thread',
    preview: 'pending body',
    receivedAt: '2026-05-09T10:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
    internalizePending: true,
  };

  const EMAIL_DONE = {
    id: 'e-done',
    threadId: 'tid-done',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Carol', email: 'carol@example.test' }],
    to: null,
    cc: null,
    subject: 'Done thread',
    preview: 'done body',
    receivedAt: '2026-05-09T11:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
    internalizePending: false,
  };

  const mailMock = {
    mailboxes: new Map<string, import('./types').Mailbox>(),
    emails: new Map([
      [EMAIL_PENDING.id, EMAIL_PENDING],
      [EMAIL_DONE.id, EMAIL_DONE],
    ]),
    threads: new Map([
      ['tid-pending', { id: 'tid-pending', emailIds: ['e-pending'] }],
      ['tid-done', { id: 'tid-done', emailIds: ['e-done'] }],
    ]),
    identities: new Map(),
    listFolder: 'inbox' as string,
    get customMailboxes() {
      return [] as import('./types').Mailbox[];
    },
    threadStatus: (tid: string) =>
      tid === 'tid-pending' || tid === 'tid-done' ? 'ready' : 'idle',
    threadError: () => null,
    threadEmails: (tid: string) => {
      if (tid === 'tid-pending') return [EMAIL_PENDING];
      if (tid === 'tid-done') return [EMAIL_DONE];
      return [];
    },
    loadThread: vi.fn().mockResolvedValue(undefined),
  };

  return { mailMock };
});

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'mail.threadReader.internalizePending.heading':
        'Images are being processed',
      'mail.threadReader.internalizePending.body':
        'External images in this message are still being internalized in the background. They appear as placeholders for now; refresh in a moment to see them.',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
}));
vi.mock('../router/router.svelte', () => ({
  router: { navigate: vi.fn() },
}));
vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn().mockReturnValue(() => undefined) },
}));
vi.mock('./ThreadToolbar.svelte', () => ({ default: () => null }));
vi.mock('./ThreadReplyBar.svelte', () => ({ default: () => null }));
vi.mock('./MessageAccordion.svelte', () => ({ default: () => null }));

describe('ThreadReader internalize-pending banner (REQ-EXTIMG-BG-31)', () => {
  beforeEach(() => {
    mailMock.listFolder = 'inbox';
  });

  it('renders the banner when any email in the thread is pending', () => {
    render(ThreadReader, { props: { threadId: 'tid-pending' } });
    const heading = screen.getByText('Images are being processed');
    expect(heading).toBeInTheDocument();
    // The banner must be a non-modal status region for screen readers.
    const banner = heading.closest('.internalize-pending-banner');
    expect(banner).not.toBeNull();
    expect(banner?.getAttribute('role')).toBe('status');
  });

  it('does not render the banner when no email is pending', () => {
    render(ThreadReader, { props: { threadId: 'tid-done' } });
    expect(
      screen.queryByText('Images are being processed'),
    ).not.toBeInTheDocument();
  });
});
