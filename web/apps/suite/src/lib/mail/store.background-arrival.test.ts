/**
 * Issue #246: a message that threads into a thread the user has already
 * opened once (so it is cached as `ready`) but is not currently reading
 * must still surface in that thread's rendered membership and count as
 * soon as the background `refreshThread()` picks it up -- there is no
 * "Neue Antwort anzeigen" banner UI for a thread that isn't open to gate
 * the arrival behind.
 *
 * The currently-open thread is a different story: `#processFreshArrivals`
 * continues to hold external arrivals in `pendingArrivals` until the user
 * accepts them via the banner. This suite exercises both threads in the
 * same push to pin the split explicitly.
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

describe('mail store: background arrival into a ready-but-not-open thread (re #246)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    batch.mockReset();
    vi.resetModules();
  });

  it('advances the committed snapshot for a ready-not-open thread while still gating the open thread behind its banner', async () => {
    const { mail } = await import('./store.svelte');

    mail.identities = new Map([['id-1', makeIdentity('me@example.test')]]);

    // tid-open: currently being read by the user.
    // tid-ready: opened earlier (has a committed snapshot + threadLoadStatus
    // 'ready') but the user has since navigated back to the folder list.
    mail.threads = new Map<string, Thread>([
      ['tid-open', { id: 'tid-open', emailIds: ['e-open-old'] }],
      ['tid-ready', { id: 'tid-ready', emailIds: ['e-ready-old'] }],
    ]);
    mail.emails = new Map<string, Email>([
      ['e-open-old', makeEmail({ id: 'e-open-old', threadId: 'tid-open' })],
      ['e-ready-old', makeEmail({ id: 'e-ready-old', threadId: 'tid-ready' })],
    ]);
    mail.threadLoadStatus = new Map([
      ['tid-open', 'ready'],
      ['tid-ready', 'ready'],
    ]);
    mail.committedThreadEmailIds = new Map([
      ['tid-open', ['e-open-old']],
      ['tid-ready', ['e-ready-old']],
    ]);
    mail.setOpenThread('tid-open');
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
            // A reply lands in the open thread (external -- must be gated
            // behind the banner) and a bounce lands in the closed-but-ready
            // thread (must surface immediately, no banner exists for it).
            { created: ['e-open-new', 'e-ready-bounce'], updated: [], destroyed: [] },
            `c${i}`,
          ]);
        } else if (c.name === 'Thread/get') {
          responses.push([
            c.name,
            {
              list: [
                { id: 'tid-open', emailIds: ['e-open-old', 'e-open-new'] },
                { id: 'tid-ready', emailIds: ['e-ready-old', 'e-ready-bounce'] },
              ],
            },
            `c${i}`,
          ]);
        } else if (c.name === 'Email/get') {
          responses.push([
            c.name,
            {
              list: [
                makeEmail({ id: 'e-open-old', threadId: 'tid-open' }),
                makeEmail({
                  id: 'e-open-new',
                  threadId: 'tid-open',
                  from: [{ name: 'Dave', email: 'dave@example.test' }],
                  receivedAt: '2026-05-09T11:00:00Z',
                }),
                makeEmail({ id: 'e-ready-old', threadId: 'tid-ready' }),
                makeEmail({
                  id: 'e-ready-bounce',
                  threadId: 'tid-ready',
                  from: [{ name: 'Mail Delivery Subsystem', email: 'mailer-daemon@example.test' }],
                  subject: 'Mail delivery failed',
                  receivedAt: '2026-05-09T11:05:00Z',
                }),
              ],
              state: 'state-2',
            },
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
    await new Promise((r) => setTimeout(r, 0));

    // tid-ready: no banner UI exists for a non-open thread, so the arrival
    // is admitted directly into the committed snapshot.
    expect(mail.committedThreadEmailIds.get('tid-ready')).toEqual([
      'e-ready-old',
      'e-ready-bounce',
    ]);
    expect(mail.threadEmails('tid-ready').map((e) => e.id)).toContain('e-ready-bounce');
    expect(mail.threadDedupeCount('tid-ready')).toBe(2);
    expect(mail.pendingArrivalsForThread('tid-ready')).toEqual([]);

    // tid-open: unchanged banner-deferral behaviour -- the external arrival
    // stays out of the committed snapshot until accepted via the banner.
    expect(mail.committedThreadEmailIds.get('tid-open')).toEqual(['e-open-old']);
    expect(mail.pendingArrivalsForThread('tid-open').map((e) => e.id)).toEqual(['e-open-new']);
  });
});
