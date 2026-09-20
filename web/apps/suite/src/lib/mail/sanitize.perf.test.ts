/**
 * Acceptance test for issue #441 ("Colour sanitizer's ancestor walk scales
 * near-quadratically with nesting depth on table-based HTML mail").
 *
 * `resolveEffectiveForeground` / `resolveEffectiveBackground` (as they
 * existed before this fix) each walked from an element up to the fragment
 * wrapper looking for a declared counterpart, per element -- so a chain of
 * N nested elements that each declare a lone color half cost O(N) per
 * element, O(N^2) overall. `sanitize.ts` now carries the inherited pair
 * top-down through a single traversal (`InheritedColorPair`,
 * `resolveDeclaredColorPair`) instead, making resolution O(1) per element.
 *
 * This test builds exactly the shape the issue measured -- a chain of N
 * nested `<div>`s, each declaring its own lone `background-color` (so
 * `collectDescendantOwnColors`'s lookahead stops at the very next nested
 * element and contributes no extra cost of its own, per the issue's own
 * measurement) -- and asserts the cost at 4x the nesting depth is nowhere
 * near 4x^2 = 16x the cost, comparing sanitize time at depth 100 against
 * depth 400.
 *
 * The assertion compares a RATIO, not an absolute duration, and allows
 * generous slack (an 8x ceiling, sitting almost exactly between the 4x a
 * linear implementation predicts and the 16x a quadratic one would
 * produce) specifically so this stays robust on a loaded CI host: what it
 * must never again do is grow like the reported 18/119/496/1844 ms series
 * (roughly a 100x blowup from depth 50 to depth 400, not the ~8x an O(N)
 * implementation predicts).
 */
import { describe, it, expect } from 'vitest';
import { sanitizeHtml } from './sanitize';

function buildNestedBackgroundFixture(depth: number): string {
  let html = 'leaf text';
  for (let i = 0; i < depth; i++) {
    const color = i % 2 === 0 ? '#1a1111' : '#111a11';
    html = `<div style="background-color:${color}">${html}</div>`;
  }
  return html;
}

/**
 * Best-of-`samples` sanitize time for a fixture of `depth` nesting levels,
 * in milliseconds. Taking the minimum (rather than a mean) discards GC
 * pauses and scheduler jitter that would otherwise inflate the smaller,
 * faster measurement disproportionately and understate the ratio's
 * headroom -- the failure mode this test exists to catch (a quadratic
 * regression) shows up as a change in the RATE of growth, not as one slow
 * sample, so minimizing incidental noise on both sides is the conservative
 * choice for a ratio-based assertion.
 */
function fastestSanitizeMillis(depth: number, samples: number): number {
  const html = buildNestedBackgroundFixture(depth);
  let best = Infinity;
  for (let i = 0; i < samples; i++) {
    const start = performance.now();
    sanitizeHtml(html, { loadImages: false });
    const elapsed = performance.now() - start;
    if (elapsed < best) best = elapsed;
  }
  return best;
}

describe('sanitizeHtml color-pair resolution scales linearly with nesting depth (issue #441)', () => {
  it('depth 400 costs nowhere near 16x depth 100 (quadratic) -- allows up to 8x', () => {
    // Warm up the JIT before measuring so the first sample's compilation
    // cost does not skew the depth-100 baseline.
    fastestSanitizeMillis(20, 3);

    const smallDepth = 100;
    const largeDepth = 400;
    const smallElapsed = fastestSanitizeMillis(smallDepth, 7);
    const largeElapsed = fastestSanitizeMillis(largeDepth, 7);

    // A linear implementation predicts ~4x (largeDepth / smallDepth). A
    // quadratic one predicts ~16x. The 8x ceiling sits between the two --
    // comfortably clear of linear noise, comfortably short of quadratic.
    const ratio = largeElapsed / Math.max(smallElapsed, 1);
    expect(
      ratio,
      `depth ${smallDepth} took ${smallElapsed.toFixed(2)}ms, depth ${largeDepth} took ` +
        `${largeElapsed.toFixed(2)}ms (ratio ${ratio.toFixed(2)}x) -- growth looks quadratic`,
    ).toBeLessThan(8);
  });
});
