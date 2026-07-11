/**
 * Shared shift-click range-selection primitive for the Suite's selectable
 * lists (re #202).
 *
 * Every selectable list (mail, contacts, and any future list) selects rows
 * via a persistent checkbox per row: a plain click toggles that row's
 * membership in the selection set, independent of every other row -- the
 * additive multi-select gesture the app already relies on for bulk actions.
 * Shift-click instead replaces the selection with the contiguous range
 * between the last-clicked row (the "anchor") and the shift-clicked row,
 * inclusive -- the conventional desktop mail-client gesture. The anchor
 * itself does not move on a shift-click, so repeated shift-clicks keep
 * extending (or shrinking) the range from the same starting point.
 *
 * This module is pure and framework-agnostic (no `$state`, no store
 * dependency) so the range math is unit-tested once and shared verbatim by
 * every list store instead of being re-derived per view.
 */

/**
 * Compute the selection set produced by a shift-click on `clickedId`.
 *
 * `visibleIds` is the ordered list of ids currently rendered (the range is
 * always computed against what the user can see, never the full unpaged
 * result set). Falls back to selecting only `clickedId` when there is no
 * anchor yet, or the anchor is no longer present in `visibleIds` (e.g. a
 * search or scope change dropped it from view since it was last clicked).
 */
export function computeShiftClickRange(
  visibleIds: readonly string[],
  anchorId: string | null,
  clickedId: string,
): Set<string> {
  const anchorIdx = anchorId !== null ? visibleIds.indexOf(anchorId) : -1;
  const clickedIdx = visibleIds.indexOf(clickedId);
  if (anchorIdx === -1 || clickedIdx === -1) {
    return new Set([clickedId]);
  }
  const [start, end] = anchorIdx <= clickedIdx ? [anchorIdx, clickedIdx] : [clickedIdx, anchorIdx];
  return new Set(visibleIds.slice(start, end + 1));
}
