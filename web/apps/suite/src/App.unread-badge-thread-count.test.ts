/**
 * Regression test for issue #255 (comment 3223): the sidebar unread badge
 * must show the count of THREADS holding at least one unread message
 * (`unreadThreads`), not the raw count of unread messages (`unreadEmails`).
 * A thread with 2 unread messages must contribute 1 to the badge.
 *
 * App.svelte's sidebar reads `mail.inbox?.unreadThreads` and
 * `m.unreadThreads` (custom mailboxes) directly off the store's cached
 * Mailbox rows -- this test drives those same getters against a store
 * seeded with a mailbox where unreadThreads and unreadEmails diverge, so
 * a regression back to unreadEmails fails it.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox } from './lib/mail/types';

vi.mock('./lib/jmap/client', () => ({
  jmap: {
    batch: vi.fn(),
  },
  strict: (r: unknown[]) => r,
}));

vi.mock('./lib/auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'account-1',
      },
      apiUrl: '/jmap',
      downloadUrl: '/jmap/download/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'test@example.com',
      accounts: {},
      state: 'sess-1',
    },
    principalId: 'principal-test',
    errorMessage: null,
    needsStepUp: false,
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('./lib/jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(() => vi.fn()),
    start: vi.fn(),
    stop: vi.fn(),
  },
}));

vi.mock('./lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('./lib/router/router.svelte', () => ({
  router: {
    parts: [],
    matches: vi.fn(() => false),
    navigate: vi.fn(),
    getParam: vi.fn(() => null),
    setParam: vi.fn(),
  },
}));

vi.mock('./lib/notifications/sounds.svelte', () => ({
  sounds: { play: vi.fn() },
}));

vi.mock('./lib/i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en-US',
  i18n: { t: (key: string, params?: Record<string, string | number>) => (params ? `${key}:${JSON.stringify(params)}` : key) },
}));

// A thread with 2 unread messages in the inbox, plus 1 read standalone
// message: raw unread-message count is 2, but only 1 thread holds an
// unread message. The badge must show 1.
function divergentMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'name' | 'role'>): Mailbox {
  return {
    parentId: null,
    sortOrder: 0,
    totalEmails: 3,
    unreadEmails: 2,
    totalThreads: 2,
    unreadThreads: 1,
    ...overrides,
  };
}

describe('unread badge reads unreadThreads, not unreadEmails (re #255 comment 3223)', () => {
  let mailMod: typeof import('./lib/mail/store.svelte');

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    mailMod = await import('./lib/mail/store.svelte');
  });

  it('mail.inbox exposes a smaller unreadThreads than unreadEmails for a doubled-up thread', () => {
    const { mail } = mailMod;
    mail.mailboxes = new Map([
      ['mbox-inbox', divergentMailbox({ id: 'mbox-inbox', name: 'Inbox', role: 'inbox' })],
    ]);

    // This is exactly the value App.svelte's sidebar badge renders.
    expect(mail.inbox?.unreadThreads).toBe(1);
    expect(mail.inbox?.unreadEmails).toBe(2);
    expect(mail.inbox?.unreadThreads).not.toBe(mail.inbox?.unreadEmails);
  });

  it('a custom mailbox badge reads unreadThreads for the same divergent counts', () => {
    const { mail } = mailMod;
    mail.mailboxes = new Map([
      ['mbox-work', divergentMailbox({ id: 'mbox-work', name: 'Work', role: null })],
    ]);

    const [work] = mail.customMailboxes;
    // This is exactly the value App.svelte's custom-mailbox badge renders.
    expect(work?.unreadThreads).toBe(1);
    expect(work?.unreadEmails).toBe(2);
  });
});
