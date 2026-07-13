/**
 * Acceptance test for issue #231 ("HTML mail: sender background-color
 * with no color renders text invisible").
 *
 * Two prior patches (commits 444951a6, 9ed8756a) implemented the lone-
 * color-half guard as a hand-rolled scanner: split the style attribute
 * on bare ";", then pattern-match property names and color values with
 * regexes. Each patch closed one reported case and left others open;
 * the second one introduced a regression. This file asserts the
 * *visible* outcome -- the resolved paint colors an iframe actually
 * renders -- rather than the presence/absence of a substring in the
 * style attribute, because the bug (and the fix) is about what the
 * reader can see, not about the text of the sanitized markup.
 *
 * Root causes this test locks down:
 *
 *   1. `internal/extimg`'s server-side placeholder/internalize pass
 *      rewrites `url(...)` inside inline `style=""` attributes to
 *      `url(data:image/gif;base64,...)` -- standard, always-on
 *      behavior for every message still pending background
 *      internalize (`Email/get`, see internal/protojmap/mail/email/
 *      get.go and render.go calling extimg.RewriteForPlaceholder).
 *      The data URI's "image/gif;base64" segment contains a bare
 *      semicolon, which broke the old scanner's "split on ;" step:
 *      its own doc comment asserted color values "do not contain
 *      semicolons", which stopped being true the moment a background
 *      image sits behind that pass. FIXTURE_* constants below are
 *      captured verbatim from a live call to
 *      extimg.RewriteForPlaceholder (see the comment on each
 *      constant) so this test exercises the actual wire shape, not
 *      an idealized one.
 *   2. The scanner's named-color detector used a curated list of 41
 *      keywords out of the 148 CSS Color Level 4 names. A sender using
 *      an uncovered keyword (midnightblue, darkblue, ...) as a bare
 *      background color was invisible to the lone-half guard.
 *
 * The fix (see sanitizeInlineColorPairs in sanitize.ts) replaces the
 * scanner with the browser's own CSS parser: the declaration is
 * assigned to a detached element and the resolved `color` /
 * `background-color` are read back off its CSSStyleDeclaration. That
 * closes both defects structurally -- there is no keyword list to be
 * incomplete, and the parser tokenizes url(...) correctly regardless
 * of what characters its argument contains.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { Window } from 'happy-dom';
import type { Element as HappyElement } from 'happy-dom';
import { sanitizeHtml } from './sanitize';

// ─── Fixtures captured from the real extimg pipeline ───────────────────────

// Verified 2026-07-14 via:
//   extimg.RewriteForPlaceholder([]byte(
//     `<div style="background:url(https://tracker.example/bg.png) #1a1111 no-repeat">x</div>`))
// internal/protojmap/mail/email/get.go / render.go call exactly this
// function on every HTML body part whenever the message row has
// InternalizePending = true -- the default state of every freshly
// received message until the background internalize worker finishes.
// The literal "cid:" prefix ahead of "data:image/gif..." is a separate,
// pre-existing defect in cssRewriteURLs (it always prepends "cid:" even
// when the substituted value, here PlaceholderDataURI, is already a
// full URI, not a bare content-id) -- out of scope for #231 and
// deliberately left byte-for-byte as observed: the sanitizer's color-
// pair detection only inspects the declaration around url(...), never
// its argument, so the exact URL content is not load-bearing here. What
// IS load-bearing is the embedded ";" in "image/gif;base64", which is
// what broke the old scanner.
const PLACEHOLDER_URL =
  'url(cid:data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==)';

const FIXTURE_LONE_BG_DATA_URI = `background:${PLACEHOLDER_URL} #1a1111 no-repeat`;
const FIXTURE_COMPLETE_PAIR_DATA_URI = `background:${PLACEHOLDER_URL} #1a1111; color:#ffffff`;
const FIXTURE_COLORLESS_BG_DATA_URI = `background:${PLACEHOLDER_URL} no-repeat`;

// ─── Test-only color oracle (never used by production code) ────────────────
//
// A tiny keyword table and contrast-ratio calculator used solely to grade
// this test's own fixtures. This is not a stand-in for the sanitizer's own
// color detection (that now goes through the browser's CSS parser, see
// sanitize.ts) -- it only lets the test assert "this pixel is legible"
// numerically for keyword colors happy-dom's getComputedStyle does not
// resolve to rgb().
const NAMED_COLOR_RGB: Record<string, [number, number, number]> = {
  midnightblue: [25, 25, 112],
  darkblue: [0, 0, 139],
  darkred: [139, 0, 0],
  darkgreen: [0, 100, 0],
  dimgray: [105, 105, 105],
  darkslategray: [47, 79, 79],
  firebrick: [178, 34, 34],
  saddlebrown: [139, 69, 19],
  indianred: [205, 92, 92],
  sienna: [160, 82, 45],
};

function parseColor(value: string): [number, number, number, number] | null {
  const v = value.trim().toLowerCase();
  if (v === '' || v === 'initial' || v === 'transparent' || v === 'inherit') return null;
  const rgbMatch = /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(v);
  if (rgbMatch) {
    const [, r, g, b, alpha] = rgbMatch;
    const a = alpha !== undefined ? parseFloat(alpha) : 1;
    if (a === 0) return null; // fully transparent paints nothing
    return [parseInt(r!, 10), parseInt(g!, 10), parseInt(b!, 10), a];
  }
  const hex6Match = /^#([0-9a-f]{6})$/.exec(v);
  if (hex6Match) {
    const n = hex6Match[1]!;
    return [parseInt(n.slice(0, 2), 16), parseInt(n.slice(2, 4), 16), parseInt(n.slice(4, 6), 16), 1];
  }
  const hex3Match = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/.exec(v);
  if (hex3Match) {
    const [, r, g, b] = hex3Match;
    return [parseInt(r! + r, 16), parseInt(g! + g, 16), parseInt(b! + b, 16), 1];
  }
  const named = NAMED_COLOR_RGB[v];
  if (named) {
    const [r, g, b] = named;
    return [r, g, b, 1];
  }
  return null;
}

function relativeLuminance([r, g, b]: [number, number, number, number]): number {
  const chan = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * chan(r) + 0.7152 * chan(g) + 0.0722 * chan(b);
}

/** WCAG contrast ratio between two opaque colors, in [1, 21]. */
function contrastRatio(
  a: [number, number, number, number],
  b: [number, number, number, number],
): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const lighter = Math.max(la, lb);
  const darker = Math.min(la, lb);
  return (lighter + 0.05) / (darker + 0.05);
}

