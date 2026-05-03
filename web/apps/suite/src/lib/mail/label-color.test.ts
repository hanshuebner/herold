import { describe, it, expect } from 'vitest';
import { LABEL_PALETTE, randomLabelColor, labelForeground } from './label-color';

// WCAG 2.1 contrast ratio helper.
function contrast(l1: number, l2: number): number {
  const lighter = Math.max(l1, l2);
  const darker = Math.min(l1, l2);
  return (lighter + 0.05) / (darker + 0.05);
}

function relativeLuminance(hex: string): number {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  const lin = (c: number): number => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

const WHITE_L = 1.0;
const BLACK_L = 0.0;
const WCAG_AA_THRESHOLD = 3.0;

describe('LABEL_PALETTE', () => {
  it('contains 12 entries', () => {
    expect(LABEL_PALETTE.length).toBe(12);
  });

  it('all entries are well-formed #RRGGBB hex literals', () => {
    for (const c of LABEL_PALETTE) {
      expect(c).toMatch(/^#[0-9a-fA-F]{6}$/);
    }
  });

  it('all palette colours pass WCAG AA against white (contrast >= 3.0)', () => {
    for (const c of LABEL_PALETTE) {
      const L = relativeLuminance(c);
      const ratio = contrast(L, WHITE_L);
      expect(
        ratio,
        `${c} contrast vs white = ${ratio.toFixed(2)} < ${WCAG_AA_THRESHOLD}`,
      ).toBeGreaterThanOrEqual(WCAG_AA_THRESHOLD);
    }
  });

  it('all palette colours pass WCAG AA against black (contrast >= 3.0)', () => {
    for (const c of LABEL_PALETTE) {
      const L = relativeLuminance(c);
      const ratio = contrast(L, BLACK_L);
      expect(
        ratio,
        `${c} contrast vs black = ${ratio.toFixed(2)} < ${WCAG_AA_THRESHOLD}`,
      ).toBeGreaterThanOrEqual(WCAG_AA_THRESHOLD);
    }
  });
});

describe('randomLabelColor', () => {
  it('returns a value that is in the palette', () => {
    for (let i = 0; i < 50; i++) {
      const c = randomLabelColor();
      expect(LABEL_PALETTE).toContain(c);
    }
  });
});

describe('labelForeground', () => {
  it('returns white on a dark background', () => {
    // Very dark background — luminance near 0.
    expect(labelForeground('#000000')).toBe('#ffffff');
    expect(labelForeground('#1a1a2e')).toBe('#ffffff');
  });

  it('returns black on a light background', () => {
    // Very light background — luminance near 1.
    expect(labelForeground('#ffffff')).toBe('#000000');
    expect(labelForeground('#f0f0f0')).toBe('#000000');
  });

  it('returns white on a mid-dark background', () => {
    // #444444: L ~= 0.067, contrast vs white ~= 7.4, vs black ~= 1.4.
    expect(labelForeground('#444444')).toBe('#ffffff');
  });

  it('returns black on a mid-light background', () => {
    // #aaaaaa: L ~= 0.402, contrast vs white ~= 2.3, vs black ~= 4.6.
    expect(labelForeground('#aaaaaa')).toBe('#000000');
  });

  it('returns white as safe default for an invalid colour', () => {
    expect(labelForeground('red')).toBe('#ffffff');
    expect(labelForeground('')).toBe('#ffffff');
    expect(labelForeground('#zzzzzz')).toBe('#ffffff');
  });
});
