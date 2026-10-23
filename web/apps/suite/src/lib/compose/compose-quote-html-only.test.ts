/**
 * Forward and reply quoting of an HTML-only original (re #533).
 *
 * RFC 8621 4.1.4 lists the text/html part in `textBody` when a message has
 * no text/plain alternative. The quote must contain readable text, never
 * the HTML source.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Email, EmailBodyPart } from '../mail/types';
import { emailTextBody } from '../mail/types';

vi.mock('../jmap/client', () => ({
  jmap: { maxUploadSize: null, uploadBlob: vi.fn(), downloadUrl: vi.fn().mockReturnValue(null) },
  strict: vi.fn(),
}));
vi.mock('../mail/sub-accounts.svelte', () => ({ subAccounts: { find: vi.fn().mockReturnValue(null) } }));
vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    primaryIdentity: null,
    drafts: null,
    identities: new Map(),
    mailboxes: new Map(),
    loadMailboxes: vi.fn(),
    loadIdentities: vi.fn(),
  },
}));
vi.mock('../settings/settings.svelte', () => ({ settings: { undoWindowSec: 0 } }));
vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../i18n/i18n.svelte', async (orig) => ({ ...(await orig<object>()), localeTag: () => 'en' }));

const HTML =
  '<!DOCTYPE html><html><head><meta charset="utf-8"><style>p{color:red}</style></head>' +
  '<body><p>Readable marketing content</p></body></html>';

function part(partId: string, type: string): EmailBodyPart {
  return { partId, blobId: 'b' + partId, size: 1, type, charset: 'utf-8', disposition: null, name: null, cid: null } as EmailBodyPart;
}

function makeParent(over: Partial<Email>): Email {
  return {
    id: 'm1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Sender', email: 'sender@example.com' }],
    to: [{ name: null, email: 'me@example.local' }],
    subject: 'Hello',
    preview: '',
    receivedAt: '2026-10-23T00:00:00Z',
    attachments: [],
    ...over,
  } as unknown as Email;
}

const bv = (value: string) => ({ value, isEncodingProblem: false, isTruncated: false });

const htmlOnly = () =>
  makeParent({
    textBody: [part('1', 'text/html')],
    htmlBody: [part('1', 'text/html')],
    bodyValues: { '1': bv(HTML) },
  });

const alternative = () =>
  makeParent({
    textBody: [part('1', 'text/plain')],
    htmlBody: [part('2', 'text/html')],
    bodyValues: { '1': bv('Plain alternative text'), '2': bv('<p>HTML alternative text</p>') },
  });

const textOnly = () =>
  makeParent({
    textBody: [part('1', 'text/plain')],
    htmlBody: [part('1', 'text/plain')],
    bodyValues: { '1': bv('Only plain text') },
  });

// text/plain, text/html, text/plain: the HTML part is neither first nor last.
const htmlInMiddle = () =>
  makeParent({
    textBody: [part('1', 'text/plain'), part('2', 'text/html'), part('3', 'text/plain')],
    htmlBody: [part('1', 'text/plain'), part('2', 'text/html'), part('3', 'text/plain')],
    bodyValues: {
      '1': bv('Leading note'),
      '2': bv('<p>Middle <b>html</b> part</p>'),
      '3': bv('Trailing note'),
    },
  });

describe('emailTextBody ignores text/html parts (re #533)', () => {
  it('returns null for an HTML-only message', () => {
    expect(emailTextBody(htmlOnly())).toBeNull();
  });
  it('returns the text/plain alternative', () => {
    expect(emailTextBody(alternative())).toBe('Plain alternative text');
  });
  it('skips an HTML part in the middle of the list', () => {
    const r = emailTextBody(htmlInMiddle()) ?? '';
    expect(r).toContain('Leading note');
    expect(r).toContain('Trailing note');
    expect(r).not.toContain('<p>');
  });
});

describe('quoting (re #533)', () => {
  beforeEach(async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
  });

  async function forwardBody(parent: Email): Promise<string> {
    const { compose } = await import('./compose.svelte');
    compose.openForward(parent);
    return compose.body;
  }
  async function replyBody(parent: Email): Promise<string> {
    const { compose } = await import('./compose.svelte');
    await compose.openReply(parent);
    return compose.body;
  }

  for (const [name, open] of [['forward', forwardBody], ['reply', replyBody]] as const) {
    it(`${name}: HTML-only original quotes readable text, not markup`, async () => {
      const body = await open(htmlOnly());
      expect(body).toContain('Readable marketing content');
      expect(body).not.toContain('DOCTYPE');
      expect(body).not.toContain('&lt;style');
      expect(body).not.toContain('color:red');
    });

    it(`${name}: multipart/alternative quotes the text/plain part`, async () => {
      const body = await open(alternative());
      expect(body).toContain('Plain alternative text');
      expect(body).not.toContain('HTML alternative text');
    });

    it(`${name}: text-only original quotes its text`, async () => {
      expect(await open(textOnly())).toContain('Only plain text');
    });

    it(`${name}: HTML part between two text parts is converted in place`, async () => {
      const body = await open(htmlInMiddle());
      expect(body).toContain('Leading note');
      expect(body).toContain('Middle html part');
      expect(body).toContain('Trailing note');
      expect(body.indexOf('Leading note')).toBeLessThan(body.indexOf('Middle html part'));
      expect(body.indexOf('Middle html part')).toBeLessThan(body.indexOf('Trailing note'));
      expect(body).not.toContain('&lt;p&gt;');
      expect(body).not.toContain('&lt;b&gt;');
    });
  }
});
