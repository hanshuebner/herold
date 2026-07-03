/**
 * Tests for the "new reply while reading" plumbing on the mail store
 * (issue #118, issue #55, issue #56). The store exposes:
 *
 *   - `openThreadId` + `setOpenThread()` — ThreadReader registers the
 *     currently-rendered thread so the Email/changes handler knows when
 *     a fresh arrival should ping.
 *   - `pendingArrivalsForThread()` + `dismissPendingArrivals()` +
 *     `acceptPendingArrivals()` — ThreadReader's banner reads / accepts
 *     / dismisses the pending arrivals set.
 *   - `committedThreadEmailIds` — frozen snapshot of the email IDs the
 *     reader is currently rendering; guards against auto-insertion of
 *     external arrivals (issue #56).
 *   - `gatedEmailIds` — tracks arrivals that have already passed through
 *     the banner (accepted or dismissed) so that subsequent state-change
 *     events don't re-trigger the banner for the same messages.
 *   - `#processFreshArrivals` — invoked from `#onEmailStateChange` after
 *     the cache refresh; self-sent arrivals are auto-committed (no banner),
 *     external arrivals go to `pendingArrivals` (shown in banner).
 *
 * The handler path is exercised end-to-end by capturing the `Email`
 * sync handler at module import time, mocking `jmap.batch` to return
 * a synthetic Email/changes delta + Thread/get + Email/get response,
 * and asserting that the resulting `pendingArrivals` / committed snapshot
 * match expectations.
 *
 * Pure helper tests (`normalizeMessageId`, `dedupeArrivalsByMessageId`)
 * import the helpers directly from the store's `_internals_forTest` export.
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
  registerAccountResetCallback: vi.fn(),
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

// ---------------------------------------------------------------------------
// Pure helper tests (no store singleton needed, import directly)
// ---------------------------------------------------------------------------

describe('normalizeMessageId', () => {
  it('strips angle brackets and lowercases', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { normalizeMessageId } = _internals_forTest;
    expect(normalizeMessageId('<Foo.BAR@host>')).toBe('foo.bar@host');
    expect(normalizeMessageId('<abc@DEF.COM>')).toBe('abc@def.com');
  });

  it('returns null for empty or whitespace-only input', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { normalizeMessageId } = _internals_forTest;
    expect(normalizeMessageId('')).toBeNull();
    expect(normalizeMessageId('   ')).toBeNull();
    expect(normalizeMessageId('<>')).toBeNull();
  });

  it('handles input without angle brackets', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { normalizeMessageId } = _internals_forTest;
    expect(normalizeMessageId('plain@host')).toBe('plain@host');
  });
});

describe('dedupeArrivalsByMessageId', () => {
  it('removes new IDs whose Message-ID matches an existing committed email', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { dedupeArrivalsByMessageId } = _internals_forTest;
    const emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 't1', messageId: ['<msg1@host>'] })],
      ['e2', makeEmail({ id: 'e2', threadId: 't1', messageId: ['<msg1@host>'] })],
    ]);
    // e1 is committed; e2 shares the same Message-ID — must be dropped.
    expect(dedupeArrivalsByMessageId(['e2'], ['e1'], emails)).toEqual([]);
  });

  it('passes through IDs whose email has no messageId field', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { dedupeArrivalsByMessageId } = _internals_forTest;
    const emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 't1' })],
    ]);
    expect(dedupeArrivalsByMessageId(['e1'], [], emails)).toEqual(['e1']);
  });

  it('deduplicates within newIds themselves', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { dedupeArrivalsByMessageId } = _internals_forTest;
    const emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 't1', messageId: ['<msg1@host>'] })],
      ['e2', makeEmail({ id: 'e2', threadId: 't1', messageId: ['<MSG1@HOST>'] })],
    ]);
    // Both e1 and e2 have the same normalised Message-ID; keep only the first.
    expect(dedupeArrivalsByMessageId(['e1', 'e2'], [], emails)).toEqual(['e1']);
  });

  it('passes through IDs that are genuinely distinct', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { dedupeArrivalsByMessageId } = _internals_forTest;
    const emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 't1', messageId: ['<msg1@host>'] })],
      ['e2', makeEmail({ id: 'e2', threadId: 't1', messageId: ['<msg2@host>'] })],
    ]);
    expect(dedupeArrivalsByMessageId(['e2'], ['e1'], emails)).toEqual(['e2']);
  });

  it('admits a new arrival whose size exceeds the committed copy of the same Message-ID', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { dedupeArrivalsByMessageId } = _internals_forTest;
    // e1 is the committed (sent) copy at 1200 bytes.
    // e2 is the inbound copy at 1850 bytes (transit headers make it larger).
    const emails = new Map<string, Email>([
      ['e1', { ...makeEmail({ id: 'e1', threadId: 't1', messageId: ['<msg1@host>'] }), size: 1200 }],
      ['e2', { ...makeEmail({ id: 'e2', threadId: 't1', messageId: ['<msg1@host>'] }), size: 1850 }],
    ]);
    // e2 is richer than the committed e1 — it must be admitted so that
    // resolveDeduplicatedThreadEmails can select it as representative.
    expect(dedupeArrivalsByMessageId(['e2'], ['e1'], emails)).toEqual(['e2']);
  });

  it('drops a same-Message-ID arrival whose size does not exceed the committed copy', async () => {
    const { _internals_forTest } = await import('./store.svelte');
    const { dedupeArrivalsByMessageId } = _internals_forTest;
    const emails = new Map<string, Email>([
      ['e1', { ...makeEmail({ id: 'e1', threadId: 't1', messageId: ['<msg1@host>'] }), size: 1850 }],
      ['e2', { ...makeEmail({ id: 'e2', threadId: 't1', messageId: ['<msg1@host>'] }), size: 1200 }],
    ]);
    // e2 is smaller than the committed e1 — drop it.
    expect(dedupeArrivalsByMessageId(['e2'], ['e1'], emails)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Store singleton tests
// ---------------------------------------------------------------------------

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

    // Dismiss drops the pending-arrivals entry and gates the id.
    mail.dismissPendingArrivals('tid-1');
    expect(mail.pendingArrivalsForThread('tid-1')).toEqual([]);
    // Dismissed id is tracked in gatedEmailIds.
    expect(mail.gatedEmailIds.get('tid-1')?.has('e-arrival')).toBe(true);
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

  it('acceptPendingArrivals advances committed snapshot and returns new emails', async () => {
    const { mail } = await import('./store.svelte');
    mail.emails.set('e-old', makeEmail({ id: 'e-old', threadId: 'tid-1' }));
    mail.emails.set(
      'e-new',
      makeEmail({ id: 'e-new', threadId: 'tid-1', receivedAt: '2026-05-09T11:00:00Z' }),
    );
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-old']]]);
    mail.pendingArrivals = new Map([['tid-1', new Set(['e-new'])]]);

    const added = mail.acceptPendingArrivals('tid-1');
    expect(added.map((e) => e.id)).toEqual(['e-new']);
    // Committed snapshot now includes the accepted email.
    expect(mail.committedThreadEmailIds.get('tid-1')).toEqual(['e-old', 'e-new']);
    // Pending arrivals cleared.
    expect(mail.pendingArrivalsForThread('tid-1')).toEqual([]);
    // Accepted id is gated.
    expect(mail.gatedEmailIds.get('tid-1')?.has('e-new')).toBe(true);
  });

  it('acceptPendingArrivals admits both copies of a same-Message-ID pair into committed (re #88)', async () => {
    const { mail } = await import('./store.svelte');
    const eSent = makeEmail({ id: 'e-sent', threadId: 'tid-1', messageId: ['<msg1@host>'] });
    const eInbox = makeEmail({ id: 'e-inbox', threadId: 'tid-1', messageId: ['<msg1@host>'] });
    mail.emails.set('e-sent', eSent);
    mail.emails.set('e-inbox', eInbox);
    // e-sent is already committed; e-inbox shares the same Message-ID but
    // a different raw ID. The committed snapshot stores all copies so that
    // resolveDeduplicatedThreadEmails() can union their mailboxIds at read
    // time. Raw-ID dedup only: e-inbox must be ADDED, not dropped.
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-sent']]]);
    mail.pendingArrivals = new Map([['tid-1', new Set(['e-inbox'])]]);

    const added = mail.acceptPendingArrivals('tid-1');
    // e-inbox is a new raw ID: added to committed so the mailboxIds union works.
    expect(added.map((e) => e.id)).toEqual(['e-inbox']);
    expect(mail.committedThreadEmailIds.get('tid-1')).toEqual(['e-sent', 'e-inbox']);
    // Both IDs gated so the banner is not re-triggered.
    expect(mail.gatedEmailIds.get('tid-1')?.has('e-inbox')).toBe(true);
  });

  it('dismissPendingArrivals gates ids without advancing committed snapshot', async () => {
    const { mail } = await import('./store.svelte');
    mail.emails.set('e-old', makeEmail({ id: 'e-old', threadId: 'tid-1' }));
    mail.emails.set('e-new', makeEmail({ id: 'e-new', threadId: 'tid-1' }));
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-old']]]);
    mail.pendingArrivals = new Map([['tid-1', new Set(['e-new'])]]);

    mail.dismissPendingArrivals('tid-1');

    // Committed snapshot unchanged — dismissed email is NOT shown.
    expect(mail.committedThreadEmailIds.get('tid-1')).toEqual(['e-old']);
    // Pending arrivals cleared.
    expect(mail.pendingArrivalsForThread('tid-1')).toEqual([]);
    // Dismissed id is gated so it won't re-trigger the banner on next sync.
    expect(mail.gatedEmailIds.get('tid-1')?.has('e-new')).toBe(true);
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
    // Set the committed snapshot so #processFreshArrivals can run.
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-old']]]);

    // ThreadReader registers the open thread.
    mail.setOpenThread('tid-1');

    // Prime the state baseline so the next push triggers the diff path.
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
    mail.committedThreadEmailIds = new Map([['tid-open', ['e-seed']]]);
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
            // e-self: own send in the open thread (must be auto-committed, not bannered).
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

    // Self-sent message in the open thread: no banner, auto-committed.
    expect(mail.pendingArrivalsForThread('tid-open')).toEqual([]);
    expect(mail.committedThreadEmailIds.get('tid-open')).toContain('e-self');
    // Arrival in a different thread: ignored.
    expect(mail.pendingArrivalsForThread('tid-other')).toEqual([]);
  });

  it('gated emails do not re-trigger the pending arrivals banner on subsequent syncs', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);
    mail.threads = new Map<string, Thread>([
      ['tid-1', { id: 'tid-1', emailIds: ['e-old', 'e-external'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e-old', makeEmail({ id: 'e-old', threadId: 'tid-1' })],
      [
        'e-external',
        makeEmail({
          id: 'e-external',
          threadId: 'tid-1',
          receivedAt: '2026-05-09T11:00:00Z',
        }),
      ],
    ]);
    mail.threadLoadStatus = new Map([['tid-1', 'ready']]);
    mail.committedThreadEmailIds = new Map([['tid-1', ['e-old']]]);
    // Gate e-external (as if the user had previously dismissed its banner).
    mail.gatedEmailIds = new Map([['tid-1', new Set(['e-external'])]]);
    mail.setOpenThread('tid-1');

    // A trivial sync that reports no new Email/changes but still exercises
    // #processFreshArrivals — simulate by calling it via a state push
    // that returns no changes.
    mail.emailState = 'state-a';
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
          responses.push([c.name, { created: [], updated: [], destroyed: [] }, `c${i}`]);
        } else if (c.name === 'Mailbox/get') {
          responses.push([c.name, { list: [], state: 'mbx-a' }, `c${i}`]);
        } else {
          responses.push([c.name, { list: [], state: 'state-b' }, `c${i}`]);
        }
      }
      return { responses, using: new Set<string>() };
    });

    const handler = syncHandlers.get('Email');
    await handler!('state-b', 'acc1');
    await new Promise((r) => setTimeout(r, 0));

    // e-external is in thread.emailIds but also in gatedEmailIds, so it
    // must NOT appear in pendingArrivals.
    expect(mail.pendingArrivalsForThread('tid-1')).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// threadDedupeCount with all-copies committed snapshot (re #88)
// ---------------------------------------------------------------------------

describe('mail store: threadDedupeCount with all-copies committed snapshot (re #88)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    vi.resetModules();
  });

  it('returns the logical-message count (3) when committed snapshot holds all 6 rows of a self-sent thread', async () => {
    const { mail } = await import('./store.svelte');

    const SENT = 'mbx-sent';
    const INBOX = 'mbx-inbox';

    // Six email rows: 3 logical messages, each as Sent + Inbox copy.
    for (const [id, mbx, mid] of [
      ['s1', SENT, '<m1@h>'],
      ['d1', INBOX, '<m1@h>'],
      ['s2', SENT, '<m2@h>'],
      ['d2', INBOX, '<m2@h>'],
      ['s3', SENT, '<m3@h>'],
      ['d3', INBOX, '<m3@h>'],
    ] as [string, string, string][]) {
      mail.emails.set(id, makeEmail({ id, threadId: 'tid-self', messageId: [mid], mailboxIds: { [mbx]: true } as Record<string, true> }));
    }

    // Seed committed snapshot with ALL 6 IDs — the new loadThread behaviour.
    mail.committedThreadEmailIds = new Map([
      ['tid-self', ['s1', 'd1', 's2', 'd2', 's3', 'd3']],
    ]);

    // threadDedupeCount must group same-Message-ID copies and return 3.
    expect(mail.threadDedupeCount('tid-self')).toBe(3);
  });

  it('threadEmails with all-copies committed snapshot unions mailboxIds for each logical message', async () => {
    const { mail } = await import('./store.svelte');

    const SENT = 'mbx-sent';
    const INBOX = 'mbx-inbox';

    for (const [id, mbx, mid] of [
      ['s1', SENT, '<m1@h>'],
      ['d1', INBOX, '<m1@h>'],
      ['s2', SENT, '<m2@h>'],
      ['d2', INBOX, '<m2@h>'],
    ] as [string, string, string][]) {
      mail.emails.set(id, makeEmail({ id, threadId: 'tid-self', messageId: [mid], mailboxIds: { [mbx]: true } as Record<string, true> }));
    }

    mail.committedThreadEmailIds = new Map([
      ['tid-self', ['s1', 'd1', 's2', 'd2']],
    ]);

    const emails = mail.threadEmails('tid-self');
    // Two logical messages, each representative has both mailboxIds.
    expect(emails).toHaveLength(2);
    for (const e of emails) {
      expect(e.mailboxIds[SENT]).toBe(true);
      expect(e.mailboxIds[INBOX]).toBe(true);
    }
  });
});
