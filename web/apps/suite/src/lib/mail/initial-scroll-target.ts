/**
 * Re #525: picks the email id ThreadReader should scroll into view when a
 * thread first opens, and enforces that the scroll fires at most once per
 * thread open.
 *
 * `initialExpandedIds` is `pickInitialExpanded`'s result for the thread
 * (always non-empty once the thread has at least one email), so its first
 * element is the first message of the initially-expanded set: the first
 * unread message when any are unread, or the thread's last message when
 * all are read.
 *
 * `alreadyFired` is the gate's own prior state for this thread open. The
 * caller keeps one gate instance per mount (i.e. per thread open) and
 * flips it to true as soon as this function returns a non-null id, so a
 * later reactive update within the same open (a pending arrival landing,
 * a dedupe-driven re-render) never re-scrolls a thread the user has
 * already scrolled -- or scrolled away from by hand.
 */
export function pickInitialScrollTarget(
  initialExpandedIds: readonly string[],
  alreadyFired: boolean,
): string | null {
  if (alreadyFired) return null;
  return initialExpandedIds[0] ?? null;
}
