/**
 * Tests for pure helpers and selection logic in the mail store.
 * Issue #36: * keyboard shortcut should toggle select-all.
 * Issue #40: duplicate emailIds in Thread must not reach the each-key.
 */
import { describe, it, expect } from 'vitest';
import { allVisibleSelected, resolveThreadEmails } from './store.svelte';
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
