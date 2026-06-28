/**
 * Unit tests for mail-favicon.ts.
 *
 * The pure SVG-generation path (svgFor) is the natural seam to test because
 * it depends on no DOM APIs other than window.matchMedia.  The rasterize /
 * setMailFavicon path uses canvas.toDataURL which is not available in
 * happy-dom; those paths are therefore excluded from this suite.
 *
 * Covered:
 *   - Badge text for 1-digit counts.
 *   - Badge text for 2-digit counts.
 *   - Counts > 99 render as "99+" (3-char badge).
 *   - count === 0 produces no badge element.
 *   - Both 16px and 32px sizes produce valid SVG.
 *   - Dark-mode stroke is lighter than the light-mode stroke.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest';
import { svgFor, _internals_forTest } from './mail-favicon';
import type { RasterizeFn } from './mail-favicon';

// Restore any matchMedia mock between tests.
afterEach(() => {
  vi.restoreAllMocks();
});

function lightMode(): void {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
}

function darkMode(): void {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query === '(prefers-color-scheme: dark)',
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
}

describe('svgFor badge text', () => {
  it('renders the count as text for a single-digit count', () => {
    lightMode();
    const svg = svgFor(32, 5);
    expect(svg).toContain('>5<');
  });

  it('renders the count as text for a two-digit count', () => {
    lightMode();
    const svg = svgFor(32, 42);
    expect(svg).toContain('>42<');
  });

  it('renders "99+" for counts above 99', () => {
    lightMode();
    const svg = svgFor(32, 100);
    expect(svg).toContain('>99+<');
  });

  it('renders "99+" for exactly 99+1 = 100', () => {
    lightMode();
    expect(svgFor(16, 100)).toContain('>99+<');
  });

  it('renders "99+" for very large counts', () => {
    lightMode();
    expect(svgFor(32, 9999)).toContain('>99+<');
  });

  it('renders "99" (not "99+") for exactly 99', () => {
    lightMode();
    const svg = svgFor(32, 99);
    expect(svg).toContain('>99<');
    expect(svg).not.toContain('>99+<');
  });
});

describe('svgFor: count === 0 produces no badge', () => {
  it('does not contain the badge fill color when count is 0', () => {
    lightMode();
    // The badge uses fill="#fa5252"; absence means no badge was appended.
    expect(svgFor(16, 0)).not.toContain('#fa5252');
    expect(svgFor(32, 0)).not.toContain('#fa5252');
  });

  it('still produces a valid svg element when count is 0', () => {
    lightMode();
    const svg = svgFor(32, 0);
    expect(svg).toMatch(/^<svg /);
    expect(svg).toContain('</svg>');
  });
});

describe('svgFor: size variants', () => {
  it('produces valid SVG for size 16', () => {
    lightMode();
    const svg = svgFor(16, 7);
    expect(svg).toMatch(/^<svg /);
    expect(svg).toContain('</svg>');
    expect(svg).toContain('>7<');
  });

  it('produces valid SVG for size 32', () => {
    lightMode();
    const svg = svgFor(32, 7);
    expect(svg).toMatch(/^<svg /);
    expect(svg).toContain('</svg>');
    expect(svg).toContain('>7<');
  });
});

describe('svgFor: dark-mode stroke flip', () => {
  it('uses the light stroke color in light mode', () => {
    lightMode();
    const svg = svgFor(32, 0);
    expect(svg).toContain('#222a3d');
    expect(svg).not.toContain('#e9ecef');
  });

  it('uses the dark stroke color in dark mode', () => {
    darkMode();
    const svg = svgFor(32, 0);
    expect(svg).toContain('#e9ecef');
    expect(svg).not.toContain('#222a3d');
  });
});

// ── Latest-wins race protection (re #71) ──────────────────────────────────
//
// setMailFaviconWith is the injectable-rasterizer form of setMailFavicon.
// These tests verify that when two calls overlap, only the most-recent
// result reaches the DOM. The rasterizer is replaced with a manually-
// controlled mock so the test can hold a call mid-flight while a second
// call completes, then release the first and confirm its result is dropped.
//
// canvas.toDataURL is not available in happy-dom so the real rasterize()
// function cannot be used here; that is why _internals_forTest exposes
// setMailFaviconWith with a pluggable rasterizer.

describe('setMailFavicon: latest-wins race protection', () => {
  // Helpers to build rasterizers with controlled timing.

  // A rasterizer whose calls never resolve until resolveAll() is called.
  function deferredRasterizer(): {
    rasterizeFn: RasterizeFn;
    resolveAll: () => void;
  } {
    const pending: Array<() => void> = [];
    return {
      rasterizeFn: (size, _count) =>
        new Promise<{ size: number; url: string } | null>((resolve) => {
          pending.push(() => resolve({ size, url: `data:image/png;tag=stale` }));
        }),
      resolveAll(): void {
        pending.forEach((r) => r());
      },
    };
  }

  // A rasterizer that resolves immediately with a tagged URL.
  function immediateRasterizer(tag: string): RasterizeFn {
    return (size, _count) => Promise.resolve({ size, url: `data:image/png;tag=${tag}` });
  }

  function iconHrefs(): string[] {
    return [...document.querySelectorAll("link[rel~='icon']")].map(
      (l) => (l as HTMLLinkElement).href,
    );
  }

  beforeEach(() => {
    _internals_forTest.resetSeq();
    // Remove any icon links left over from a previous test.
    document.querySelectorAll("link[rel~='icon']").forEach((l) => l.remove());
  });

  it('applies a single call normally', async () => {
    await _internals_forTest.setMailFaviconWith(3, immediateRasterizer('only'));
    const hrefs = iconHrefs();
    expect(hrefs.length).toBeGreaterThan(0);
    expect(hrefs.every((h) => h.includes('only'))).toBe(true);
  });

  it('discards a stale in-flight result when a newer call completes first', async () => {
    // Start a slow call (count=1); it rasterizes but does not resolve yet.
    const { rasterizeFn: slowFn, resolveAll } = deferredRasterizer();
    const stalePromise = _internals_forTest.setMailFaviconWith(1, slowFn);

    // Start and complete a faster call (count=2).
    await _internals_forTest.setMailFaviconWith(2, immediateRasterizer('fresh'));

    // favicon should show the fresh (count=2) result.
    expect(iconHrefs().every((h) => h.includes('fresh'))).toBe(true);

    // Now release the stale (count=1) rasterization.
    resolveAll();
    await stalePromise;

    // The stale result must have been dropped — DOM still reflects fresh.
    const hrefs = iconHrefs();
    expect(hrefs.every((h) => !h.includes('stale'))).toBe(true);
    expect(hrefs.some((h) => h.includes('fresh'))).toBe(true);
  });

  it('last call wins when both resolve in rapid succession', async () => {
    // Both calls are immediate but run concurrently.
    const first = _internals_forTest.setMailFaviconWith(1, immediateRasterizer('first'));
    const second = _internals_forTest.setMailFaviconWith(2, immediateRasterizer('second'));
    await Promise.all([first, second]);

    // The second call incremented the seq counter, so the first's DOM write
    // was guarded out. Only the second's hrefs should appear.
    const hrefs = iconHrefs();
    expect(hrefs.every((h) => !h.includes('first'))).toBe(true);
    expect(hrefs.some((h) => h.includes('second'))).toBe(true);
  });
});
