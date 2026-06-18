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

import { describe, it, expect, afterEach, vi } from 'vitest';
import { svgFor } from './mail-favicon';

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
