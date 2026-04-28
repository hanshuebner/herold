/**
 * Tests for the pure formatting helpers exported from compose.svelte.ts
 * via `_internals_forTest`. These cover address parsing, subject
 * normalisation, References merging, and HTML <-> text projection —
 * the things that have to stay correct for reply / forward / quoted
 * body to render the way users expect.
 */
import { describe, it, expect } from 'vitest';
import { _internals_forTest } from './compose.svelte';
import type { Email } from '../mail/types';

const {
  parseAddressList,
  replySubject,
  forwardSubject,
  mergeReferences,
  addressToString,
  addressListToString,
  htmlToPlainText,
  escapeHtml,
  computeReplyAllCc,
  formatBytes,
  appendSignature,
} = _internals_forTest;

const ID_NO_SIG = {
  id: 'i1',
  name: 'Hans',
  email: 'h@x.test',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
};
const ID_WITH_SIG = { ...ID_NO_SIG, textSignature: 'Hans Hübner\nh@x.test' };

describe('parseAddressList', () => {
  it('splits commas and semicolons', () => {
    expect(parseAddressList('a@x.test, b@y.test;c@z.test')).toEqual([
      'a@x.test',
      'b@y.test',
      'c@z.test',
    ]);
  });

  it('strips Name <addr> wrappers', () => {
    expect(parseAddressList('Alice <a@x.test>, Bob <b@y.test>')).toEqual([
      'a@x.test',
      'b@y.test',
    ]);
  });

  it('skips empty entries', () => {
    expect(parseAddressList('a@x.test,, , ')).toEqual(['a@x.test']);
  });

  it('returns empty for an empty string', () => {
    expect(parseAddressList('')).toEqual([]);
  });
});

describe('replySubject / forwardSubject', () => {
  it('prepends Re: to a subject without one', () => {
    expect(replySubject('Project plan')).toBe('Re: Project plan');
  });

  it('does not double-prepend Re:', () => {
    expect(replySubject('Re: Project plan')).toBe('Re: Project plan');
    expect(replySubject('RE: Project plan')).toBe('RE: Project plan');
  });

  it('prepends Fwd: only once', () => {
    expect(forwardSubject('hi')).toBe('Fwd: hi');
    expect(forwardSubject('Fwd: hi')).toBe('Fwd: hi');
    expect(forwardSubject('Fw: hi')).toBe('Fw: hi');
  });

  it('handles null original subject', () => {
    expect(replySubject(null)).toBe('Re: ');
    expect(forwardSubject(null)).toBe('Fwd: ');
  });
});

describe('mergeReferences', () => {
  function email(refs: string[] | null, mid: string[] | null): Email {
    return {
      id: 'x',
      threadId: 'tx',
      mailboxIds: {},
      keywords: {},
      from: null,
      to: null,
      subject: null,
      preview: '',
      receivedAt: '2026-04-28T00:00:00Z',
      sentAt: null,
      hasAttachment: false,
      messageId: mid,
      references: refs,
    } as unknown as Email;
  }

  it('returns parent.references then parent.messageId', () => {
    const e = email(['<a@x>', '<b@x>'], ['<c@x>']);
    expect(mergeReferences(e)).toEqual(['<a@x>', '<b@x>', '<c@x>']);
  });

  it('handles missing references', () => {
    const e = email(null, ['<a@x>']);
    expect(mergeReferences(e)).toEqual(['<a@x>']);
  });

  it('handles missing messageId', () => {
    const e = email(['<a@x>'], null);
    expect(mergeReferences(e)).toEqual(['<a@x>']);
  });

  it('returns [] when neither is set', () => {
    const e = email(null, null);
    expect(mergeReferences(e)).toEqual([]);
  });
});

describe('addressToString / addressListToString', () => {
  it('renders Name <email> when name is present', () => {
    expect(addressToString({ name: 'Alice', email: 'a@x.test' })).toBe(
      'Alice <a@x.test>',
    );
  });

  it('renders bare email when name is empty', () => {
    expect(addressToString({ name: '', email: 'a@x.test' })).toBe('a@x.test');
    expect(addressToString({ name: null, email: 'a@x.test' })).toBe('a@x.test');
  });

  it('returns empty for undefined', () => {
    expect(addressToString(undefined)).toBe('');
  });

  it('joins a list with comma-space', () => {
    expect(
      addressListToString([
        { name: 'Alice', email: 'a@x.test' },
        { name: null, email: 'b@y.test' },
      ]),
    ).toBe('Alice <a@x.test>, b@y.test');
  });

  it('returns empty for null/empty list', () => {
    expect(addressListToString(null)).toBe('');
    expect(addressListToString([])).toBe('');
  });
});