// ─── Rendering the sanitized output as a real (happy-dom) document ─────────
//
// sanitizeHtml's return value is set verbatim as an <iframe srcdoc> in
// HtmlBody.svelte -- the browser parses it as an independent document
// and resolves `@media (prefers-color-scheme)` against the OS/browser
// setting. Reproduce that here with a dedicated happy-dom Window per
// theme (separate from the ambient vitest environment's global
// `document`, which sanitizeHtml itself uses via DOMPurify).

let activeWindow: InstanceType<typeof Window> | null = null;

afterEach(() => {
  activeWindow?.close();
  activeWindow = null;
});

function renderSrcdoc(srcdoc: string, colorScheme: 'light' | 'dark') {
  const win = new Window({ settings: { device: { prefersColorScheme: colorScheme } } });
  activeWindow = win;
  const doc = win.document;
  const headMatch = /<head>([\s\S]*?)<\/head>/.exec(srcdoc);
  const bodyMatch = /<body>([\s\S]*?)<\/body>/.exec(srcdoc);
  if (!headMatch || !bodyMatch) {
    throw new Error('sanitizeHtml output did not contain <head>/<body> -- cannot render');
  }
  doc.head.innerHTML = headMatch[1]!;
  doc.body.innerHTML = bodyMatch[1]!;
  return { window: win, document: doc };
}

/** Computed `color`, parsed. Always resolvable -- `color` is inherited. */
function computedForeground(win: InstanceType<typeof Window>, el: HappyElement): [number, number, number, number] {
  const c = parseColor(win.getComputedStyle(el).color);
  if (!c) throw new Error(`unparseable computed color: "${win.getComputedStyle(el).color}"`);
  return c;
}

/**
 * Computed `background-color`, walking up through ancestors when the
 * element itself leaves it unset/transparent -- exactly what a browser
 * shows visually, since background-color is not an inherited CSS
 * property but an unset one lets ancestor backgrounds show through.
 */
function effectiveBackground(win: InstanceType<typeof Window>, el: HappyElement): [number, number, number, number] {
  let node: HappyElement | null = el;
  while (node) {
    const parsed = parseColor(win.getComputedStyle(node).backgroundColor);
    if (parsed) return parsed;
    node = node.parentElement;
  }
  throw new Error('no ancestor declared a resolvable background-color (missing body theme rule?)');
}

/** Minimum ratio for this suite's assertions: WCAG AA for normal text. */
const READABLE = 4.5;

