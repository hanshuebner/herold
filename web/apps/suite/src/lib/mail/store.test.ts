/**
 * Tests for pure helpers and selection logic in the mail store.
 * Issue #36: * keyboard shortcut should toggle select-all.
 */
import { describe, it, expect } from 'vitest';
import { allVisibleSelected } from './store.svelte';

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
