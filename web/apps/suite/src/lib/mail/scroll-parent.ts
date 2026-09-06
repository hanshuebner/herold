/**
 * Returns the nearest scrollable ancestor of `el` in the same document.
 *
 * "Scrollable" means the element's computed `overflow-x` or `overflow-y`
 * is `auto` or `scroll`. The document element itself is excluded: it is
 * the viewport scroll container and is always reachable via window.scrollBy.
 * Returns `null` when no intermediate scrollable ancestor exists.
 *
 * Used by HtmlBody to locate the thread scroll container from inside the
 * iframe wheel-forwarding handler (issue #51).
 */
export function findScrollParent(el: HTMLElement): HTMLElement | null {
  const win = el.ownerDocument.defaultView;
  if (!win) return null;
  let parent = el.parentElement;
  while (parent && parent !== el.ownerDocument.documentElement) {
    const style = win.getComputedStyle(parent);
    if (
      style.overflowY === 'auto' ||
      style.overflowY === 'scroll' ||
      style.overflowX === 'auto' ||
      style.overflowX === 'scroll'
    ) {
      return parent;
    }
    parent = parent.parentElement;
  }
  return null;
}

/**
 * Computes the `scrollBy({ top })` delta that brings a fragment-link
 * target inside a sandboxed `srcdoc` iframe to the top edge of the
 * outer thread scroll container (issue #293).
 *
 * `targetRect` is the target element's `getBoundingClientRect()` as
 * read from inside the iframe -- relative to the iframe's own
 * (unscrolled, by design) viewport. Adding `frameRect.top` (the
 * iframe element's own position in the outer viewport) turns that into
 * an outer-viewport-relative coordinate, matching `parentRect`'s
 * coordinate space; subtracting `parentRect.top` gives the distance the
 * scroll container needs to move to bring the target to its top edge.
 */
export function fragmentScrollDelta(
  frameRect: { top: number },
  targetRect: { top: number },
  parentRect: { top: number },
): number {
  return frameRect.top + targetRect.top - parentRect.top;
}
