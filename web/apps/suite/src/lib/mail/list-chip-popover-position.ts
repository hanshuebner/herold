/**
 * Pure viewport geometry for the mailing-list chip's popover (issue #457).
 *
 * `ListChip.svelte` portals the popover to `document.body` and positions
 * it `fixed` from the chip button's measured rect, since it can no
 * longer rely on a `position: absolute` layout relationship inside the
 * clipped sender row (issue #415's `overflow: hidden`). Extracted here,
 * separate from the DOM plumbing, so the edge cases -- a chip near the
 * bottom, a chip near the top, a chip near the right edge, and a
 * viewport shorter than the popover itself -- can be asserted against
 * exact fixture rectangles without needing a real layout engine.
 *
 * Nothing here touches the DOM; callers supply already-measured rects
 * and sizes.
 */

export interface AnchorRect {
  top: number;
  bottom: number;
  left: number;
}

export interface PopoverSize {
  width: number;
  height: number;
}

export interface ViewportSize {
  width: number;
  height: number;
}

export interface PopoverPosition {
  top: number;
  left: number;
}

export interface PopoverPositionOptions {
  /** Gap between the chip and the popover (matches `--spacing-02`). */
  gap: number;
  /** Minimum distance kept from any viewport edge. */
  margin: number;
}

/**
 * Compute the `position: fixed` top/left for the popover given the chip
 * button's rect, the popover's own measured size, and the viewport.
 *
 * Horizontal: clamps the popover's left edge so it never runs past the
 * right or left edge of the viewport, same as before this function was
 * extracted.
 *
 * Vertical: prefers placing the popover below the chip -- the original,
 * pre-portal layout -- and flips it above only when below does not have
 * room AND above does. This order matters: flipping above unconditionally
 * whenever below does not fit would, for a chip near the top of the
 * viewport, place the popover at a negative `top` (the same defect this
 * function exists to fix, just moved to the opposite edge).
 *
 * When neither direction has room (a viewport shorter than the popover
 * itself), the popover is pinned to the top margin rather than centered
 * or split across both edges: a chip's popover leads with its actions,
 * so keeping the top -- and therefore as many leading actions as fit --
 * on screen is worth more than staying anchored to the trigger. The
 * remainder is accepted to run past the bottom edge in that case.
 */
export function computeListPopoverPosition(
  anchor: AnchorRect,
  popover: PopoverSize,
  viewport: ViewportSize,
  { gap, margin }: PopoverPositionOptions,
): PopoverPosition {
  const left = Math.max(
    margin,
    Math.min(viewport.width - popover.width - margin, anchor.left),
  );

  const spaceBelow = viewport.height - anchor.bottom - gap;
  const spaceAbove = anchor.top - gap;
  const top =
    spaceBelow >= popover.height
      ? anchor.bottom + gap
      : spaceAbove >= popover.height
        ? anchor.top - gap - popover.height
        : anchor.bottom + gap;
  const clampedTop = Math.max(
    margin,
    Math.min(viewport.height - popover.height - margin, top),
  );

  return { top: clampedTop, left };
}
