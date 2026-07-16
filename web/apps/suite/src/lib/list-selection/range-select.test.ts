import { describe, it, expect, vi } from 'vitest';
import { computeShiftClickRange, reconcileSelection, handleRowCheckboxClick } from './range-select';

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

describe('reconcileSelection (re #202)', () => {
  it('returns the same Set instance when nothing is stale', () => {
    const selected = new Set(['a', 'b']);
    const result = reconcileSelection(selected, ['a', 'b', 'c']);
    expect(result).toBe(selected);
  });

  it('drops ids no longer present in the rendered list', () => {
    const selected = new Set(['a', 'b', 'c']);
    const result = reconcileSelection(selected, ['a', 'c']);
    expect(result).toEqual(new Set(['a', 'c']));
    expect(result).not.toBe(selected);
  });

  it('drops every id when nothing selected is still rendered', () => {
    const selected = new Set(['x', 'y']);
    const result = reconcileSelection(selected, ['a', 'b']);
    expect(result).toEqual(new Set());
  });

  it('is a no-op on an empty selection', () => {
    const selected = new Set<string>();
    const result = reconcileSelection(selected, ['a', 'b']);
    expect(result).toBe(selected);
  });
});

describe('handleRowCheckboxClick (re #202)', () => {
  /**
   * Build a fake checkbox click event whose `currentTarget` is a minimal
   * stand-in for an HTMLInputElement (no real DOM needed for this pure
   * dispatch-order test).
   */
  function fakeClickEvent(): { event: { preventDefault: () => void; stopPropagation: () => void; currentTarget: { checked: boolean } }; el: { checked: boolean } } {
    const el = { checked: false };
    const event = {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      currentTarget: el,
    };
    return { event, el };
  }

  it('calls preventDefault and stopPropagation synchronously', () => {
    const { event } = fakeClickEvent();
    handleRowCheckboxClick(event as unknown as MouseEvent, vi.fn(), () => true);
    expect(event.preventDefault).toHaveBeenCalledTimes(1);
    expect(event.stopPropagation).toHaveBeenCalledTimes(1);
  });

  it('applies the selection mutation synchronously, before the deferred DOM write', () => {
    const { event } = fakeClickEvent();
    const order: string[] = [];
    const applySelection = vi.fn(() => order.push('applySelection'));
    handleRowCheckboxClick(event as unknown as MouseEvent, applySelection, () => {
      order.push('isSelected');
      return true;
    });
    // applySelection runs synchronously inside the handler; isSelected (and
    // the DOM write) is deferred to a macrotask, not called yet.
    expect(order).toEqual(['applySelection']);
  });

  it('writes the checkbox checked property from a macrotask, after the browser own synchronous revert would have run', () => {
    vi.useFakeTimers();
    const { event, el } = fakeClickEvent();
    handleRowCheckboxClick(event as unknown as MouseEvent, vi.fn(), () => true);
    // Not yet applied synchronously.
    expect(el.checked).toBe(false);
    vi.runAllTimers();
    expect(el.checked).toBe(true);
    vi.useRealTimers();
  });

  it('reads the post-mutation selection state, not a value captured before the mutation', () => {
    vi.useFakeTimers();
    const { event, el } = fakeClickEvent();
    let selected = false;
    handleRowCheckboxClick(
      event as unknown as MouseEvent,
      () => {
        selected = true;
      },
      () => selected,
    );
    vi.runAllTimers();
    expect(el.checked).toBe(true);
    vi.useRealTimers();
  });
});
