/**
 * Shared page-append + hasMore contract for the Suite's incrementally
 * loaded lists (re #221).
 *
 * Every paged list store (the mail folder list, mail search results, the
 * contacts list, and the contacts duplicate-check results) fetches a
 * fixed-size page starting at `position = <items already loaded>.length`,
 * appends the newly-fetched items to the ordered list (deduping against
 * items already present -- a defensive guard against a race with a
 * concurrent in-place reconciliation, e.g. a live sync push landing between
 * two `loadMore()` calls), and decides whether a further page likely exists
 * on the server.
 *
 * Two `hasMore` signals are in use across stores:
 *
 *   - page-fullness: the fetched page came back exactly `pageSize` long
 *     (mail folder/search paging, issue #161/#219) -- used when the
 *     server-side total is not tracked between pages.
 *   - loaded-vs-total: the loaded item count is still below a known
 *     `total` (contacts list paging, REQ-CONT-21, via `calculateTotal:
 *     true`).
 *
 * `appendPage` accepts an optional `total` and prefers it when present, so
 * every store composes the same function regardless of which signal it has
 * available for a given page fetch.
 */

export interface AppendPageResult<T> {
  items: T[];
  hasMore: boolean;
}

export interface AppendPageOptions<T> {
  /** The fixed page size requested from the server for this page. */
  pageSize: number;
  /**
   * The true total item count, when known (e.g. `Email/query` or
   * `Contact/query` with `calculateTotal: true`). When present, `hasMore`
   * is derived from `items.length < total` instead of page-fullness.
   */
  total?: number | null;
  /** Identity extractor used to dedupe `pageItems` against `currentItems`. */
  idOf: (item: T) => unknown;
}

/**
 * Append a freshly-fetched page onto the already-loaded item list.
 *
 * Items in `pageItems` whose identity (per `idOf`) already appears in
 * `currentItems` are dropped before appending -- the same overlap can occur
 * when a page fetch races an in-place list reconciliation; if that leaves
 * the local view slightly behind the server's true offset, the next
 * `loadMore()` call simply re-requests an overlapping page and dedupes
 * again, so the list never shows a duplicate or skipped row.
 */
export function appendPage<T>(
  currentItems: readonly T[],
  pageItems: readonly T[],
  opts: AppendPageOptions<T>,
): AppendPageResult<T> {
  const existing = new Set(currentItems.map(opts.idOf));
  const appended = pageItems.filter((item) => !existing.has(opts.idOf(item)));
  const items = [...currentItems, ...appended];
  const hasMore =
    opts.total != null ? items.length < opts.total : pageItems.length === opts.pageSize;
  return { items, hasMore };
}

/**
 * True when a `loadMore()` call should actually fetch a page: the list is
 * in its steady "ready" state, the server is believed to hold more (per
 * `appendPage`'s `hasMore`), and no page fetch is already in flight.
 */
export function canLoadMore(opts: {
  isReady: boolean;
  hasMore: boolean;
  loadingMore: boolean;
}): boolean {
  return opts.isReady && opts.hasMore && !opts.loadingMore;
}
