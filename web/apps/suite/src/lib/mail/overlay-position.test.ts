/**
 * Acceptance test for issue #311 ("HTML mail: clicking a text link navigates
 * to the download URL of an unrelated inline image").
 *
 * Uses the exact rects measured against a live browser (puppeteer, real
 * `getBoundingClientRect()` semantics for a same-origin iframe) while
 * reproducing the report: a LinkedIn messaging-digest thread whose iframe
 * sits 210px down / 256px right of the browser viewport (typical once the
 * app header and thread-list sidebar are accounted for), containing a
 * 40x40 avatar image near the top and a 300x150 promo image further down.
 */
import { describe, it, expect } from 'vitest';
import { overlayButtonRect } from './overlay-position';

// Measured via puppeteer against a running dev instance: wrapper and frame
// coincide (the iframe is the only element in the wrapper's normal flow).
const wrapperRect = { top: 210, left: 256, width: 948, height: 900 };
const frameRect = { top: 210, left: 256, width: 948, height: 900 };

describe('issue #311 -- overlayButtonRect places the download button over its own image', () => {
  it('the avatar image (near the iframe top) lands at its true on-page position', () => {
    // img.getBoundingClientRect() as read from INSIDE the iframe -- relative
    // to the iframe's own viewport, not the outer page's.
    const imgRect = { top: 1, left: 1, width: 40, height: 40 };
    const rect = overlayButtonRect(wrapperRect, frameRect, imgRect, 0);
    // True on-page position = frameRect.top/left + imgRect.top/left.
    // Position is returned wrapper-relative, so subtract wrapperRect.
    expect(rect.top).toBe(frameRect.top + imgRect.top - wrapperRect.top);
    expect(rect.left).toBe(frameRect.left + imgRect.left - wrapperRect.left);
    expect(rect.width).toBe(40);
    expect(rect.height).toBe(40);
  });

  it('a promo image further down does NOT get shifted up onto earlier content', () => {
    // The exact iframe-local rect of the promo image in the reproducing
    // fixture: 230px down, 350px in from the iframe's own left edge.
    const imgRect = { top: 230, left: 350, width: 300, height: 150 };
    const rect = overlayButtonRect(wrapperRect, frameRect, imgRect, 0);

    // Convert back to page-absolute coordinates the way HtmlBody's caller
    // does (wrapperRect.top/left + the returned wrapper-relative rect) and
    // assert it equals the image's actual on-page position -- NOT the
    // pre-fix value (imgRect.top - frameRect.top = 20, landing near the
    // very top of the message where the reported headline link sits).
    const pageTop = wrapperRect.top + rect.top;
    const pageLeft = wrapperRect.left + rect.left;
    expect(pageTop).toBe(frameRect.top + imgRect.top); // 440, not 230.
    expect(pageLeft).toBe(frameRect.left + imgRect.left); // 606, not 350.

    // The pre-fix formula's result is a concrete regression guard: it must
    // no longer be what this function returns.
    const buggyTop = imgRect.top - frameRect.top; // -- prior computeOverlay() bug.
    expect(pageTop).not.toBe(wrapperRect.top + buggyTop);
  });

  it('a non-zero iframe scrollY is still added on top of the frame offset', () => {
    const imgRect = { top: 50, left: 10, width: 20, height: 20 };
    const rect = overlayButtonRect(wrapperRect, frameRect, imgRect, 15);
    expect(rect.top).toBe(frameRect.top - wrapperRect.top + imgRect.top + 15);
  });
});
