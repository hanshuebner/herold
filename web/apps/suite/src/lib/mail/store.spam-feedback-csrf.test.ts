/**
 * Unit tests for the REQ-FILT-70 spam-feedback POST's CSRF header (issue
 * #391). `#postSpamFeedback` (invoked by both reportSpam and notSpam) must
 * send X-CSRF-Token from the herold_public_csrf cookie -- cookie-authenticated
 * mutating POSTs on the public listener are rejected with 403 csrf_required
 * otherwise, so no audit record is ever written -- and must log a non-2xx
 * response through the debug ring rather than swallowing it silently.
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

const appendEventMock = vi.fn();
vi.mock('../debug-ring/debug-ring', () => ({ appendEvent: appendEventMock }));

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: { 'mbx-inbox': true },
    keywords: {},
    from: [{ name: 'Sender', email: 'sender@example.com' }],
    to: null,
    subject: 'Hello',
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

describe('mail #postSpamFeedback CSRF header (issue #391)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    document.cookie = 'herold_public_csrf=test-csrf-token; path=/';
    jmapMod = await import('../jmap/client');
    mailMod = await import('./store.svelte');

    const { mail } = mailMod;
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
      ['mbx-junk', makeMailbox({ id: 'mbx-junk', role: 'junk' })],
    ]);
  });

  it('reportSpam posts /api/v1/spam-feedback with X-CSRF-Token from the cookie', async () => {
    const { mail } = mailMod;
    mail.emails.set('e-1', makeEmail({ id: 'e-1', threadId: 't-1' }));
    mail.listEmailIds = ['e-1'];

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [invocation('Email/set', { newState: 's2', updated: { 'e-1': {} } })],
    });

    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    try {
      await mail.reportSpam('e-1', 'spam');
      await vi.waitFor(() => {
        expect(fetchMock).toHaveBeenCalledWith(
          '/api/v1/spam-feedback',
          expect.objectContaining({
            method: 'POST',
            headers: expect.objectContaining({ 'X-CSRF-Token': 'test-csrf-token' }),
            body: JSON.stringify({ emailId: 'e-1', kind: 'spam' }),
          }),
        );
      });
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('notSpam posts /api/v1/spam-feedback with X-CSRF-Token from the cookie', async () => {
    const { mail } = mailMod;
    mail.emails.set('e-2', makeEmail({ id: 'e-2', threadId: 't-2', mailboxIds: { 'mbx-junk': true }, keywords: { $junk: true } }));
    mail.listEmailIds = ['e-2'];

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [invocation('Email/set', { newState: 's3', updated: { 'e-2': {} } })],
    });

    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    try {
      await mail.notSpam('e-2');
      await vi.waitFor(() => {
        expect(fetchMock).toHaveBeenCalledWith(
          '/api/v1/spam-feedback',
          expect.objectContaining({
            method: 'POST',
            headers: expect.objectContaining({ 'X-CSRF-Token': 'test-csrf-token' }),
            body: JSON.stringify({ emailId: 'e-2', kind: 'ham' }),
          }),
        );
      });
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('logs a non-2xx spam-feedback response through the debug ring instead of swallowing it', async () => {
    const { mail } = mailMod;
    mail.emails.set('e-3', makeEmail({ id: 'e-3', threadId: 't-3' }));
    mail.listEmailIds = ['e-3'];

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [invocation('Email/set', { newState: 's4', updated: { 'e-3': {} } })],
    });

    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 403 }));
    vi.stubGlobal('fetch', fetchMock);
    try {
      await mail.reportSpam('e-3', 'spam');
      await vi.waitFor(() => {
        expect(appendEventMock).toHaveBeenCalledWith(
          'page',
          'error',
          expect.stringContaining('spam-feedback'),
          expect.objectContaining({ status: 403 }),
        );
      });
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
