/**
 * Fixture tests for `computeListPopoverPosition` (issue #457).
 *
 * Each fixture is a real geometry an actual browser can produce; the
 * expected `{ top, left }` is computed by hand against the rule stated
 * in list-chip-popover-position.ts's doc comment, not read back from the
 * implementation. Every fixture is paired with the "naive" value a
 * simpler (and wrong) implementation would produce, so a regression
 * that reintroduces the naive behaviour is caught by the assertion, not
 * just by inspection.
 */

import { describe, it, expect } from 'vitest';
import { computeListPopoverPosition } from './list-chip-popover-position';

const GAP = 4;
const MARGIN = 4;
const opts = { gap: GAP, margin: MARGIN };

describe('computeListPopoverPosition: bottom edge', () => {
  it('flips the popover above a chip near the bottom of the viewport, fully on screen', () => {
    // Mirrors the live-measured defect geometry: a 900-tall viewport,
    // chip bottom at y=895 (only 1px of room below after the gap).
    const anchor = { top: 875, bottom: 895, left: 300 };
    const popover = { width: 180, height: 120 };
    const viewport = { width: 1000, height: 900 };

    const result = computeListPopoverPosition(anchor, popover, viewport, opts);

    // Naive (pre-fix) behaviour: always place below, ignoring viewport
    // height -- this is what produced the reported defect (top=899,
    // bottom=1019, entirely past the 900-tall viewport).
    const naiveTop = anchor.bottom + GAP;
    expect(naiveTop).toBe(899);

    expect(result.top).toBe(751); // anchor.top - gap - popover.height = 875-4-120
    expect(result.top).not.toBe(naiveTop);
    expect(result.top).toBeGreaterThanOrEqual(MARGIN);
    expect(result.top + popover.height).toBeLessThanOrEqual(viewport.height - MARGIN);
  });
});

describe('computeListPopoverPosition: top edge', () => {
  it('does not flip into negative space for a chip near the top when neither direction fully fits', () => {
    // A chip 15px from the top of a 120-tall viewport; a 100-tall
    // popover fits below the chip only barely (121 - 100 = 21) short by
    // 19px, and fits above not at all.
    const anchor = { top: 15, bottom: 35, left: 300 };
    const popover = { width: 180, height: 100 };
    const viewport = { width: 1000, height: 120 };

    const result = computeListPopoverPosition(anchor, popover, viewport, opts);

    // Naive "flip above whenever below does not fit, unconditionally"
    // behaviour: top - gap - height = 15 - 4 - 100 = -89, deep in
    // negative (off-screen-above) territory -- the same defect as the
    // bottom-edge case, moved to the opposite edge.
    const naiveFlippedTop = anchor.top - GAP - popover.height;
    expect(naiveFlippedTop).toBe(-89);

    expect(result.top).toBe(16); // clamped: viewport.height - popover.height - margin = 120-100-4
    expect(result.top).not.toBe(naiveFlippedTop);
    expect(result.top).toBeGreaterThanOrEqual(0);
    expect(result.top).toBeGreaterThanOrEqual(MARGIN);
    expect(result.top + popover.height).toBeLessThanOrEqual(viewport.height - MARGIN);
  });
});

describe('computeListPopoverPosition: right edge', () => {
  it('clamps the left edge so the popover stays inside the viewport for a chip near the right', () => {
    const anchor = { top: 400, bottom: 420, left: 950 };
    const popover = { width: 220, height: 120 };
    const viewport = { width: 1000, height: 900 };

    const result = computeListPopoverPosition(anchor, popover, viewport, opts);

    // Naive (unclamped) behaviour: left = anchor.left = 950, right edge
    // at 950+220=1170, 170px past the 1000-wide viewport.
    const naiveLeft = anchor.left;
    expect(naiveLeft + popover.width).toBeGreaterThan(viewport.width);

    expect(result.left).toBe(776); // viewport.width - popover.width - margin = 1000-220-4
    expect(result.left).not.toBe(naiveLeft);
    expect(result.left + popover.width).toBeLessThanOrEqual(viewport.width - MARGIN);
  });
});

describe('computeListPopoverPosition: viewport shorter than the popover', () => {
  it('pins to the top margin rather than splitting overflow across both edges', () => {
    // A 200-tall popover cannot fit inside an 80-tall viewport at all,
    // regardless of the chip's position.
    const anchor = { top: 30, bottom: 50, left: 300 };
    const popover = { width: 180, height: 200 };
    const viewport = { width: 1000, height: 80 };

    const result = computeListPopoverPosition(anchor, popover, viewport, opts);

    // A "naive centre or split" clamp would place top such that the
    // popover overflows both edges symmetrically (e.g. a negative top
    // half the excess above zero); this implementation instead pins the
    // leading edge (and therefore the popover's first, most important
    // actions) on screen.
    expect(result.top).toBe(MARGIN);
    expect(result.top).toBeGreaterThanOrEqual(0);
    // The remainder is accepted to run past the bottom edge in this
    // case -- there is no position that avoids it entirely.
    expect(result.top + popover.height).toBeGreaterThan(viewport.height);
  });
});
