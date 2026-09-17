/**
 * Coordinate math for the G16 inline-image download overlay (issue #311,
 * #410).
 *
 * `HtmlBody.svelte` renders one `<a class="img-download">` per resolved
 * inline `<img>`, absolutely positioned in the OUTER page above the
 * sandboxed iframe: a small square anchored to the image's top-right
 * corner (REQ-ATT-26), sized and placed by `overlayButtonRect` +
 * `downloadButtonRect` below.
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

/**
 * Side length (px) of the discoverable download control laid over an
 * inline image's top-right corner (issue #410).
 */
export const DOWNLOAD_BUTTON_SIZE = 44;

/**
 * Shrink a full-image rect (as returned by `overlayButtonRect`) to the
 * small square, anchored to the image's top-right corner, that carries the
 * download affordance (issue #410). Clamped to the image's own dimensions
 * so it never exceeds a small image.
 *
 * The rest of the image's area is left uncovered by the caller so a click
 * there reaches the sender's own HTML underneath -- the wrapping `<a>`
 * when the image is a link, or HtmlBody's own click-to-lightbox listener
 * when it is not. A prior version of this overlay covered the full image
 * area, which put every click on a linked inline image onto the download
 * button instead of the sender's link.
 */
export function downloadButtonRect(imageRect: RectLike): RectLike {
  const size = Math.max(0, Math.min(DOWNLOAD_BUTTON_SIZE, imageRect.width, imageRect.height));
  return {
    top: imageRect.top,
    left: imageRect.left + imageRect.width - size,
    width: size,
    height: size,
  };
}
