/**
 * Coordinate math for the G16 inline-image download overlay (issue #311).
 *
 * `HtmlBody.svelte` renders one `<a class="img-download">` per resolved
 * inline `<img>`, absolutely positioned in the OUTER page above the
 * sandboxed iframe, sized and placed to cover that image so a single
 * click downloads it (REQ-ATT-26).
 */

/** The subset of `DOMRect` this module reads. */
export interface RectLike {
  top: number;
  left: number;
  width: number;
  height: number;
}

/**
 * Position/size, relative to the wrapper element, for the download button
 * covering one inline `<img>`.
 *
 * `imgRect` is `img.getBoundingClientRect()` read on an element inside
 * `frame.contentDocument` -- per the DOM spec this is relative to the
 * IFRAME's OWN viewport, not the outer page's: a nested browsing context
 * has its own coordinate origin, which happens to coincide on-screen with
 * the iframe element's own content-box top-left corner. Converting that
 * iframe-local rect to a wrapper-relative one needs exactly ONE additive
 * term -- how far the iframe's own top-left sits from the wrapper's
 * (`frameRect` vs `wrapperRect`, both read in the OUTER page) -- not a
 * second subtraction of the iframe's own page-relative position.
 *
 * A prior version of this function subtracted `frameRect.top`/
 * `frameRect.left` a second time, which shifted every overlay button up
 * and left by exactly the iframe's own position in the browser viewport.
 * Whenever that shift happened to land a button over unrelated earlier
 * content in the message, that content's own click became indistinguishable
 * from a click on the image -- reported as "clicking a text link downloads
 * an unrelated inline image" against a LinkedIn messaging-digest mail
 * (#311), reproduced by measuring `elementFromPoint` at the clicked text's
 * on-screen position and finding the mispositioned overlay anchor there.
 */
export function overlayButtonRect(
  wrapperRect: RectLike,
  frameRect: RectLike,
  imgRect: RectLike,
  frameScrollY: number,
): { top: number; left: number; width: number; height: number } {
  const frameOffsetTop = frameRect.top - wrapperRect.top;
  const frameOffsetLeft = frameRect.left - wrapperRect.left;
  return {
    top: frameOffsetTop + imgRect.top + frameScrollY,
    left: frameOffsetLeft + imgRect.left,
    width: imgRect.width,
    height: imgRect.height,
  };
}
