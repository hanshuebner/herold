import { describe, it, expect } from 'vitest';
import {
  allVisibleSelected,
  shouldOfferWholeSet,
  selectAllVisible,
  toggleSelectAllVisible,
} from './whole-set-selection';

describe('allVisibleSelected', () => {
  it('is false for an empty visible set', () => {
    expect(allVisibleSelected([], new Set(['a', 'b']))).toBe(false);
  });

  it('is false when nothing is selected', () => {
    expect(allVisibleSelected(['a', 'b'], new Set())).toBe(false);
  });

  it('is false when only some visible ids are selected', () => {
    expect(allVisibleSelected(['a', 'b', 'c'], new Set(['a', 'b']))).toBe(false);
  });

  it('is true when every visible id is selected', () => {
    expect(allVisibleSelected(['a', 'b'], new Set(['a', 'b']))).toBe(true);
  });

  it('is true when the selection is a superset of the visible ids', () => {
    expect(allVisibleSelected(['a'], new Set(['a', 'b', 'c']))).toBe(true);
  });
});

describe('shouldOfferWholeSet', () => {
  it('is false when total is unknown', () => {
    expect(shouldOfferWholeSet(['a', 'b'], new Set(['a', 'b']), null)).toBe(false);
  });

  it('is false when nothing is loaded yet', () => {
    expect(shouldOfferWholeSet([], new Set(), 10)).toBe(false);
  });

  it('is false when the total does not exceed the loaded count', () => {
    expect(shouldOfferWholeSet(['a', 'b'], new Set(['a', 'b']), 2)).toBe(false);
  });

  it('is false when not every loaded row is selected', () => {
    expect(shouldOfferWholeSet(['a', 'b'], new Set(['a']), 10)).toBe(false);
  });

  it('is true when every loaded row is selected and more exist on the server', () => {
    expect(shouldOfferWholeSet(['a', 'b'], new Set(['a', 'b']), 10)).toBe(true);
  });
});

describe('selectAllVisible', () => {
  it('selects exactly the visible ids', () => {
    expect(selectAllVisible(['a', 'b'])).toEqual(new Set(['a', 'b']));
  });
});

describe('toggleSelectAllVisible', () => {
  it('selects everything visible when nothing was selected', () => {
    expect(toggleSelectAllVisible(['a', 'b'], new Set())).toEqual(new Set(['a', 'b']));
  });

  it('selects everything visible when only some rows were selected', () => {
    expect(toggleSelectAllVisible(['a', 'b'], new Set(['a']))).toEqual(new Set(['a', 'b']));
  });

  it('clears the selection when everything visible was already selected', () => {
    expect(toggleSelectAllVisible(['a', 'b'], new Set(['a', 'b']))).toEqual(new Set());
  });
});
