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
