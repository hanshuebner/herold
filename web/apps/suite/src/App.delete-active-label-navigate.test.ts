/**
 * Regression test for issue #201: deleting the currently-viewed label
 * strands the SPA on a "not found" folder route.
 *
 * Root cause: destroyMailbox() in lib/mail/store.svelte.ts removed the
 * mailbox from the `mailboxes` map and, when the deleted id was the active
 * `listFolder`, reset the store's internal state via loadFolder('inbox') --
 * but never called router.navigate(). MailView.svelte derives the rendered
 * folder from the URL param, validating custom-mailbox ids against
 * `mail.mailboxes.has(id)`; since the URL still pointed at the deleted id,
 * it resolved to undefined and rendered the not-found view even though
 * `mail.listFolder` had already been reset internally.
 *
 * Fix: destroyMailbox() now calls router.navigate('/mail') alongside
 * loadFolder('inbox') whenever the deleted mailbox is the one the URL is
 * currently routed to. Deleting a mailbox that is not the active route must
 * not move the router at all.
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

const navigateMock = vi.fn();
vi.mock('./lib/router/router.svelte', () => ({
  router: {
    parts: [],
    matches: vi.fn(() => false),
    navigate: navigateMock,
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

function testMailbox(id: string, name: string): Mailbox {
  return {
    id,
    name,
    role: null,
    parentId: null,
    sortOrder: 100,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  };
}

function mockSuccessfulDestroy(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  jmap: { batch: (...args: any[]) => any },
  destroyedId: string,
): void {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  vi.mocked(jmap.batch).mockImplementation(async (builder: any) => {
    builder({
      call: (_name: string, _args: Record<string, unknown>) => ({ ref: () => null }),
    });
    return {
      responses: [['Mailbox/set', { destroyed: [destroyedId], notDestroyed: null }, 'c0']],
      sessionState: 'state-2',
    };
  });
}

describe('mail.destroyMailbox navigates off the deleted active folder (issue #201)', () => {
  let mailMod: typeof import('./lib/mail/store.svelte');
  let jmapMod: typeof import('./lib/jmap/client');

  beforeEach(async () => {
    vi.clearAllMocks();
    mailMod = await import('./lib/mail/store.svelte');
    jmapMod = await import('./lib/jmap/client');
  });

  it('navigates to /mail when the deleted mailbox is the active listFolder', async () => {
    const { mail } = mailMod;
    const { jmap } = jmapMod;

    mail.mailboxes = new Map([['mbox-1', testMailbox('mbox-1', 'test')]]);
    // Simulate the user currently viewing this label (the URL had already
    // resolved to it, so loadFolder set listFolder to its id).
    mail.listFolder = 'mbox-1';

    mockSuccessfulDestroy(jmap, 'mbox-1');

    const result = await mail.destroyMailbox('mbox-1', 'destroy');

    expect(result).toBe(true);
    expect(navigateMock).toHaveBeenCalledWith('/mail');
  });

  it('does not navigate when the deleted mailbox is not the active listFolder', async () => {
    const { mail } = mailMod;
    const { jmap } = jmapMod;

    mail.mailboxes = new Map([
      ['mbox-1', testMailbox('mbox-1', 'test')],
      ['mbox-2', testMailbox('mbox-2', 'other')],
    ]);
    // User is viewing a different label than the one being deleted.
    mail.listFolder = 'mbox-2';

    mockSuccessfulDestroy(jmap, 'mbox-1');

    const result = await mail.destroyMailbox('mbox-1', 'destroy');

    expect(result).toBe(true);
    expect(navigateMock).not.toHaveBeenCalled();
    expect(mail.listFolder).toBe('mbox-2');
  });
});
