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
} = _internals_forTest;

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
