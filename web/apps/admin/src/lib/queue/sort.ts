/**
 * Sorting helpers for queue items displayed in the Research view.
 *
 * These helpers operate on copies and never mutate the source array.
 * The Queue view retains the backend-returned order; only the Research
 * view applies this client-side sort.
 */

import type { QueueItem } from './queue.svelte';

/**
 * Compare two QueueItems for descending created_at order (newest first).
 *
 * Ordering contract:
 *   - Items with a parseable created_at come before items without one.
 *   - Among items with a created_at, later timestamps sort first.
 *   - Equal or absent timestamps fall back to ascending id comparison for
 *     a stable, deterministic tiebreak.
 */
export function compareByCreatedAtDesc(a: QueueItem, b: QueueItem): number {
  const ta = a.created_at ? new Date(a.created_at).getTime() : null;
  const tb = b.created_at ? new Date(b.created_at).getTime() : null;

  if (ta !== null && tb !== null) {
    if (tb !== ta) return tb - ta;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  }
  if (ta !== null) return -1;
  if (tb !== null) return 1;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * Return a new array containing all items from `items`, sorted newest-first
 * by created_at. The source array is not modified.
 */
export function sortByCreatedAtDesc(items: QueueItem[]): QueueItem[] {
  return [...items].sort(compareByCreatedAtDesc);
}
