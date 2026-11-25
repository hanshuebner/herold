/**
 * Issue #570: an inline reply autosaves a draft, whose creation triggers a
 * thread refresh. Sending (the same Email id is updated in place with the
 * attachment, then moved to Sent) triggers a second refresh. When the first
 * refresh's response lands after the second, applying it rolled the cached
 * message back to its draft-time shape (no attachment, $draft, Drafts), and
 * the Suite showed the sent message without its attachment card until a
 * hard reload. A thread fetch overtaken by a newer one must be discarded.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email } from './types';

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
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

function draftEmail(overrides: Partial<Email>): Email {
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
    attachments: [],
    ...overrides,
  };
}

function response(email: Email) {
  return {
    responses: [
      ['Thread/get', { list: [{ id: 't1', emailIds: ['d1'] }] }, 'c0'],
      ['Email/get', { list: [email], state: 's' }, 'c1'],
    ],
    using: new Set<string>(),
  };
}

describe('mail store: out-of-order thread refresh responses (re #570)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
  });

  it('discards a refresh response overtaken by a newer refresh of the same thread', async () => {
    const { mail } = await import('./store.svelte');

    let releaseOld!: () => void;
    const oldGate = new Promise<void>((r) => (releaseOld = r));
    // First refresh: read while still a draft, response held back.
    batch.mockImplementationOnce(async () => {
      await oldGate;
      return response(draftEmail({}));
    });
    // Second refresh: read after the send committed.
    batch.mockImplementationOnce(async () =>
      response(
        draftEmail({
          mailboxIds: { sent: true },
          keywords: { $seen: true },
          hasAttachment: true,
          attachments: [
            {
              partId: '3',
              blobId: 'att',
              size: 10,
              type: 'application/pdf',
              charset: null,
              disposition: 'attachment',
              name: 'CV.pdf',
              cid: null,
            },
          ],
        }),
      ),
    );

    const oldRefresh = mail.refreshThread('t1');
    await mail.refreshThread('t1');
    releaseOld();
    await oldRefresh;

    const e = mail.emails.get('d1')!;
    expect(e.attachments).toHaveLength(1);
    expect(e.hasAttachment).toBe(true);
    expect(e.keywords.$draft).toBeUndefined();
    expect(e.mailboxIds).toEqual({ sent: true });
  });

  it('still applies a later-issued response that lands after an earlier one', async () => {
    const { mail } = await import('./store.svelte');
    batch.mockImplementationOnce(async () => response(draftEmail({ subject: 'first' })));
    batch.mockImplementationOnce(async () => response(draftEmail({ subject: 'second' })));
    await mail.refreshThread('t1');
    await mail.refreshThread('t1');
    expect(mail.emails.get('d1')!.subject).toBe('second');
  });
});
