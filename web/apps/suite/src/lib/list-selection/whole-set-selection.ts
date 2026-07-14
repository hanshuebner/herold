/**
 * Shared whole-set-selection contract for the Suite's selectable lists
 * (re #221).
 *
 * A selectable list's checkbox selection normally targets only the ids
 * currently loaded into `visibleIds` (`selectedIds` is a subset of what's
 * on screen). Once every loaded row is selected and the list's true
 * `total` exceeds what's loaded, a list can additionally offer a virtual
 * "select all N matching" mode: the store flips a `wholeSetSelected` flag
 * and routes bulk actions against the full server-side result set instead
 * of the loaded window, without ever fetching every row into memory. This
 * two-phase model -- loaded-page selection, then an opt-in virtual
 * whole-set mode -- originates in the mail store's whole-mailbox selection
 * (issue #149) and whole-search-result selection (issue #207); every list
 * store composes it from here rather than re-deriving it.
 *
 * This module only covers the pure "which ids are checked" half of that
 * model (the loaded-page part). The `wholeSetSelected` flag itself, and
 * how a store resolves it into a full-set id enumeration or a
 * server-side scoped query, stay store-specific -- what counts as "the
 * full matching set" differs between a mailbox filter and a contacts
 * address-book/group/search scope.
 */

/**
 * True when every id in `visibleIds` is present in `selected` AND
 * `visibleIds` is non-empty.
 */
export function allVisibleSelected(
  visibleIds: readonly string[],
  selected: ReadonlySet<string>,
): boolean {
  if (visibleIds.length === 0) return false;
  return visibleIds.every((id) => selected.has(id));
}

/**
 * True when the "select all N matching" affordance should be offered:
 * every loaded row is selected and the true total exceeds the loaded
 * count, so a whole-set mode has something further to select.
 */
export function shouldOfferWholeSet(
  visibleIds: readonly string[],
  selected: ReadonlySet<string>,
  total: number | null,
): boolean {
  if (total === null) return false;
  if (visibleIds.length === 0) return false;
  if (total <= visibleIds.length) return false;
  return allVisibleSelected(visibleIds, selected);
}

/** Selection set produced by "select all visible" -- every loaded row, nothing more. */
export function selectAllVisible(visibleIds: readonly string[]): Set<string> {
  return new Set(visibleIds);
}

/**
 * Selection set produced by toggling "select all visible": clears if
 * everything visible is already selected, otherwise selects everything
 * visible.
 */
export function toggleSelectAllVisible(
  visibleIds: readonly string[],
  selected: ReadonlySet<string>,
): Set<string> {
  return allVisibleSelected(visibleIds, selected) ? new Set() : new Set(visibleIds);
}
