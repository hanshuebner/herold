/**
 * Issue #570, Sent-view path. A draft sent with an attachment keeps its
 * Email id. (1) A list fetch (folder load, search, ...) whose response
 * arrives after a newer fetch must not overwrite the fresher cache entry
 * with its older $draft / Drafts / hasAttachment=false shape. (2) A list
 * fetch must not graft a cached body-complete copy captured before the
 * attachment was added (attachments=[]) onto list properties that report
 * hasAttachment=true, because the Sent view then shows the paperclip but no
 * attachment card.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email, EmailBodyPart } from './types';

vi.mock('../jmap/sync.svelte', () => ({ sync: { on: vi.fn(() => vi.fn()) } }));
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
vi.mock('../settings/settings.svelte', () => ({
  settings: { desktopNotifEnabled: false, isImageAllowed: () => false },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k, locale: 'en' },
  localeTag: () => 'en',
}));

const pdf: EmailBodyPart = {
  partId: '3',
  blobId: 'att',
  size: 10,
  type: 'application/pdf',
  charset: null,
  disposition: 'attachment',
  name: 'CV.pdf',
  cid: null,
};

function listEmail(overrides: Partial<Email>): Email {
  return {
    id: 'd1',
    threadId: 't1',
    mailboxIds: { drafts: true },
    keywords: { $draft: true, $seen: true },
    from: [{ name: 'Me', email: 'me@example.test' }],
    to: null,
    subject: 'Re: x',
    preview: '',
    receivedAt: '2026-11-25T10:00:00Z',
    hasAttachment: false,
    blobId: 'b',
    ...overrides,
  };
}

const SENT: Partial<Email> = {
  mailboxIds: { sent: true },
  keywords: { $seen: true },
  hasAttachment: true,
};

function searchResponse(e: Email) {
  return {
    responses: [
      ['Email/query', { ids: [e.id], total: 1 }, 'c0'],
      ['Email/get', { list: [e], state: 's' }, 'c1'],
      ['Thread/get', { list: [{ id: e.threadId, emailIds: [e.id] }] }, 'c2'],
      ['Email/get', { list: [e], state: 's' }, 'c3'],
    ],
    using: new Set<string>(),
  };
}

describe('mail store: Sent-view list path (re #570)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
  });

  it('a list response overtaken by a newer list fetch does not roll the entry back to its draft shape', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([['m1', { id: 'm1', name: 'Inbox', role: 'inbox' } as never]]);

    let releaseOld!: () => void;
    const gate = new Promise<void>((r) => (releaseOld = r));
    batch.mockImplementationOnce(async () => {
      await gate;
      return searchResponse(listEmail({}));
    });
    batch.mockImplementationOnce(async () => searchResponse(listEmail(SENT)));

    const oldSearch = mail.runSearch('older');
    await mail.runSearch('newer');
    releaseOld();
    await oldSearch;

    const e = mail.emails.get('d1')!;
    expect(e.keywords.$draft).toBeUndefined();
    expect(e.mailboxIds).toEqual({ sent: true });
    expect(e.hasAttachment).toBe(true);
  });

  it('a list fetch does not keep a cached body whose attachments predate the in-place send', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map([['m1', { id: 'm1', name: 'Inbox', role: 'inbox' } as never]]);
    // Cached full-fetch of the draft, taken before the PDF was attached.
    mail.emails = new Map([
      [
        'd1',
        listEmail({
          bodyValues: { '1': { value: 'hi', isEncodingProblem: false, isTruncated: false } },
          attachments: [],
        }),
      ],
    ]);
    mail.threadLoadStatus = new Map([['t1', 'ready']]);
    // The follow-up refetch of the ready thread returns the sent shape.
    const sentFull = listEmail({
      ...SENT,
      bodyValues: { '1': { value: 'hi', isEncodingProblem: false, isTruncated: false } },
      attachments: [pdf],
    });
    batch.mockImplementationOnce(async () => searchResponse(listEmail(SENT)));
    batch.mockImplementation(async () => ({
      responses: [
        ['Thread/get', { list: [{ id: 't1', emailIds: ['d1'] }] }, 'c0'],
        ['Email/get', { list: [sentFull], state: 's' }, 'c1'],
      ],
      using: new Set<string>(),
    }));

    await mail.runSearch('q');
    await new Promise((r) => setTimeout(r, 0));

    const e = mail.emails.get('d1')!;
    // Never the stale empty list next to hasAttachment=true.
    expect(e.hasAttachment).toBe(true);
    expect(e.attachments).toEqual([pdf]);
  });

  it('mergeEmailListFetch drops a cached body that disagrees on hasAttachment', async () => {
    const { mergeEmailListFetch } = await import('./store.svelte');
    const cached = listEmail({
      bodyValues: { '1': { value: 'hi', isEncodingProblem: false, isTruncated: false } },
      attachments: [],
    });
    const merged = mergeEmailListFetch(cached, listEmail(SENT));
    expect(merged.attachments).toBeUndefined();
    expect(merged.hasAttachment).toBe(true);
  });
});
