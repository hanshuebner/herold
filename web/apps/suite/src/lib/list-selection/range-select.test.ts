import { describe, it, expect } from 'vitest';
import { computeShiftClickRange } from './range-select';

describe('computeShiftClickRange', () => {
  const ids = ['a', 'b', 'c', 'd', 'e'];

  it('selects only the clicked id when there is no anchor', () => {
    expect(computeShiftClickRange(ids, null, 'c')).toEqual(new Set(['c']));
  });

  it('selects only the clicked id when the anchor is no longer visible', () => {
    expect(computeShiftClickRange(ids, 'zzz', 'c')).toEqual(new Set(['c']));
  });

  it('selects the forward range from anchor to a later row, inclusive', () => {
    expect(computeShiftClickRange(ids, 'b', 'd')).toEqual(new Set(['b', 'c', 'd']));
  });

  it('selects the backward range when the clicked row precedes the anchor', () => {
    expect(computeShiftClickRange(ids, 'd', 'b')).toEqual(new Set(['b', 'c', 'd']));
  });

  it('selects a single row when the anchor equals the clicked id', () => {
    expect(computeShiftClickRange(ids, 'c', 'c')).toEqual(new Set(['c']));
  });

  it('selects the full range when anchor and clicked id are the first and last rows', () => {
    expect(computeShiftClickRange(ids, 'a', 'e')).toEqual(new Set(ids));
  });
});
