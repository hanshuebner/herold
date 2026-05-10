/**
 * REQ-SRC-06: default search scope excludes Trash + Junk via
 * applyTrashJunkExclusion. The helper wraps the parsed filter with
 * inMailboxOtherThan: [<trash-id>, <junk-id>] when those mailboxes
 * exist for the principal.
 */

import { describe, it, expect } from 'vitest';
import { applyTrashJunkExclusion } from './store.svelte';
import type { Mailbox } from './types';

function mb(id: string, name: string, role: string | null): Mailbox {
  return {
    id,
    name,
    role,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  };
}

describe('applyTrashJunkExclusion (REQ-SRC-06)', () => {
  it('splices inMailboxOtherThan into a flat text filter without wrapping in AND', () => {
    // A flat FilterCondition (no `operator` key) gets the exclusion
    // spliced in directly. RFC 8621 §5.5 makes a single FilterCondition
    // with multiple keys implicitly AND-ed, and the server's fast-path
    // gate (mergeFilterIntoOpts) only recognises the flat shape — see
    // REQ-PERF-INDEX-10.
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const out = applyTrashJunkExclusion({ text: 'foo' }, m);
    expect(out).not.toHaveProperty('operator');
    expect(out).toEqual({
      text: 'foo',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('splices inMailboxOtherThan into a flat before-filter without wrapping in AND', () => {
    // The reported bug shape (issue 2026-05-10): `before:2009-01-01`
    // produced `{operator: AND, conditions: [{before}, {inMailboxOtherThan}]}`
    // which the fast-pushability gate rejected, dropping the query to
    // the slow path and tripping the response deadline. After the fix
    // the shape is flat and the fast path engages.
    const m = new Map<string, Mailbox>();
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    const out = applyTrashJunkExclusion({ before: '2009-01-01T00:00:00Z' }, m);
    expect(out).not.toHaveProperty('operator');
    expect(out).toEqual({
      before: '2009-01-01T00:00:00Z',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('returns the filter unchanged when neither role exists', () => {
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    const filter = { text: 'foo' };
    expect(applyTrashJunkExclusion(filter, m)).toBe(filter);
  });

  it('returns the filter unchanged when neither role exists for an AND-shaped parsed filter', () => {
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    const filter = {
      operator: 'OR' as const,
      conditions: [{ from: 'alice' }, { from: 'bob' }],
    };
    expect(applyTrashJunkExclusion(filter, m)).toBe(filter);
  });

  it('wraps an operator-shaped parsed filter with AND', () => {
    // When `parsed` is itself a FilterOperator (`{operator, conditions}`)
    // we cannot splice a sibling key — operator-shaped objects do not
    // accept FilterCondition keys. Wrap with AND so semantics are
    // preserved; this shape will not engage the fast path until
    // REQ-PERF-INDEX-10's gate accepts it (only AND-of-flat-conjuncts
    // is currently pushable).
    const m = new Map<string, Mailbox>();
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    const inner = {
      operator: 'OR' as const,
      conditions: [{ from: 'alice' }, { from: 'bob' }],
    };
    const out = applyTrashJunkExclusion(inner, m);
    expect(out).toEqual({
      operator: 'AND',
      conditions: [inner, { inMailboxOtherThan: ['mb-trash', 'mb-junk'] }],
    });
  });

  it('wraps an AND-tree filter with AND (operator key is the only signal)', () => {
    const m = new Map<string, Mailbox>();
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    const inner = {
      operator: 'AND' as const,
      conditions: [{ from: 'alice' }, { hasAttachment: true }],
    };
    const out = applyTrashJunkExclusion(inner, m);
    expect(out).toEqual({
      operator: 'AND',
      conditions: [inner, { inMailboxOtherThan: ['mb-trash', 'mb-junk'] }],
    });
  });
});
