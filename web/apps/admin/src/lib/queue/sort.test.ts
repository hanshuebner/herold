import { describe, it, expect } from 'vitest';
import { compareByCreatedAtDesc, sortByCreatedAtDesc } from './sort';
import type { QueueItem } from './queue.svelte';

function makeItem(id: string, created_at?: string): QueueItem {
  return {
    id,
    principal_id: 'p1',
    mail_from: `${id}@sender.example`,
    rcpt_to: `${id}@rcpt.example`,
    envelope_id: `env-${id}`,
    state: 'queued',
    attempts: 0,
    created_at,
  };
}

describe('compareByCreatedAtDesc', () => {
  it('sorts newer timestamps before older ones', () => {
    const older = makeItem('a', '2024-01-01T00:00:00Z');
    const newer = makeItem('b', '2024-06-01T00:00:00Z');
    expect(compareByCreatedAtDesc(newer, older)).toBeLessThan(0);
    expect(compareByCreatedAtDesc(older, newer)).toBeGreaterThan(0);
  });

  it('places items with created_at before items without', () => {
    const withDate = makeItem('a', '2024-01-01T00:00:00Z');
    const noDate = makeItem('b');
    expect(compareByCreatedAtDesc(withDate, noDate)).toBeLessThan(0);
    expect(compareByCreatedAtDesc(noDate, withDate)).toBeGreaterThan(0);
  });

  it('falls back to ascending id tiebreak when timestamps are equal', () => {
    const ts = '2024-03-15T12:00:00Z';
    const alpha = makeItem('aaa', ts);
    const beta = makeItem('bbb', ts);
    expect(compareByCreatedAtDesc(alpha, beta)).toBeLessThan(0);
    expect(compareByCreatedAtDesc(beta, alpha)).toBeGreaterThan(0);
  });

  it('falls back to ascending id tiebreak when both items lack created_at', () => {
    const first = makeItem('alpha');
    const second = makeItem('beta');
    expect(compareByCreatedAtDesc(first, second)).toBeLessThan(0);
    expect(compareByCreatedAtDesc(second, first)).toBeGreaterThan(0);
  });

  it('returns 0 for identical items', () => {
    const item = makeItem('x', '2024-01-01T00:00:00Z');
    expect(compareByCreatedAtDesc(item, item)).toBe(0);
  });
});

describe('sortByCreatedAtDesc', () => {
  it('returns items sorted newest-first', () => {
    const items = [
      makeItem('c', '2024-01-01T00:00:00Z'),
      makeItem('a', '2024-12-31T23:59:59Z'),
      makeItem('b', '2024-06-15T08:00:00Z'),
    ];
    const sorted = sortByCreatedAtDesc(items);
    expect(sorted.map((i) => i.id)).toEqual(['a', 'b', 'c']);
  });

  it('places items without created_at after dated items', () => {
    const items = [
      makeItem('no-date'),
      makeItem('old', '2023-01-01T00:00:00Z'),
      makeItem('new', '2025-01-01T00:00:00Z'),
    ];
    const sorted = sortByCreatedAtDesc(items);
    expect(sorted.map((i) => i.id)).toEqual(['new', 'old', 'no-date']);
  });

  it('does not mutate the source array', () => {
    const items = [
      makeItem('b', '2024-02-01T00:00:00Z'),
      makeItem('a', '2024-01-01T00:00:00Z'),
    ];
    const original = [...items];
    sortByCreatedAtDesc(items);
    expect(items.map((i) => i.id)).toEqual(original.map((i) => i.id));
  });

  it('handles an empty array', () => {
    expect(sortByCreatedAtDesc([])).toEqual([]);
  });

  it('handles a single item', () => {
    const item = makeItem('only', '2024-01-01T00:00:00Z');
    expect(sortByCreatedAtDesc([item])).toEqual([item]);
  });

  it('uses id as a stable tiebreak for equal timestamps', () => {
    const ts = '2024-05-01T00:00:00Z';
    const items = [makeItem('zzz', ts), makeItem('aaa', ts), makeItem('mmm', ts)];
    const sorted = sortByCreatedAtDesc(items);
    expect(sorted.map((i) => i.id)).toEqual(['aaa', 'mmm', 'zzz']);
  });
});
