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

/**
 * The bulk-selection mutation a plain (non-shift) checkbox click just
 * performed on the anchor row: `'select'` if the click added the row to
 * the selection, `'deselect'` if it removed it (re #202 handback,
 * comment 3131). Shift-click extends this same operation across the
 * range instead of unconditionally selecting.
 */
export type SelectionOp = 'select' | 'deselect';

/**
 * Compute the selection produced by a shift-click, applying the anchor's
 * last plain-click operation (`anchorOp`) across the whole anchor-to-click
 * range instead of always selecting (re #202 handback, comment 3131): this
 * is the conventional desktop-list behaviour where the anchor carries both
 * a position and an operation, and shift extends that operation.
 *
 * `baseSelection` is the selection as it stood at the moment the anchor was
 * set by its plain click -- not the live selection -- so that repeated
 * shift-clicks from the same anchor keep replacing the range's extent
 * (shrinking it back down when the target moves closer to the anchor)
 * rather than accumulating every range ever visited in this shift session.
 * Ids outside the anchor-to-click range, which were already part of
 * `baseSelection`, are left untouched either way: a `'select'` operation
 * adds the range on top of them, a `'deselect'` operation removes only the
 * range's ids from them.
 *
 * Falls back to selecting only `clickedId` when there is no anchor (or the
 * anchor is no longer visible), matching `computeShiftClickRange`.
 */
export function applyShiftClickSelection(
  visibleIds: readonly string[],
  anchorId: string | null,
  anchorOp: SelectionOp,
  baseSelection: ReadonlySet<string>,
  clickedId: string,
): Set<string> {
  const range = computeShiftClickRange(visibleIds, anchorId, clickedId);
  if (anchorId === null || !visibleIds.includes(anchorId)) {
    return range;
  }
  const next = new Set(baseSelection);
  if (anchorOp === 'deselect') {
    for (const id of range) next.delete(id);
  } else {
    for (const id of range) next.add(id);
  }
  return next;
}

/**
 * Handle a row-checkbox click (plain or shift) for every selectable list
 * in one place (re #202).
 *
 * The checkbox's `checked` DOM property is a one-way `checked={...}`
 * binding sourced from the selection store. The browser's own checkbox
 * activation behaviour runs in two steps around the click event: a
 * "pre-click" step flips the element's internal checkedness before any
 * listener sees the event, and -- because this handler always calls
 * `preventDefault()` -- a "canceled activation" step reverts it again once
 * the click event has finished dispatching. Both steps mutate the
 * checkbox's internal checkedness directly (not through the exposed
 * `HTMLInputElement.prototype.checked` accessor), so neither one is
 * visible to, or preventable by, application code, and the revert step
 * measurably runs *after* the current task's microtask queue has already
 * drained (confirmed by instrumenting the accessor and racing a
 * microtask against a macrotask around a real click: the microtask still
 * observes the pre-click `true`, the macrotask observes the reverted
 * `false`). A framework-driven write made synchronously inside this
 * handler, or scheduled via `queueMicrotask`, therefore always lands
 * before the revert and gets silently clobbered by it -- the checkbox
 * then shows the wrong state even though the store's selection set is
 * correct, and no later store-driven re-render corrects it because the
 * framework's own reactivity cache already agrees with the (silently
 * reverted) target value. Re-asserting the DOM property one macrotask
 * later, once the revert has already happened, is what actually sticks.
 *
 * `applySelection` performs the store mutation (a plain click toggle or a
 * shift-click range); `isSelected` reads the resulting membership for this
 * row after that mutation.
 */
export function handleRowCheckboxClick(
  e: MouseEvent,
  applySelection: () => void,
  isSelected: () => boolean,
): void {
  e.preventDefault();
  e.stopPropagation();
  const el = e.currentTarget as HTMLInputElement;
  applySelection();
  setTimeout(() => {
    el.checked = isSelected();
  }, 0);
}

/**
 * Prune a selection Set back to ids present in `renderedIds` (re #202).
 *
 * The toolbar's selected-count display and every bulk action read the
 * selection Set directly. Nothing about a category-tab switch, a search
 * scope change, or a background folder/list refresh touches that Set on
 * its own -- they only change what's rendered -- so a dropped id can
 * silently linger in the Set with no on-screen checkbox: the toolbar count
 * then exceeds what's checked, and a bulk action would act on a row the
 * user can no longer see. Returns the same Set instance when nothing is
 * stale, so callers can skip a reactive write (and the checkbox re-render
 * churn that would cause) when there's nothing to reconcile.
 */
export function reconcileSelection(
  selected: ReadonlySet<string>,
  renderedIds: readonly string[],
): Set<string> {
  const rendered = new Set(renderedIds);
  let stale = false;
  for (const id of selected) {
    if (!rendered.has(id)) {
      stale = true;
      break;
    }
  }
  if (!stale) return selected as Set<string>;
  const next = new Set<string>();
  for (const id of selected) {
    if (rendered.has(id)) next.add(id);
  }
  return next;
}
