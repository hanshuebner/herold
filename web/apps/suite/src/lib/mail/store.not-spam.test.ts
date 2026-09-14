/**
 * Unit tests for mail.notSpam (issue #382, REQ-FILT-02a / REQ-FLT-16).
 *
 * notSpam is the reverse of reportSpam: it moves a Junk-mailbox email to
 * Inbox and clears the $junk / $phishing keywords via the same Email/set
 * path every other mailbox move (archive, restore, block-sender) already
 * uses. Mirrors the mocking pattern in store.mark-thread-seen-dedup.test.ts.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email, Mailbox } from './types';

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), hasCapability: vi.fn(() => true) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
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

const toastShow = vi.fn();
vi.mock('../toast/toast.svelte', () => ({ toast: { show: toastShow } }));
vi.mock('../router/router.svelte', () => ({
  router: { parts: [], matches: vi.fn(() => false), navigate: vi.fn(), getParam: vi.fn(() => null) },
}));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/notifications.svelte', () => ({ notifications: { add: vi.fn() } }));
vi.mock('../compose/compose.svelte', () => ({
  compose: { openReply: vi.fn(), openReplyAll: vi.fn(), openForward: vi.fn(), openDraft: vi.fn() },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  i18n: { t: (key: string) => key },
  localeTag: () => 'en',
}));

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: { 'mbx-junk': true },
    keywords: { $junk: true },
    from: [{ name: 'Notify', email: 'notify@accountprotection.microsoft.com' }],
    to: null,
    subject: 'Password reset',
    preview: '',
    receivedAt: '2026-09-01T10:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    ...overrides,
  };
}

function makeMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'role'>): Mailbox {
  return {
    name: overrides.role ?? overrides.id,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...overrides,
  };
}

function invocation(name: string, args: unknown, callId = 'c0'): [string, unknown, string] {
  return [name, args, callId];
}

describe('mail.notSpam (issue #382)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    jmapMod = await import('../jmap/client');
    mailMod = await import('./store.svelte');

    const { mail } = mailMod;
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
      ['mbx-junk', makeMailbox({ id: 'mbx-junk', role: 'junk' })],
    ]);
  });

  it('moves the email to Inbox, clears $junk, and returns true on success', async () => {
    const { mail } = mailMod;
    mail.emails.set('e-1', makeEmail({ id: 'e-1', threadId: 't-1' }));
    mail.listEmailIds = ['e-1'];

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [invocation('Email/set', { newState: 's2', updated: { 'e-1': {} } })],
    });

    const ok = await mail.notSpam('e-1');
    expect(ok).toBe(true);

    // First jmap.batch call is the Email/set; a second, unmocked call may
    // follow from the store's post-mutation mailbox-count refresh.
    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentUpdate: unknown;
    builder({
      call: (_name: string, args: { update: unknown }) => {
        sentUpdate = args.update;
        return { ref: () => ({}) };
      },
    });
    expect(sentUpdate).toEqual({
      'e-1': { 'keywords/$junk': null, mailboxIds: { 'mbx-inbox': true } },
    });

    const patched = mail.emails.get('e-1');
    expect(patched.mailboxIds).toEqual({ 'mbx-inbox': true });
    expect(patched.keywords.$junk).toBeUndefined();
    expect(mail.listEmailIds).toEqual([]);
  });

  it('also clears $phishing when the message carried it', async () => {
    const { mail } = mailMod;
    mail.emails.set(
      'e-2',
      makeEmail({ id: 'e-2', threadId: 't-2', keywords: { $junk: true, $phishing: true } }),
    );
    mail.listEmailIds = ['e-2'];

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [invocation('Email/set', { newState: 's3', updated: { 'e-2': {} } })],
    });

    await mail.notSpam('e-2');

    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentUpdate: unknown;
    builder({
      call: (_name: string, args: { update: unknown }) => {
        sentUpdate = args.update;
        return { ref: () => ({}) };
      },
    });
    expect(sentUpdate).toEqual({
      'e-2': {
        'keywords/$junk': null,
        'keywords/$phishing': null,
        mailboxIds: { 'mbx-inbox': true },
      },
    });
  });

  it('reverts the optimistic patch and returns false on server failure', async () => {
    const { mail } = mailMod;
    const original = makeEmail({ id: 'e-3', threadId: 't-3' });
    mail.emails.set('e-3', original);
    mail.listEmailIds = ['e-3'];

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/set', {
          newState: 's4',
          notUpdated: { 'e-3': { type: 'forbidden' } },
        }),
      ],
    });

    const ok = await mail.notSpam('e-3');
    expect(ok).toBe(false);

    const reverted = mail.emails.get('e-3');
    expect(reverted.mailboxIds).toEqual(original.mailboxIds);
    expect(reverted.keywords).toEqual(original.keywords);
    expect(mail.listEmailIds).toEqual(['e-3']);
    expect(toastShow).toHaveBeenCalled();
  });

  it('returns false for an unknown email id without calling the server', async () => {
    const { mail } = mailMod;
    const ok = await mail.notSpam('does-not-exist');
    expect(ok).toBe(false);
    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
  });
});
