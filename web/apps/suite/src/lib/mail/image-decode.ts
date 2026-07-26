/**
 * Detection helpers for undecodable inline (`cid:`) images (issue #269).
 *
 * REQ-MAIL-21 / REQ-ATT-26 reconciliation: an inline image that renders
 * normally stays out of the attachment chip strip (it is already visible in
 * the body). An inline image the browser cannot decode gets a download chip
 * so its original bytes are still reachable. Two independent signals feed
 * that decision, combined by the caller (MessageAccordion.svelte):
 *
 *   1. A content-type hint (`isKnownNonRenderableImageType`): a narrow
 *      denylist of MIME types no evergreen desktop browser decodes in an
 *      `<img>` element. TIFF is the issue #269 reproduction case (Apple
 *      Mail "Pasted Graphic" signatures). This is a static, synchronous
 *      signal available immediately, before the image has even started
 *      loading — it avoids a layout flash where a doomed-to-fail image
 *      shows no chip for the brief window before the runtime check lands.
 *
 *   2. A runtime signal (`inlineImageDecodeStatus`): the actual `<img>`
 *      element's `complete` / `naturalWidth` / `naturalHeight` state (and,
 *      in HtmlBody.svelte, its `error` event) after the browser has tried
 *      to decode it. This is authoritative — it reflects what THIS browser
 *      actually managed to render, so a type-hinted image that decodes fine
 *      (e.g. Safari's native TIFF support) is un-flagged, and a type not on
 *      the static list that still fails to decode is caught anyway.
 *
 * The static hint is deliberately narrow (see NON_RENDERABLE_IMAGE_TYPES):
 * it exists only to pre-empt the flash described above for a known-bad
 * format, not to substitute for the runtime signal.
 */

const NON_RENDERABLE_IMAGE_TYPES = new Set([
  'image/tiff',
  'image/tif',
  'image/x-tiff',
]);

/**
 * True when `type` (a MIME type, optionally with a `; charset=...` or
 * similar parameter suffix) is on the static denylist of image formats no
 * evergreen desktop browser decodes in an `<img>` element.
 */
export function isKnownNonRenderableImageType(
  type: string | undefined | null,
): boolean {
  if (!type) return false;
  const bare = type.toLowerCase().split(';', 1)[0]?.trim();
  return bare !== undefined && NON_RENDERABLE_IMAGE_TYPES.has(bare);
}

/** The three states an `<img>` element's decode attempt can be in. */
export type ImageDecodeStatus = 'pending' | 'success' | 'failure';

/**
 * Pure classification of an `<img>` element's decode outcome from its DOM
 * state, extracted so it is unit-testable without a real iframe/browser
 * image pipeline (happy-dom does not fire real image load/error events).
 *
 * `complete` is true once the browser has finished attempting to load the
 * resource (successfully or not); `naturalWidth`/`naturalHeight` are 0 when
 * the browser could not decode the bytes into a displayable image, even
 * though the fetch itself succeeded (the TIFF case: the bytes downloaded
 * fine, decoding into pixels is what fails).
 */
export function inlineImageDecodeStatus(img: {
  complete: boolean;
  naturalWidth: number;
  naturalHeight: number;
}): ImageDecodeStatus {
  if (!img.complete) return 'pending';
  if (img.naturalWidth > 0 && img.naturalHeight > 0) return 'success';
  return 'failure';
}
