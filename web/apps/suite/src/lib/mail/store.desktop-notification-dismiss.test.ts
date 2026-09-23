/**
 * Issue #481: a page-created desktop notification (`#fireDesktopNotification`,
 * issue #23a) is not visible to the service worker's `getNotifications()`, so
 * a mail-dismiss push cannot withdraw it while the tab that created it stays
 * open. `#onEmailStateChange` closes these directly instead, off the same
 * Email/changes delta this handler already computes on every state advance:
 * a message the delta reports updated or destroyed is re-checked against its
 * authoritative `$seen`/inbox-membership state and, when it now qualifies,
 * the tracked notification for that emailId is closed.
 *
 * This is independent of push delivery entirely (no Web Push payload is
 * simulated here) -- it is the open-tab channel the SW's mail-dismiss push
 * handler (see sw-mail-dismiss.test.ts) cannot reach.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email, Identity, Mailbox, Thread } from './types';

const syncHandlers = new Map<
  string,
  (newState: string, accountId: string) => void
>();

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(
      (
        type: string,
        handler: (newState: string, accountId: string) => void,
      ) => {
        syncHandlers.set(type, handler);
        return vi.fn();
      },
    ),
  },
}));

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch, session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc1',
        'urn:ietf:params:jmap:submission': 'acc1',
      },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../router/router.svelte', () => ({
  router: { parts: [], getParam: () => null },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));
vi.mock('../debug-ring/debug-ring', () => ({
  appendEvent: vi.fn(() => Promise.resolve()),
}));
// Desktop notifications are opt-in (default false); enable them so
// #fireDesktopNotification actually constructs a Notification.
vi.mock('../settings/settings.svelte', () => ({
  settings: { desktopNotifEnabled: true },
}));

class MockNotification {
  static permission = 'granted';
  static instances: MockNotification[] = [];
  onclick: ((event: Event) => void) | null = null;
  onclose: (() => void) | null = null;
  close = vi.fn();
  constructor(
    public title: string,
    public options?: { body?: string; tag?: string },
  ) {
    MockNotification.instances.push(this);
  }
}

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Carol', email: 'carol@example.test' }],
    to: null,
    subject: 'Re: deploy',
    preview: '',
    receivedAt: '2026-05-09T10:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    ...overrides,
  };
}

function makeIdentity(email: string): Identity {
  return {
    id: `id-${email}`,
    name: '',
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
  };
}

function makeMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id'>): Mailbox {
  return {
    name: overrides.id,
    role: null,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...overrides,
  };
}

/**
 * Install the shared batch mock for one `#onEmailStateChange` invocation.
 * Dispatches purely on JMAP method name -- each `jmap.batch()` call site
 * (the top-level Email/changes call, refreshThread's Thread/get + Email/get,
 * and the reconcile step's own direct Email/get) runs as its own independent
 * invocation of this implementation, so a fixed per-name response is
 * sufficient regardless of which call site issued it.
 */
function installBatchMock(opts: {
  created?: string[];
  updated?: string[];
  destroyed?: string[];
  finalEmail: Email;
  finalState: string;
}): void {
  const { created = [], updated = [], destroyed = [], finalEmail, finalState } = opts;
  batch.mockImplementation(async (builder: unknown) => {
    let counter = 0;
    const calls: { name: string; args: Record<string, unknown> }[] = [];
    const api = {
      call: (name: string, args: Record<string, unknown>) => {
        calls.push({ name, args });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (builder as (b: unknown) => void)(api);
    const responses: Array<[string, unknown, string]> = [];
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i]!;
      if (c.name === 'Email/changes') {
        responses.push([c.name, { created, updated, destroyed }, `c${i}`]);
      } else if (c.name === 'Thread/get') {
        responses.push([
          c.name,
          { list: [{ id: 'tid-1', emailIds: [finalEmail.id] }] },
          `c${i}`,
        ]);
      } else if (c.name === 'Email/get') {
        responses.push([c.name, { list: [finalEmail], state: finalState }, `c${i}`]);
      } else {
        responses.push([c.name, {}, `c${i}`]);
      }
    }
    return { responses, using: new Set<string>() };
  });
}