function assertReadable(win: InstanceType<typeof Window>, el: HappyElement, label: string) {
  const fg = computedForeground(win, el);
  const bg = effectiveBackground(win, el);
  const ratio = contrastRatio(fg, bg);
  expect(
    ratio,
    `${label}: computed color=${win.getComputedStyle(el).color} vs effective background=rgb(${bg[0]},${bg[1]},${bg[2]}) -> contrast ${ratio.toFixed(2)}:1, want >= ${READABLE}:1`,
  ).toBeGreaterThanOrEqual(READABLE);
}

// ─── Herold's own paired theme colors (wrapInIframeDocument), for exact
//     fallback assertions ──────────────────────────────────────────────────
const THEME = {
  light: { color: [22, 22, 22, 1] as [number, number, number, number], background: [255, 255, 255, 1] as [number, number, number, number] },
  dark: { color: [244, 244, 244, 1] as [number, number, number, number], background: [22, 22, 22, 1] as [number, number, number, number] },
};

describe('issue #231 -- resolved paint colors are always readable', () => {
  const themes: Array<'light' | 'dark'> = ['light', 'dark'];

  for (const theme of themes) {
    describe(`${theme} theme`, () => {
      it('the originally reported case: lone background-color hex, no color', () => {
        const html = '<span style="background-color:#1a1111">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'lone background-color hex');
      });

      it('a named color OUTSIDE the old curated 41-keyword list: midnightblue', () => {
        const html = '<span style="background:midnightblue">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'midnightblue background, no color');
      });

      it('a named color OUTSIDE the old curated list: darkblue', () => {
        const html = '<span style="background:darkblue">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'darkblue background, no color');
      });

      it.each([
        'darkred', 'darkgreen', 'dimgray', 'darkslategray',
        'firebrick', 'saddlebrown', 'indianred', 'sienna',
      ])('further named colors missing from the old curated list: %s', (name) => {
        const html = `<span style="background:${name}">text</span>`;
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, `${name} background, no color`);
      });

      it('the data-URI shorthand case: background:url(data:...) #1a1111 no-repeat, no color (default pipeline)', () => {
        const html = `<div style="${FIXTURE_LONE_BG_DATA_URI}">text</div>`;
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('div')!;
        assertReadable(window, el, 'data-URI background shorthand, no color');
      });

      it('a colourless background:url(...) is left completely untouched', () => {
        const html = `<div style="${FIXTURE_COLORLESS_BG_DATA_URI}">text</div>`;
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('div')!;
        // No color component was ever declared -- the element must
        // still carry no explicit background-color of its own (the
        // rule must not misfire and strip a background-image-only
        // shorthand), so it inherits the iframe's own theme background.
        const computedBg = window.getComputedStyle(el).backgroundColor;
        expect(['', 'initial', 'transparent']).toContain(computedBg.trim().toLowerCase());
        assertReadable(window, el, 'colourless background:url(...)');
      });

      it('!important on a lone background shorthand', () => {
        const html = '<span style="background: #1a1111 !important">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, '!important lone background');
      });

      it('lone `color` with no background', () => {
        const html = '<span style="color:#f0f0f0">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'lone light color, no background');
      });
    });

    describe(`${theme} theme -- complete sender pairs are preserved exactly`, () => {
      it('a complete hex pair renders exactly as authored', () => {
        const html = '<span style="color:#ffffff;background-color:#000000">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
        expect(parseColor(window.getComputedStyle(el).backgroundColor)).toEqual([0, 0, 0, 1]);
      });

      it('REGRESSION (introduced by 9ed8756a): a complete pair with a data-URI background must survive intact, image included', () => {
        const html = `<span style="${FIXTURE_COMPLETE_PAIR_DATA_URI}">text</span>`;
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        // The sender's own pair (background #1a1111 / color #ffffff)
        // must render verbatim -- neither half stripped, and the
        // background-image layer of the shorthand still present.
        expect(parseColor(window.getComputedStyle(el).backgroundColor)).toEqual([26, 17, 17, 1]);
        expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
        expect(el.getAttribute('style') ?? '').toContain('url(');
      });
    });
  }

  it('a lone stripped half falls back to Herold\'s own paired theme colors, not a mismatched mix (light)', () => {
    const html = '<span style="background-color:#1a1111">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.light.color);
    expect(effectiveBackground(window, el)).toEqual(THEME.light.background);
  });

  it('a lone stripped half falls back to Herold\'s own paired theme colors, not a mismatched mix (dark)', () => {
    const html = '<span style="background-color:#1a1111">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'dark');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.dark.color);
    expect(effectiveBackground(window, el)).toEqual(THEME.dark.background);
  });
});
