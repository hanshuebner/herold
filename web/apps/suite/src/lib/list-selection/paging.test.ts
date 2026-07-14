import { describe, it, expect } from 'vitest';
import { appendPage, canLoadMore } from './paging';

const idOf = (id: string): string => id;

describe('appendPage', () => {
  it('appends a full page and reports hasMore true (page-fullness signal)', () => {
    const result = appendPage(['a', 'b'], ['c', 'd'], { pageSize: 2, idOf });
    expect(result.items).toEqual(['a', 'b', 'c', 'd']);
    expect(result.hasMore).toBe(true);
  });

  it('appends a short page and reports hasMore false (page-fullness signal)', () => {
    const result = appendPage(['a', 'b'], ['c'], { pageSize: 2, idOf });
    expect(result.items).toEqual(['a', 'b', 'c']);
    expect(result.hasMore).toBe(false);
  });

  it('dedupes page items already present in currentItems', () => {
    const result = appendPage(['a', 'b'], ['b', 'c'], { pageSize: 2, idOf });
    expect(result.items).toEqual(['a', 'b', 'c']);
  });

  it('prefers the total signal over page-fullness when total is given', () => {
    // Page came back full (pageSize items) but total says nothing more.
    const result = appendPage(['a', 'b'], ['c', 'd'], { pageSize: 2, total: 4, idOf });
    expect(result.items).toEqual(['a', 'b', 'c', 'd']);
    expect(result.hasMore).toBe(false);
  });

  it('reports hasMore true from total when the loaded count is still below it', () => {
    const result = appendPage(['a', 'b'], ['c'], { pageSize: 2, total: 10, idOf });
    expect(result.hasMore).toBe(true);
  });

  it('works on non-string items via idOf', () => {
    interface Row { id: string; name: string }
    const current: Row[] = [{ id: '1', name: 'a' }];
    const page: Row[] = [{ id: '2', name: 'b' }];
    const result = appendPage(current, page, { pageSize: 1, idOf: (r) => r.id });
    expect(result.items.map((r) => r.id)).toEqual(['1', '2']);
    expect(result.hasMore).toBe(true);
  });
});

describe('canLoadMore', () => {
  it('is true only when ready, hasMore, and not already loading', () => {
    expect(canLoadMore({ isReady: true, hasMore: true, loadingMore: false })).toBe(true);
  });

  it('is false when not ready', () => {
    expect(canLoadMore({ isReady: false, hasMore: true, loadingMore: false })).toBe(false);
  });

  it('is false when there is nothing more to load', () => {
    expect(canLoadMore({ isReady: true, hasMore: false, loadingMore: false })).toBe(false);
  });

  it('is false while a page fetch is already in flight', () => {
    expect(canLoadMore({ isReady: true, hasMore: true, loadingMore: true })).toBe(false);
  });
});
