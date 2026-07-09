/**
 * Coverage for `MailStore.retryEmailImages` (issue #162, REQ-EXTIMG-71/73).
 *
 * Calls `Email/retryImages` under the
 * `https://netzhansa.com/jmap/email-image-retry` capability, patches the
 * cached email's `failedImageCount` from the response, and -- only when
 * `retriedCount > 0` -- re-fetches the message body so the newly-
 * internalized images render. The origin URL never appears anywhere in
 * this path; the store only ever sees the counts the server returns.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox, Email } from './types';

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), hasCapability: vi.fn(() => true) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: {
        'urn:ietf:params:jmap:mail': {},
        'https://netzhansa.com/jmap/email-image-retry': {},
      },
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-1' },
      apiUrl: '/jmap',
      downloadUrl: '/jmap/dl/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'alice@example.local',
      accounts: {},
      state: 'sess-1',
    },
    principalId: 'principal-alice',
    errorMessage: null,
    needsStepUp: false,
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()), start: vi.fn(), stop: vi.fn() },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../router/router.svelte', () => ({
  router: {
    parts: [],
    matches: vi.fn(() => false),
    navigate: vi.fn(),
    getParam: vi.fn(() => null),
    setParam: vi.fn(),
  },
}));

vi.mock('../notifications/sounds.svelte', () => ({
  sounds: { play: vi.fn() },
}));

vi.mock('../notifications/notifications.svelte', () => ({
  notifications: { add: vi.fn() },
}));

vi.mock('../compose/compose.svelte', () => ({
  compose: {
    openReply: vi.fn(),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
    openDraft: vi.fn(),
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en-US',
}));

function makeMailbox(
  overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'name' | 'role'>,
): Mailbox {
  return {
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...overrides,
  };
}

function makeEmail(id: string, failedImageCount: number): Email {
  return {
    id,
    threadId: `thread-${id}`,
    mailboxIds: { 'mbx-inbox': true },
    keywords: {},
    receivedAt: '2026-01-01T12:00:00Z',
    blobId: `blob-${id}`,
    hasAttachment: false,
    preview: 'preview',
    subject: null,
    from: [],
    to: [],
    failedImageCount,
  };
}

function invocation(name: string, args: unknown, callId = 'c0'): [string, unknown, string] {
  return [name, args, callId];
}

describe('MailStore.retryEmailImages (issue #162)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    jmapMod = await import('../jmap/client');
    vi.mocked(jmapMod.jmap.hasCapability).mockReturnValue(true);
    mailMod = await import('./store.svelte');
    const { mail } = mailMod;
    mail.mailboxes = new Map([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', name: 'Inbox', role: 'inbox' })],
    ]);
    mail.emails.set('e1', makeEmail('e1', 3));
  });

  it('calls Email/retryImages with accountId + id under the image-retry capability', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch).mockImplementationOnce(async (builder: (b: unknown) => void) => {
      const calls: Array<[string, unknown]> = [];
      builder({
        call: (name: string, args: unknown) => {
          calls.push([name, args]);
          return { ref: () => ({}) };
        },
        usingSet: () => new Set(),
      });
      expect(calls[0]?.[0]).toBe('Email/retryImages');
      expect(calls[0]?.[1]).toEqual({ accountId: 'acct-1', id: 'e1' });
      return {
        responses: [
          invocation('Email/retryImages', {
            accountId: 'acct-1',
            id: 'e1',
            retriedCount: 0,
            failedImageCount: 3,
            newState: 's1',
          }),
        ],
      };
    });

    const result = await mail.retryEmailImages('e1');
    expect(result).toEqual({ retriedCount: 0, failedImageCount: 3 });
    expect(mail.emails.get('e1')?.failedImageCount).toBe(3);
  });

  it('patches failedImageCount to 0 and refetches the body on a successful retry', async () => {
    const { mail } = mailMod;
    vi.mocked(jmapMod.jmap.batch)
      .mockResolvedValueOnce({
        responses: [
          invocation('Email/retryImages', {
            accountId: 'acct-1',
            id: 'e1',
            retriedCount: 3,
            failedImageCount: 0,
            newState: 's2',
          }),
        ],
      })
      .mockResolvedValueOnce({
        responses: [
          invocation('Email/get', {
            list: [{ ...makeEmail('e1', 0), htmlBody: [] }],
            state: 's2',
          }),
        ],
      });

    const result = await mail.retryEmailImages('e1');
    expect(result).toEqual({ retriedCount: 3, failedImageCount: 0 });
    expect(mail.emails.get('e1')?.failedImageCount).toBe(0);
    // The body refetch (loadDraftBody) is the second batch call.
    expect(jmapMod.jmap.batch).toHaveBeenCalledTimes(2);
  });

  it('does not call Email/retryImages when the capability is absent', async () => {
    vi.mocked(jmapMod.jmap.hasCapability).mockReturnValue(false);
    const { mail } = mailMod;
    const toastMod = await import('../toast/toast.svelte');

    const result = await mail.retryEmailImages('e1');

    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
    expect(result).toEqual({ retriedCount: 0, failedImageCount: 3 });
    expect(toastMod.toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });
});
