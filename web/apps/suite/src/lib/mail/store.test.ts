/**
 * Tests for pure helpers and selection logic in the mail store.
 * Issue #36: * keyboard shortcut should toggle select-all.
 * Issue #40: duplicate emailIds in Thread must not reach the each-key.
 */
import { describe, it, expect } from 'vitest';
import { allVisibleSelected, expandToThreadIds, resolveThreadEmails } from './store.svelte';
import type { Email } from './types';

describe('allVisibleSelected', () => {
  it('returns false when visibleIds is empty', () => {
    expect(allVisibleSelected([], new Set(['a', 'b']))).toBe(false);
  });

  it('returns false when selection is empty and there are visible ids', () => {
    expect(allVisibleSelected(['a', 'b'], new Set())).toBe(false);
  });

  it('returns false when only some visible ids are selected', () => {
    expect(allVisibleSelected(['a', 'b', 'c'], new Set(['a', 'b']))).toBe(false);
  });

  it('returns true when every visible id is selected', () => {
    expect(allVisibleSelected(['a', 'b'], new Set(['a', 'b']))).toBe(true);
  });

  it('returns true when selection is a superset of visible ids', () => {
    // Selection may contain ids from a different tab/view; still counts as all-selected.
    expect(allVisibleSelected(['a'], new Set(['a', 'b', 'c']))).toBe(true);
  });

  it('returns false for a single visible id that is not selected', () => {
    expect(allVisibleSelected(['z'], new Set(['a']))).toBe(false);
  });
});

// Minimal email stub — only the id field matters for resolveThreadEmails.
function makeEmail(id: string): Email {
  return {
    id,
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: null,
    preview: '',
    receivedAt: '2024-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
  };
}

describe('resolveThreadEmails (issue #40)', () => {
  it('returns emails in emailIds order when there are no duplicates', () => {
    const emails = new Map([
      ['e1', makeEmail('e1')],
      ['e2', makeEmail('e2')],
      ['e3', makeEmail('e3')],
    ]);
    const result = resolveThreadEmails(['e1', 'e2', 'e3'], emails);
    expect(result.map((e) => e.id)).toEqual(['e1', 'e2', 'e3']);
  });

  it('deduplicates repeated emailIds, keeping the first occurrence', () => {
    const emails = new Map([
      ['e1', makeEmail('e1')],
      ['e2', makeEmail('e2')],
    ]);
    // Server returned e1 twice — previously caused each_key_duplicate in ThreadReader.
    const result = resolveThreadEmails(['e1', 'e2', 'e1'], emails);
    expect(result.map((e) => e.id)).toEqual(['e1', 'e2']);
  });

  it('skips ids that are not in the email cache', () => {
    const emails = new Map([['e1', makeEmail('e1')]]);
    const result = resolveThreadEmails(['e1', 'e2'], emails);
    expect(result.map((e) => e.id)).toEqual(['e1']);
  });

  it('returns an empty array when emailIds is empty', () => {
    const emails = new Map([['e1', makeEmail('e1')]]);
    expect(resolveThreadEmails([], emails)).toEqual([]);
  });

  it('returns an empty array when the cache has no matching emails', () => {
    const emails = new Map<string, Email>();
    expect(resolveThreadEmails(['e1', 'e2'], emails)).toEqual([]);
  });
});

// ── expandToThreadIds (REQ-MAIL-51, REQ-MAIL-52, REQ-MAIL-54) ───────────────
//
// Thread-scoped bulk operations (move, archive, delete) must hit every
// email in the affected thread, not just the row the user clicked. The
// helper computes the thread expansion; the bulk methods consume it.

function makeEmailInThread(id: string, threadId: string): Email {
  return { ...makeEmail(id), threadId };
}

describe('expandToThreadIds', () => {
  it('expands a single id to every email in its thread', () => {
    const emails = new Map<string, Email>([
      ['e1', makeEmailInThread('e1', 't1')],
      ['e2', makeEmailInThread('e2', 't1')],
      ['e3', makeEmailInThread('e3', 't1')],
    ]);
    const threads = new Map<string, { emailIds: readonly string[] }>([
      ['t1', { emailIds: ['e1', 'e2', 'e3'] }],
    ]);
    expect(expandToThreadIds(['e1'], threads, emails)).toEqual(['e1', 'e2', 'e3']);
  });

  it('expands multiple ids and deduplicates thread members', () => {
    const emails = new Map<string, Email>([
      ['e1', makeEmailInThread('e1', 't1')],
      ['e2', makeEmailInThread('e2', 't1')],
      ['e3', makeEmailInThread('e3', 't2')],
    ]);
    const threads = new Map<string, { emailIds: readonly string[] }>([
      ['t1', { emailIds: ['e1', 'e2'] }],
      ['t2', { emailIds: ['e3'] }],
    ]);
    // User selected e1 and e2 (same thread); both should expand once.
    expect(expandToThreadIds(['e1', 'e2'], threads, emails)).toEqual(['e1', 'e2']);
    // User selected one row from each of two threads.
    expect(expandToThreadIds(['e1', 'e3'], threads, emails)).toEqual(['e1', 'e2', 'e3']);
  });

  it('does not visit the same thread twice when multiple rows of it are selected', () => {
    const emails = new Map<string, Email>([
      ['e1', makeEmailInThread('e1', 't1')],
      ['e2', makeEmailInThread('e2', 't1')],
    ]);
    const threads = new Map<string, { emailIds: readonly string[] }>([
      ['t1', { emailIds: ['e1', 'e2'] }],
    ]);
    // Both selected rows are members of t1; the result is t1 once.
    const out = expandToThreadIds(['e2', 'e1'], threads, emails);
    expect(new Set(out)).toEqual(new Set(['e1', 'e2']));
    expect(out.length).toBe(2);
  });

  it('passes through ids whose thread is not loaded so partial caches still drive single-message ops', () => {
    const emails = new Map<string, Email>([
      ['e1', makeEmailInThread('e1', 't1')],
    ]);
    const threads = new Map<string, { emailIds: readonly string[] }>(); // empty
    expect(expandToThreadIds(['e1'], threads, emails)).toEqual(['e1']);
  });

  it('passes through unknown email ids', () => {
    const emails = new Map<string, Email>();
    const threads = new Map<string, { emailIds: readonly string[] }>();
    expect(expandToThreadIds(['unknown1', 'unknown2'], threads, emails)).toEqual([
      'unknown1',
      'unknown2',
    ]);
  });

  it('handles a thread record with an empty emailIds list (transient state) by passing the seed through', () => {
    const emails = new Map<string, Email>([['e1', makeEmailInThread('e1', 't1')]]);
    const threads = new Map<string, { emailIds: readonly string[] }>([
      ['t1', { emailIds: [] }],
    ]);
    expect(expandToThreadIds(['e1'], threads, emails)).toEqual(['e1']);
  });

  it('preserves stored thread order regardless of which row was the seed', () => {
    const emails = new Map<string, Email>([
      ['e1', makeEmailInThread('e1', 't1')],
      ['e2', makeEmailInThread('e2', 't1')],
      ['e3', makeEmailInThread('e3', 't1')],
    ]);
    const threads = new Map<string, { emailIds: readonly string[] }>([
      ['t1', { emailIds: ['e1', 'e2', 'e3'] }],
    ]);
    // Seed is the middle reply — output still reflects thread storage order.
    expect(expandToThreadIds(['e2'], threads, emails)).toEqual(['e1', 'e2', 'e3']);
  });
});
