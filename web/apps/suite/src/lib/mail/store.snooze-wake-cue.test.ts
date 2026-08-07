/**
 * Issue #274: a snooze wake adds the message to its wake-destination
 * mailbox via `AddMessageToMailbox`, which the store-side change feed
 * records as `ChangeOpCreated` for that Email id -- regardless of
 * whether the client already knew the id. `Email/changes` therefore
 * reports a woken message in `created`, not `updated`, even though it
 * is not a brand-new message. `#onEmailStateChange` must treat that
 * "known id reappears in `created`" pattern as the wake signal and
 * evaluate the mail-cue gate for it, since the plain
 * `knownEmailIds.has(id)` short-circuit would otherwise suppress the
 * cue for every woken message (they are, by definition, already known).
 *
 * A message merely `updated` (e.g. a keyword flip, no membership
 * change) must not re-trigger the cue -- that is the regression guard
 * this suite also pins.
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

const playMailCue = vi.fn();
vi.mock('../notifications/sounds.svelte', () => ({
  sounds: { play: playMailCue },
}));

// Real cue-gates logic (not stubbed) -- this suite depends on
// shouldPlayMailCue's actual inbox-membership gate to distinguish a
// wake from a no-op update.
vi.mock('../router/router.svelte', () => ({
  router: { parts: [], getParam: () => null },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

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

describe('mail store: snooze-wake mail cue (issue #274)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    playMailCue.mockReset();
    vi.resetModules();
  });

  it('fires the mail cue when a known message reappears via a fresh Email/changes created entry', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
      ['mbx-sent', makeMailbox({ id: 'mbx-sent', role: 'sent' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e-woken'] }],
    ]);
    mail.emails = new Map<string, Email>([
      [
        'e-woken',
        makeEmail({
          id: 'e-woken',
          threadId: 'tid-1',
          mailboxIds: { 'mbx-sent': true },
        }),
      ],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-woken']]]);
    mail.emailState = 'state-1';

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
          // The wake worker's AddMessageToMailbox records ChangeOpCreated
          // for the already-known id -- it lands in `created`, not
          // `updated` (see internal/protojmap/mail/email/changes.go).
          responses.push([
            c.name,
            { created: ['e-woken'], updated: [], destroyed: [] },
            `c${i}`,
          ]);
        } else if (c.name === 'Thread/get') {
          responses.push([
            c.name,
            { list: [{ id: 'tid-1', emailIds: ['e-woken'] }] },
            `c${i}`,
          ]);
        } else if (c.name === 'Email/get') {
          responses.push([
            c.name,
            {
              list: [
                makeEmail({
                  id: 'e-woken',
                  threadId: 'tid-1',
                  // Wake retains the origin membership and adds the
                  // destination (Inbox) membership (issue #274).
                  mailboxIds: { 'mbx-sent': true, 'mbx-inbox': true },
                }),
              ],
              state: 'state-2',
            },
            `c${i}`,
          ]);
        } else {
          responses.push([c.name, {}, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(playMailCue).toHaveBeenCalledWith('mail');
    expect(mail.emails.get('e-woken')?.mailboxIds).toEqual({
      'mbx-sent': true,
      'mbx-inbox': true,
    });
  });

  it('does not fire the mail cue for a plain update with no membership gain', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-inbox', makeMailbox({ id: 'mbx-inbox', role: 'inbox' })],
      ['mbx-sent', makeMailbox({ id: 'mbx-sent', role: 'sent' })],
    ]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e-known'] }],
    ]);
    mail.emails = new Map<string, Email>([
      [
        'e-known',
        makeEmail({
          id: 'e-known',
          threadId: 'tid-1',
          mailboxIds: { 'mbx-inbox': true },
          keywords: {},
        }),
      ],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-known']]]);
    mail.emailState = 'state-1';

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
          responses.push([
            c.name,
            { created: [], updated: ['e-known'], destroyed: [] },
            `c${i}`,
          ]);
        } else if (c.name === 'Thread/get') {
          responses.push([
            c.name,
            { list: [{ id: 'tid-1', emailIds: ['e-known'] }] },
            `c${i}`,
          ]);
        } else if (c.name === 'Email/get') {
          responses.push([
            c.name,
            {
              list: [
                makeEmail({
                  id: 'e-known',
                  threadId: 'tid-1',
                  mailboxIds: { 'mbx-inbox': true },
                  keywords: { $seen: true },
                }),
              ],
              state: 'state-2',
            },
            `c${i}`,
          ]);
        } else {
          responses.push([c.name, {}, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();
    await handler!('state-2', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(playMailCue).not.toHaveBeenCalled();
  });
});
