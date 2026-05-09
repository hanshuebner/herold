/**
 * Tests for the suite search-query parser. Two surfaces:
 *  - parseQuery: input string → JMAP filter shape (back end)
 *  - decodeChips: input string → UI chips (front end)
 *
 * decodeChips MUST round-trip the original token text in `raw` so the
 * search-route's chip strip can re-render the user's input verbatim.
 */
import { describe, it, expect } from 'vitest';
import { decodeChips, parseQuery } from './search-query';
import type { Mailbox } from './types';

const ctx = { mailboxes: new Map<string, Mailbox>() };

describe('decodeChips', () => {
  it('returns an empty array for an empty query', () => {
    expect(decodeChips('')).toEqual([]);
    expect(decodeChips('   ')).toEqual([]);
  });

  it('treats bareword tokens as text chips', () => {
    expect(decodeChips('hello')).toEqual([
      { raw: 'hello', operator: 'text', value: 'hello', label: 'hello' },
    ]);
  });

  it('parses operator:value pairs', () => {
    const chips = decodeChips('from:alice@x.test');
    expect(chips).toHaveLength(1);
    expect(chips[0]).toMatchObject({
      operator: 'from',
      value: 'alice@x.test',
      label: 'from: alice@x.test',
    });
  });

  it('renders has:attachment with a friendlier label', () => {
    expect(decodeChips('has:attachment')[0]?.label).toBe('has attachment');
  });

  it('renders is:unread with a friendlier label', () => {
    expect(decodeChips('is:unread')[0]?.label).toBe('is unread');
  });

  it('preserves quoted phrases as a single chip', () => {
    const chips = decodeChips('"weekly meeting"');
    expect(chips).toHaveLength(1);
    expect(chips[0]?.value).toBe('weekly meeting');
  });

  it('decodes a multi-token query in input order', () => {
    const chips = decodeChips('from:alice "weekly meeting" hello');
    expect(chips.map((c) => c.operator)).toEqual(['from', 'text', 'text']);
    expect(chips[0]?.value).toBe('alice');
    expect(chips[1]?.value).toBe('weekly meeting');
    expect(chips[2]?.value).toBe('hello');
  });
});

describe('parseQuery — operator surface', () => {
  it('text-only query returns a text filter', () => {
    expect(parseQuery('hello world', ctx)).toEqual({
      filter: {
        operator: 'AND',
        conditions: [{ text: 'hello' }, { text: 'world' }],
      },
      includesTrashOrJunk: false,
    });
  });

  it('from: → JMAP from filter', () => {
    expect(parseQuery('from:alice@x.test', ctx)).toEqual({
      filter: { from: 'alice@x.test' },
      includesTrashOrJunk: false,
    });
  });

  it('has:attachment → hasAttachment: true', () => {
    expect(parseQuery('has:attachment', ctx)).toEqual({
      filter: { hasAttachment: true },
      includesTrashOrJunk: false,
    });
  });

  it('is:unread → notKeyword $seen', () => {
    expect(parseQuery('is:unread', ctx)).toEqual({
      filter: { notKeyword: '$seen' },
      includesTrashOrJunk: false,
    });
  });

  it('label:work resolves via the mailbox map', () => {
    const mb: Mailbox = {
      id: 'mb-work',
      name: 'work',
      role: null,
      parentId: null,
      sortOrder: 0,
      totalEmails: 0,
      unreadEmails: 0,
      totalThreads: 0,
      unreadThreads: 0,
    };
    const m = new Map<string, Mailbox>();
    m.set('mb-work', mb);
    expect(parseQuery('label:work', { mailboxes: m })).toEqual({
      filter: { inMailbox: 'mb-work' },
      includesTrashOrJunk: false,
    });
  });

  it('unknown operator falls through to a text filter', () => {
    expect(parseQuery('weird:thing', ctx)).toEqual({
      filter: { text: 'weird:thing' },
      includesTrashOrJunk: false,
    });
  });

  it('combines operators with AND', () => {
    const result = parseQuery('from:alice has:attachment', ctx);
    expect(result.filter).toEqual({
      operator: 'AND',
      conditions: [{ from: 'alice' }, { hasAttachment: true }],
    });
  });
});

describe('parseQuery — in: operator and trash/junk opt-in (REQ-SRC-06/07)', () => {
  function ctxWithRoles(): { mailboxes: Map<string, Mailbox> } {
    const mb = (id: string, name: string, role: string | null): Mailbox => ({
      id,
      name,
      role,
      parentId: null,
      sortOrder: 0,
      totalEmails: 0,
      unreadEmails: 0,
      totalThreads: 0,
      unreadThreads: 0,
    });
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    m.set('mb-sent', mb('mb-sent', 'Sent', 'sent'));
    return { mailboxes: m };
  }

  it('bare text query does not opt into trash/junk', () => {
    const r = parseQuery('milfsdating', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(false);
    expect(r.filter).toEqual({ text: 'milfsdating' });
  });

  it('in:trash resolves to the trash mailbox and opts in', () => {
    const r = parseQuery('in:trash', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(true);
    expect(r.filter).toEqual({ inMailbox: 'mb-trash' });
  });

  it('in:junk resolves to the junk mailbox and opts in', () => {
    const r = parseQuery('in:junk', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(true);
    expect(r.filter).toEqual({ inMailbox: 'mb-junk' });
  });

  it('in:spam is an alias for in:junk', () => {
    const r = parseQuery('in:spam', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(true);
    expect(r.filter).toEqual({ inMailbox: 'mb-junk' });
  });

  it('in:anywhere contributes no condition but opts in', () => {
    const r = parseQuery('milfsdating in:anywhere', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(true);
    // Sentinel was filtered out, only the text term remains.
    expect(r.filter).toEqual({ text: 'milfsdating' });
  });

  it('in:inbox does NOT opt into trash/junk', () => {
    const r = parseQuery('in:inbox', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(false);
    expect(r.filter).toEqual({ inMailbox: 'mb-inbox' });
  });

  it('label:Junk opts in via name resolution', () => {
    const r = parseQuery('label:Junk', ctxWithRoles());
    expect(r.includesTrashOrJunk).toBe(true);
    expect(r.filter).toEqual({ inMailbox: 'mb-junk' });
  });
});
