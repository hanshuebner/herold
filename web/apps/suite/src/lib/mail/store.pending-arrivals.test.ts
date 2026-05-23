/**
 * Tests for the "new reply while reading" plumbing on the mail store
 * (issue #118). The store exposes:
 *
 *   - `openThreadId` + `setOpenThread()` — ThreadReader registers the
 *     currently-rendered thread so the Email/changes handler knows when
 *     a fresh arrival should ping.
 *   - `pendingArrivalsForThread()` + `dismissPendingArrivals()` —
 *     ThreadReader's banner reads / clears the pending arrivals set.
 *   - `#recordPendingArrivals` — invoked from `#onEmailStateChange`
 *     after the cache refresh, populates the set with arrivals in the
 *     open thread that aren't from the user themselves.
 *
 * The handler path is exercised end-to-end by capturing the `Email`
 * sync handler at module import time, mocking `jmap.batch` to return
 * a synthetic Email/changes delta + Thread/get + Email/get response,
 * and asserting that the resulting `pendingArrivals` matches.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email, Identity, Thread } from './types';

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
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
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

describe('mail store: pending-arrival surface (issue #118)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    vi.resetModules();
  });

  it('setOpenThread / pendingArrivalsForThread / dismissPendingArrivals manage the per-thread set', async () => {
    const { mail } = await import('./store.svelte');
    expect(mail.openThreadId).toBeNull();
    expect(mail.pendingArrivalsForThread('tid-1')).toEqual([]);

    // Simulate a pending arrival having been recorded by the sync path.
    mail.emails.set(
      'e-arrival',
      makeEmail({ id: 'e-arrival', threadId: 'tid-1', receivedAt: '2026-05-09T10:01:00Z' }),
    );
    mail.pendingArrivals = new Map([['tid-1', new Set(['e-arrival'])]]);

    const arrivals = mail.pendingArrivalsForThread('tid-1');
    expect(arrivals.map((e) => e.id)).toEqual(['e-arrival']);

    // Dismiss drops the entry.
    mail.dismissPendingArrivals('tid-1');
    expect(mail.pendingArrivalsForThread('tid-1')).toEqual([]);
  });

  it('setOpenThread wipes pending arrivals from threads other than the one being opened', async () => {
    const { mail } = await import('./store.svelte');
    mail.emails.set('e-a', makeEmail({ id: 'e-a', threadId: 'tid-a' }));
    mail.emails.set('e-b', makeEmail({ id: 'e-b', threadId: 'tid-b' }));
    mail.pendingArrivals = new Map([
      ['tid-a', new Set(['e-a'])],
      ['tid-b', new Set(['e-b'])],
    ]);

    mail.setOpenThread('tid-b');
    expect(mail.openThreadId).toBe('tid-b');
    expect(mail.pendingArrivalsForThread('tid-a')).toEqual([]);
    expect(mail.pendingArrivalsForThread('tid-b').map((e) => e.id)).toEqual(['e-b']);

    // Setting null clears everything.
    mail.setOpenThread(null);
    expect(mail.openThreadId).toBeNull();
    expect(mail.pendingArrivalsForThread('tid-b')).toEqual([]);
  });

  it('Email/changes records arrivals in the currently-open thread, skipping self-sent messages', async () => {
    const { mail } = await import('./store.svelte');

    // Seed: existing thread state + identities so isFromSelf works.
    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e-old'] }],
    ]);
    mail.emails = new Map<string, Email>([
      [
        'e-old',
        makeEmail({ id: 'e-old', threadId: 'tid-1', receivedAt: '2026-05-09T09:00:00Z' }),
      ],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);

    // ThreadReader registers the open thread.
    mail.setOpenThread('tid-1');

    // Prime the state baseline so the next push triggers the diff path.
    // The handler captures the baseline on first push by recording the
    // newState; subsequent pushes go through the changes/refresh flow.
    mail.emailState = 'state-1';

    // Push: Email/changes returns one created (carol's new reply) plus
    // the Thread/get + Email/get refresh that adds the row to the cache.
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
            { created: ['e-new'], updated: [], destroyed: [] },
            `c${i}`,
          ]);
        } else if (c.name === 'Email/query') {
          responses.push([c.name, { ids: ['e-new', 'e-old'] }, `c${i}`]);
        } else if (c.name === 'Email/get') {
          responses.push([
            c.name,
            {
              list: [
                makeEmail({
                  id: 'e-new',
                  threadId: 'tid-1',
                  from: [{ name: 'Carol', email: 'carol@example.test' }],
                  receivedAt: '2026-05-09T10:30:00Z',
                  preview: 'snippet',
                }),
                makeEmail({ id: 'e-old', threadId: 'tid-1' }),
              ],
              state: 'state-2',
            },
            `c${i}`,
          ]);
        } else if (c.name === 'Thread/get') {
          responses.push([
            c.name,
            { list: [{ id: 'tid-1', emailIds: ['e-old', 'e-new'] }] },
            `c${i}`,
          ]);
        } else if (c.name === 'Mailbox/get') {
          responses.push([c.name, { list: [], state: 'mbx-state-1' }, `c${i}`]);
        } else {
          responses.push([c.name, {}, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    expect(handler).toBeDefined();
    await handler!('state-2', 'acc1');

    // Drain microtasks queued by the handler's coalesced refresh paths.
    await new Promise((r) => setTimeout(r, 0));

    const arrivals = mail.pendingArrivalsForThread('tid-1');
    expect(arrivals.map((e) => e.id)).toEqual(['e-new']);
  });

  it('Email/changes ignores arrivals in other threads and self-sent arrivals in the open thread', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.threads = new Map<string, Thread>([
      ['tid-open', { id: 'tid-open', emailIds: ['e-seed'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e-seed', makeEmail({ id: 'e-seed', threadId: 'tid-open' })],
    ]);
    mail.threadLoadStatus = new Map([['tid-open', 'ready']]);
    mail.setOpenThread('tid-open');
    mail.emailState = 'state-x';

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
            // e-self: own send in the open thread (must be filtered).
            // e-other: arrival in a different thread (must be ignored).
            { created: ['e-self', 'e-other'], updated: [], destroyed: [] },
            `c${i}`,
          ]);
        } else if (c.name === 'Email/get') {
          responses.push([
            c.name,
            {
              list: [
                makeEmail({
                  id: 'e-self',
                  threadId: 'tid-open',
                  from: [{ name: 'Me', email: 'me@example.test' }],
                  receivedAt: '2026-05-09T11:00:00Z',
                }),
                makeEmail({
                  id: 'e-other',
                  threadId: 'tid-other',
                  receivedAt: '2026-05-09T11:01:00Z',
                }),
                makeEmail({ id: 'e-seed', threadId: 'tid-open' }),
              ],
              state: 'state-y',
            },
            `c${i}`,
          ]);
        } else if (c.name === 'Thread/get') {
          responses.push([
            c.name,
            { list: [{ id: 'tid-open', emailIds: ['e-seed', 'e-self'] }] },
            `c${i}`,
          ]);
        } else if (c.name === 'Mailbox/get') {
          responses.push([c.name, { list: [], state: 'mbx' }, `c${i}`]);
        } else {
          responses.push([c.name, {}, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    await handler!('state-y', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    expect(mail.pendingArrivalsForThread('tid-open')).toEqual([]);
    expect(mail.pendingArrivalsForThread('tid-other')).toEqual([]);
  });
});