describe('htmlToPlainText', () => {
  it('keeps inline text', () => {
    expect(htmlToPlainText('<p>hello world</p>').trim()).toBe('hello world');
  });

  it('inserts newlines for block elements', () => {
    const out = htmlToPlainText('<p>line one</p><p>line two</p>');
    expect(out).toContain('line one');
    expect(out).toContain('line two');
    expect(out.indexOf('line two')).toBeGreaterThan(out.indexOf('line one'));
  });

  it('renders <a href> with the link in parentheses when text differs', () => {
    expect(htmlToPlainText('<p>see <a href="https://x.test">site</a></p>'))
      .toContain('site (https://x.test)');
  });

  it('does not double-render when text equals href', () => {
    const url = 'https://x.test/';
    expect(htmlToPlainText(`<p><a href="${url}">${url}</a></p>`)).toContain(url);
  });

  it('renders <li> as "- " bullet', () => {
    const out = htmlToPlainText('<ul><li>one</li><li>two</li></ul>');
    expect(out).toMatch(/-\s+one[\s\S]*-\s+two/);
  });

  it('quotes <blockquote> with "> " prefix per line', () => {
    const out = htmlToPlainText('<blockquote>quoted line</blockquote>');
    expect(out).toContain('> quoted line');
  });
});

describe('escapeHtml', () => {
  it('escapes &, <, >, "', () => {
    expect(escapeHtml('<a href="x">&y</a>')).toBe(
      '&lt;a href=&quot;x&quot;&gt;&amp;y&lt;/a&gt;',
    );
  });
});

describe('computeReplyAllCc', () => {
  function emailWith(args: {
    from?: string;
    to?: string[];
    cc?: string[];
  }): Email {
    return {
      id: 'x',
      threadId: 't',
      mailboxIds: {},
      keywords: {},
      from: args.from ? [{ name: null, email: args.from }] : null,
      to: args.to?.map((email) => ({ name: null, email })) ?? null,
      cc: args.cc?.map((email) => ({ name: null, email })) ?? null,
      subject: null,
      preview: '',
      receivedAt: '2026-04-28T00:00:00Z',
      hasAttachment: false,
    } as unknown as Email;
  }

  it('includes parent To and Cc, excluding the From', () => {
    const parent = emailWith({
      from: 'alice@x.test',
      to: ['bob@y.test', 'carol@z.test'],
      cc: ['dave@w.test'],
    });
    const cc = computeReplyAllCc(parent, new Set());
    expect(cc.map((a) => a.email)).toEqual([
      'bob@y.test',
      'carol@z.test',
      'dave@w.test',
    ]);
  });

  it('excludes every Identity.email (case-insensitive)', () => {
    const parent = emailWith({
      from: 'alice@x.test',
      to: ['ME@self.test', 'bob@y.test'],
      cc: ['Other@Self.Test'],
    });
    const self = new Set(['me@self.test', 'other@self.test']);
    const cc = computeReplyAllCc(parent, self);
    expect(cc.map((a) => a.email)).toEqual(['bob@y.test']);
  });

  it('drops duplicates on lowercase email', () => {
    const parent = emailWith({
      from: 'alice@x.test',
      to: ['Bob@Y.Test', 'bob@y.test'],
      cc: ['BOB@y.test'],
    });
    const cc = computeReplyAllCc(parent, new Set());
    expect(cc).toHaveLength(1);
  });

  it('returns empty when only the From is on the message', () => {
    const parent = emailWith({ from: 'alice@x.test', to: [] });
    expect(computeReplyAllCc(parent, new Set())).toEqual([]);
  });

  it('handles missing addresses without throwing', () => {
    const parent = emailWith({});
    expect(computeReplyAllCc(parent, new Set())).toEqual([]);
  });
});

describe('appendSignature', () => {
  it('returns the body unchanged when identity is null', () => {
    expect(appendSignature('<p>hello</p>', null)).toBe('<p>hello</p>');
  });
  it('returns the body unchanged when textSignature is empty', () => {
    expect(appendSignature('<p>hello</p>', ID_NO_SIG)).toBe('<p>hello</p>');
  });
  it('returns the body unchanged when textSignature is whitespace only', () => {
    expect(
      appendSignature('<p>hello</p>', { ...ID_NO_SIG, textSignature: '   \n\n  ' }),
    ).toBe('<p>hello</p>');
  });
  it('appends the standard delimiter and the signature lines', () => {
    const out = appendSignature('<p>hello</p>', ID_WITH_SIG);
    // Cursor lands at top of editor by default — signature must be
    // BELOW the body so the user types above it (REQ-MAIL-101).
    expect(out.startsWith('<p>hello</p>')).toBe(true);
    // The standard `-- ` delimiter is present as its own paragraph.
    expect(out).toContain('<p>-- </p>');
    // Signature lines render as paragraphs.
    expect(out).toContain('Hans Hübner');
    expect(out).toContain('h@x.test');
  });
  it('escapes HTML metacharacters in the signature', () => {
    const out = appendSignature('', {
      ...ID_NO_SIG,
      textSignature: 'A&B <c>',
    });
    expect(out).toContain('A&amp;B &lt;c&gt;');
  });
});

describe('formatBytes', () => {
  it('formats bytes', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(900)).toBe('900 B');
  });
  it('formats kilobytes with one decimal', () => {
    expect(formatBytes(1024)).toBe('1.0 KB');
    expect(formatBytes(2048)).toBe('2.0 KB');
    expect(formatBytes(1536)).toBe('1.5 KB');
  });
  it('formats megabytes with one decimal', () => {
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB');
    expect(formatBytes(50 * 1024 * 1024)).toBe('50.0 MB');
  });
  it('formats gigabytes with two decimals', () => {
    expect(formatBytes(1024 * 1024 * 1024)).toBe('1.00 GB');
  });
});
