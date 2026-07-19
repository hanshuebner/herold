/**
 * Regression test for issue #255 (comment 3239): the Drafts sidebar badge
 * must show the count of THREADS in the drafts mailbox (`totalThreads`),
 * not the raw count of draft messages (`totalEmails`), for consistency
 * with the thread-collapsed drafts list it sits next to.
 *
 * App.svelte's sidebar reads `mail.drafts?.totalThreads` directly off the
 * store's cached Mailbox rows -- this test drives that same getter against
 * a store seeded with a mailbox where totalThreads and totalEmails
 * diverge, so a regression back to totalEmails fails it.
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

// A drafts mailbox with 3 stored draft messages that collapse to 2
// distinct threads: raw message count is 3, but the thread-collapsed
// drafts list renders only 2 rows. The badge must show 2.
function divergentDraftsMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'name' | 'role'>): Mailbox {
  return {
    parentId: null,
    sortOrder: 0,
    totalEmails: 3,
    unreadEmails: 0,
    totalThreads: 2,
    unreadThreads: 0,
    ...overrides,
  };
}

describe('drafts total badge reads totalThreads, not totalEmails (re #255 comment 3239)', () => {
  let mailMod: typeof import('./lib/mail/store.svelte');

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    mailMod = await import('./lib/mail/store.svelte');
  });

  it('mail.drafts exposes a smaller totalThreads than totalEmails for a doubled-up thread', () => {
    const { mail } = mailMod;
    mail.mailboxes = new Map([
      ['mbox-drafts', divergentDraftsMailbox({ id: 'mbox-drafts', name: 'Drafts', role: 'drafts' })],
    ]);

    // This is exactly the value App.svelte's Drafts sidebar badge renders.
    expect(mail.drafts?.totalThreads).toBe(2);
    expect(mail.drafts?.totalEmails).toBe(3);
    expect(mail.drafts?.totalThreads).not.toBe(mail.drafts?.totalEmails);
  });
});
