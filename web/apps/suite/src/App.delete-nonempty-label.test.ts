/**
 * Regression test for issue #65: deleting a non-empty label yields a server
 * error instead of succeeding.
 *
 * Root cause: confirmDestroyMailbox in App.svelte called mail.destroyMailbox(id)
 * with no second argument, which defaulted to 'removeOnly'.  That maps to
 * onDestroyRemoveEmails: false in the JMAP Mailbox/set call.  Per RFC 8621
 * section 2.5 the server rejects that request with error type "mailboxHasEmail"
 * whenever the mailbox contains any messages.
 *
 * Fix: App.svelte now calls mail.destroyMailbox(id, 'destroy') which sends
 * onDestroyRemoveEmails: true, consistent with the warning text already shown
 * to the user before confirmation.
 *
 * This test drives the mail store's destroyMailbox() method directly, verifying
 * that the 'destroy' mode sets onDestroyRemoveEmails: true in the JMAP request
 * and that the call succeeds when the server acknowledges the destroy.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox } from './lib/mail/types';

// ── Module mocks ─────────────────────────────────────────────────────────────
// Declared before any dynamic imports so vitest hoists them correctly.

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
}));

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('mail.destroyMailbox (issue #65)', () => {
  let mailMod: typeof import('./lib/mail/store.svelte');
  let jmapMod: typeof import('./lib/jmap/client');

  beforeEach(async () => {
    vi.clearAllMocks();
    mailMod = await import('./lib/mail/store.svelte');
    jmapMod = await import('./lib/jmap/client');

    // Seed the store with a custom mailbox so destroyMailbox finds it.
    const testMailbox: Mailbox = {
      id: 'mbox-1',
      name: 'Work',
      role: null,
      parentId: null,
      sortOrder: 100,
      totalEmails: 5,
      unreadEmails: 2,
      totalThreads: 3,
      unreadThreads: 1,
    };
    mailMod.mail.mailboxes = new Map([['mbox-1', testMailbox]]);
  });

  it("'destroy' mode sends onDestroyRemoveEmails: true so non-empty label deletion succeeds", async () => {
    const { mail } = mailMod;
    const { jmap } = jmapMod;

    // Capture the args passed to b.call() inside jmap.batch().
    const capturedArgs: Record<string, unknown>[] = [];

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    vi.mocked(jmap.batch).mockImplementation(async (builder: any) => {
      builder({
        call: (_name: string, args: Record<string, unknown>) => {
          capturedArgs.push(args);
          return { ref: () => null };
        },
      });
      // Server responds with a successful destroy.
      return {
        responses: [['Mailbox/set', { destroyed: ['mbox-1'], notDestroyed: null }, 'c0']],
        sessionState: 'state-2',
      };
    });

    const result = await mail.destroyMailbox('mbox-1', 'destroy');

    expect(result).toBe(true);
    expect(capturedArgs).toHaveLength(1);
    expect(capturedArgs[0]!['onDestroyRemoveEmails']).toBe(true);
  });

  it("'removeOnly' mode sends onDestroyRemoveEmails: false (server will reject non-empty mailboxes)", async () => {
    const { mail } = mailMod;
    const { jmap } = jmapMod;

    const capturedArgs: Record<string, unknown>[] = [];

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    vi.mocked(jmap.batch).mockImplementation(async (builder: any) => {
      builder({
        call: (_name: string, args: Record<string, unknown>) => {
          capturedArgs.push(args);
          return { ref: () => null };
        },
      });
      return {
        responses: [['Mailbox/set', { destroyed: ['mbox-1'], notDestroyed: null }, 'c0']],
        sessionState: 'state-2',
      };
    });

    await mail.destroyMailbox('mbox-1', 'removeOnly');

    expect(capturedArgs).toHaveLength(1);
    expect(capturedArgs[0]!['onDestroyRemoveEmails']).toBe(false);
  });
});