describe('mail store: page-level desktop notification dismissal (issue #481)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    MockNotification.instances = [];
    vi.resetModules();
    vi.stubGlobal('Notification', MockNotification);
  });

  it('closes the page-level notification once the message is observed $seen elsewhere', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e1'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 'tid-1', mailboxIds: {}, keywords: {} })],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e1']]]);
    mail.emailState = 'state-1';

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    // Step 1: the message arrives in the inbox -- the mail cue fires and
    // #fireDesktopNotification creates and tracks a notification for it.
    installBatchMock({
      created: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: {},
      }),
      finalState: 'state-2',
    });
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(MockNotification.instances).toHaveLength(1);
    const notification = MockNotification.instances[0]!;
    expect(notification.options?.tag).toBe('mail-e1');
    expect(notification.close).not.toHaveBeenCalled();

    // Step 2: the message is marked $seen elsewhere. Email/changes reports
    // it updated; the reconcile step's own Email/get confirms $seen=true.
    installBatchMock({
      updated: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: { $seen: true },
      }),
      finalState: 'state-3',
    });
    await handler!('state-3', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(notification.close).toHaveBeenCalledTimes(1);
    // No second notification was created for the same message.
    expect(MockNotification.instances).toHaveLength(1);
  });

  it('closes the page-level notification once the message leaves the inbox elsewhere', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
      ['mbx-archive', makeMailbox({ id: 'mbx-archive', role: 'archive' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e1'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 'tid-1', mailboxIds: {}, keywords: {} })],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e1']]]);
    mail.emailState = 'state-1';

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    installBatchMock({
      created: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: {},
      }),
      finalState: 'state-2',
    });
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    const notification = MockNotification.instances[0]!;
    expect(notification.close).not.toHaveBeenCalled();

    // Archived elsewhere: the message keeps $seen unset but no longer holds
    // the inbox membership.
    installBatchMock({
      updated: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-archive': true },
        keywords: {},
      }),
      finalState: 'state-3',
    });
    await handler!('state-3', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(notification.close).toHaveBeenCalledTimes(1);
  });

  it('leaves the notification open for an unrelated update that keeps the message unread in the inbox', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e1'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 'tid-1', mailboxIds: {}, keywords: {} })],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e1']]]);
    mail.emailState = 'state-1';

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    installBatchMock({
      created: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: {},
      }),
      finalState: 'state-2',
    });
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    const notification = MockNotification.instances[0]!;

    // An unrelated update (e.g. a flag/label change) leaves the message
    // unread and still in the inbox.
    installBatchMock({
      updated: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: { $flagged: true },
      }),
      finalState: 'state-3',
    });
    await handler!('state-3', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(notification.close).not.toHaveBeenCalled();
  });

  it('closes the page-level notification immediately when marked read through the store itself (issue #483)', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e1'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 'tid-1', mailboxIds: {}, keywords: {} })],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e1']]]);
    mail.emailState = 'state-1';

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    installBatchMock({
      created: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: {},
      }),
      finalState: 'state-2',
    });
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    const notification = MockNotification.instances[0]!;
    expect(notification.close).not.toHaveBeenCalled();

    // Mark the message read through the store's own public action -- no
    // second JMAP session, no Email/changes round trip needed: #patchEmail
    // applies the optimistic keyword change synchronously and the
    // notification closes before the Email/set network call even settles.
    batch.mockResolvedValue({ responses: [['Email/set', {}, 'c0']], using: new Set<string>() });
    await mail.setSeen('e1', true);

    expect(notification.close).toHaveBeenCalledTimes(1);
  });

  it('closes the page-level notification immediately when archived through the store itself (issue #483)', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e1'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 'tid-1', mailboxIds: {}, keywords: {} })],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e1']]]);
    mail.emailState = 'state-1';
    mail.listFolder = 'inbox';

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();

    installBatchMock({
      created: ['e1'],
      finalEmail: makeEmail({
        id: 'e1',
        threadId: 'tid-1',
        mailboxIds: { 'mbx-inbox': true },
        keywords: {},
      }),
      finalState: 'state-2',
    });
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    const notification = MockNotification.instances[0]!;
    expect(notification.close).not.toHaveBeenCalled();

    batch.mockResolvedValue({ responses: [['Email/set', {}, 'c0']], using: new Set<string>() });
    await mail.archiveEmail('e1');

    expect(notification.close).toHaveBeenCalledTimes(1);
  });
});
