/**
 * Label colour helpers (REQ-PROTO-56 / REQ-STORE-34).
 *
 * All helpers are pure functions with no side effects so they can be used
 * from store actions, components, and tests without DOM access.
 */

/**
 * Twelve opinionated mid-saturation hues that each pass WCAG AA contrast
 * against both #000000 and #ffffff.  A colour passes WCAG AA when its
 * contrast ratio against both anchors is >= 3.0 (large text / UI
 * components).  These values were chosen to sit between relative luminance
 * 0.18 and 0.42 where the contrast ratio against black is >= 3:1 and
 * against white is >= 3:1.
 *
 * Luminance range that satisfies both constraints simultaneously:
 *   contrast vs white = (1.05) / (L + 0.05) >= 3  =>  L <= 0.30
 *   contrast vs black = (L + 0.05) / (0.05)  >= 3  =>  L >= 0.10
 * All palette entries have L in [0.10, 0.30], satisfying both at >= 3:1.
 */
export const LABEL_PALETTE: readonly string[] = [
  '#c0392b', // crimson      L~0.14
  '#d35400', // burnt orange L~0.20
  '#b8860b', // amber        L~0.27
  '#1e8449', // forest green L~0.17
  '#148f77', // teal         L~0.21
  '#1f8a70', // emerald      L~0.20
  '#21618c', // ocean blue   L~0.11
  '#2980b9', // sky blue     L~0.19
  '#8e44ad', // purple       L~0.13
  '#9b59b6', // violet       L~0.18
  '#a04000', // brick        L~0.11
  '#5d6d7e', // steel        L~0.15
];

/**
 * Returns a random colour from LABEL_PALETTE.  The returned value is a
 * well-formed "#RRGGBB" hex literal accepted by the JMAP Mailbox.color
 * extension.
 */
export function randomLabelColor(): string {
  const idx = Math.floor(Math.random() * LABEL_PALETTE.length);
  // Non-null assertion is safe: idx is always in [0, LABEL_PALETTE.length).
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  return LABEL_PALETTE[idx]!;
}

/**
 * Parses a "#RRGGBB" hex literal into [r, g, b] in the range [0, 255].
 * Returns null when the string is not a valid 7-character hex colour.
 */
function parseHex(color: string): [number, number, number] | null {
  if (!/^#[0-9A-Fa-f]{6}$/.test(color)) return null;
  const r = parseInt(color.slice(1, 3), 16);
  const g = parseInt(color.slice(3, 5), 16);
  const b = parseInt(color.slice(5, 7), 16);
  return [r, g, b];
}

/**
 * Computes the relative luminance of an sRGB colour per WCAG 2.1 §1.4.
 * Input channels are in [0, 255].
 */
function relativeLuminance(r: number, g: number, b: number): number {
  const lin = (c: number): number => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/**
 * Returns the WCAG-AA foreground colour to use on top of `bg`.  The
 * returned value is either '#000000' (black text) or '#ffffff' (white text).
 * When `bg` is not a parseable "#RRGGBB" literal, white is returned as a
 * safe default.
 */
export function labelForeground(bg: string): '#000000' | '#ffffff' {
  const rgb = parseHex(bg);
  if (!rgb) return '#ffffff';
  const L = relativeLuminance(...rgb);
  // Contrast ratio vs white: (1.05) / (L + 0.05)
  // Contrast ratio vs black: (L + 0.05) / 0.05
  const contrastWhite = 1.05 / (L + 0.05);
  const contrastBlack = (L + 0.05) / 0.05;
  return contrastWhite >= contrastBlack ? '#ffffff' : '#000000';
}
