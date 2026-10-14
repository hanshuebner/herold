/**
 * Unit tests for pickInitialScrollTarget (re #525).
 *
 * The gate decides which email id ThreadReader scrolls into view when a
 * thread opens, and that it does so at most once per thread open.
 */

import { describe, it, expect } from 'vitest';
import { pickInitialScrollTarget } from './initial-scroll-target';

describe('pickInitialScrollTarget (re #525)', () => {
  it('returns the first id of the initially-expanded set when not yet fired', () => {
    expect(pickInitialScrollTarget(['e3', 'e4', 'e5'], false)).toBe('e3');
  });

  it('returns the sole id when a single message is initially expanded (unread at the end)', () => {
    expect(pickInitialScrollTarget(['e9'], false)).toBe('e9');
  });

  it('returns the sole id when a single unread message sits in the middle', () => {
    expect(pickInitialScrollTarget(['e4'], false)).toBe('e4');
  });

  it('returns the last message id when the thread is fully read (pickInitialExpanded falls back to it)', () => {
    expect(pickInitialScrollTarget(['e10'], false)).toBe('e10');
  });

  it('returns null once the gate has already fired, even with a non-empty set', () => {
    expect(pickInitialScrollTarget(['e3', 'e4', 'e5'], true)).toBeNull();
  });

  it('returns null for an empty initially-expanded set regardless of the gate', () => {
    expect(pickInitialScrollTarget([], false)).toBeNull();
    expect(pickInitialScrollTarget([], true)).toBeNull();
  });

  it('never fires twice across repeated calls once the caller latches the gate (simulated open)', () => {
    let fired = false;
    const first = pickInitialScrollTarget(['e1', 'e2'], fired);
    expect(first).toBe('e1');
    fired = fired || first !== null;

    // A later reactive update (e.g. a pending arrival landing) re-derives
    // the initially-expanded set but must not re-trigger the scroll.
    const second = pickInitialScrollTarget(['e1', 'e2', 'e3'], fired);
    expect(second).toBeNull();
  });
});
