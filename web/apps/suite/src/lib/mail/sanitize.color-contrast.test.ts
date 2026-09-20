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
 *
 * Issue #422 broadened the lone-half rule, but only for a lone half that
 * pairs with an ancestor's or the carried-over `<body>`'s own declared
 * counterpart: that case is now stripped only on an actual collision
 * (WCAG contrast below sanitize.ts's `DEGENERATE_CONTRAST_THRESHOLD`), not
 * unconditionally. A lone half with NO ancestor/body counterpart anywhere
 * in the message -- #231's original scenario, nothing anywhere says what
 * the other half should be -- keeps #231's original, stricter bar: full
 * WCAG AA (`sanitize.ts`'s `WCAG_AA_CONTRAST_THRESHOLD`, mirrored below as
 * `WCAG_AA_MIN_CONTRAST`), so it survives only when it was already legible
 * on its own, and is otherwise stripped to the theme's own (highly legible)
 * pair. Every fixture in the "issue #231" describe block below is this
 * theme-only-fallback case, so `assertReadable` asserts the full AA bar for
 * them; the "issue #422" describe block's fixtures all declare their
 * counterpart on an ancestor or `<body>`, so they assert exact equality to
 * the authored color instead (that pair is preserved verbatim, not merely
 * "readable").
 *
 * `sanitizeHtml` itself resolves the reading pane's theme via the
 * *ambient* `getComputedStyle(document.documentElement).colorScheme` at
 * sanitize time -- the CSS property Herold's own theme setting
 * (`tokens.css`) sets on `<html>`, which the rendered `<iframe srcdoc>`
 * inherits its own `prefers-color-scheme` resolution from (verified live:
 * the OS-level `window.matchMedia` and the app's own theme can disagree,
 * and the iframe follows the app's theme). `withTheme` below sets that
 * ambient CSS property for the duration of a test so both the sanitize-
 * time decision and the `renderSrcdoc` window agree on which theme is
 * active, exactly as they always do in the running app.
 */
import { describe, it, expect, afterEach, beforeEach } from 'vitest';
import { Window } from 'happy-dom';
import type { Element as HappyElement } from 'happy-dom';
import { sanitizeHtml } from './sanitize';

/**
 * Set the ambient `document.documentElement`'s `color-scheme` so
 * `sanitizeHtml`'s own theme-fallback resolution (`readingPaneTheme` in
 * sanitize.ts) agrees with the theme this test renders with. Reset by the
 * file-level `afterEach`.
 */
function withTheme(theme: 'light' | 'dark'): void {
  document.documentElement.style.colorScheme = theme;
}

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
  red: [255, 0, 0],
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
  document.documentElement.style.removeProperty('color-scheme');
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

/**
 * Minimum ratio for the "issue #231" describe block's assertions. Mirrors
 * sanitize.ts's own `WCAG_AA_CONTRAST_THRESHOLD` (4.5). Every fixture in
 * that block is a lone half with NO ancestor or `<body>` counterpart
 * anywhere in the message, so `sanitizeInlineColorPairs` holds it to full
 * WCAG AA rather than the looser `DEGENERATE_CONTRAST_THRESHOLD` (1.5)
 * used for a lone half that pairs with an ancestor/`<body>` (issue #422,
 * asserted separately below with exact-equality checks, not this helper):
 * a fixture here either was already legible at >= 4.5:1 as authored and
 * survives untouched, or fails AA and is stripped to Herold's own paired
 * theme colors, which clear AA by a wide margin -- so in both outcomes the
 * rendered contrast this helper checks is guaranteed to be at least 4.5:1.
 */
const WCAG_AA_MIN_CONTRAST = 4.5;

function assertReadable(win: InstanceType<typeof Window>, el: HappyElement, label: string) {
  const fg = computedForeground(win, el);
  const bg = effectiveBackground(win, el);
  const ratio = contrastRatio(fg, bg);
  expect(
    ratio,
    `${label}: computed color=${win.getComputedStyle(el).color} vs effective background=rgb(${bg[0]},${bg[1]},${bg[2]}) -> contrast ${ratio.toFixed(2)}:1, want >= ${WCAG_AA_MIN_CONTRAST}:1`,
  ).toBeGreaterThanOrEqual(WCAG_AA_MIN_CONTRAST);
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
      beforeEach(() => withTheme(theme));

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

      it('DEGENERATE PAIR: background-color:currentColor paired with an explicit color paints solid-on-solid', () => {
        // Adversarial case found by independent verification: both halves
        // resolve to a non-empty CSSOM value, so a naive "both declared"
        // check accepts this as a complete pair -- but currentColor ties
        // the background to whatever `color` resolves to, so it is
        // red-on-red (or whatever color) by construction.
        const html = '<span style="color:red;background-color:currentColor">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'color:red; background-color:currentColor');
      });

      it('DEGENERATE PAIR, symmetric case: color:currentColor paired with an explicit background', () => {
        // color:currentColor is a no-op pass-through to the inherited
        // color -- pairing it with an explicit background-color is
        // effectively a lone background declaration.
        const html = '<span style="color:currentColor;background-color:#1a1111">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'color:currentColor; background-color:#1a1111');
      });

      it('both sides currentColor: a pure no-op pair, must not survive as an empty/no-op style', () => {
        const html = '<span style="color:currentColor;background-color:currentColor;font-weight:bold">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'color:currentColor; background-color:currentColor');
        expect(el.getAttribute('style') ?? '').not.toContain('currentColor');
      });

      it('SAME COLOR, different syntax: color:#f00 and background-color:#ff0000 are the identical color', () => {
        // The general contrast-based rule (not just the currentColor
        // special case): two independently declared, syntactically
        // different values that resolve to the same paint color are
        // exactly as invisible as currentColor.
        const html = '<span style="color:#f00;background-color:#ff0000">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        assertReadable(window, el, 'color:#f00; background-color:#ff0000 (same color, different syntax)');
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

      it('a legitimate LOW-but-adequate-contrast sender pair is preserved, not over-stripped', () => {
        // white on a medium brand blue: ~3.34:1 -- below WCAG AA for
        // normal text (4.5:1) but a real, deliberate, readable design
        // choice (a common button/badge style), and well above the
        // DEGENERATE_CONTRAST_THRESHOLD (1.5:1). Proves the general
        // contrast-based degenerate check does not over-strip ordinary
        // low-contrast styling -- only colors that are the same or
        // nearly so.
        const html = '<span style="color:#ffffff;background-color:#4a90d9">text</span>';
        const srcdoc = sanitizeHtml(html, { loadImages: false });
        const { window, document } = renderSrcdoc(srcdoc, theme);
        const el = document.querySelector('span')!;
        expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
        expect(parseColor(window.getComputedStyle(el).backgroundColor)).toEqual([0x4a, 0x90, 0xd9, 1]);
      });
    });
  }

  it('a lone half that collides with the theme falls back to Herold\'s own paired theme colors, not a mismatched mix (light)', () => {
    withTheme('light');
    const html = '<span style="background-color:#1a1111">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.light.color);
    expect(effectiveBackground(window, el)).toEqual(THEME.light.background);
  });

  // Follow-up to issue #422's own fix: a lone `darkred` background with NO
  // pair anywhere in the message (no ancestor, no <body>) is a theme-only
  // fallback -- darkred against the light theme's near-black default text
  // (#161616) is ~1.6:1, comfortably above `DEGENERATE_CONTRAST_THRESHOLD`
  // (1.5) but well short of WCAG AA (4.5). An earlier version of this fix
  // used the degenerate bar for this case too and let darkred survive
  // un-stripped; the theme-only-fallback path must instead hold to full AA
  // (#231's original behaviour) and strip it to the theme's own pair.
  it('a lone darkred background with no pair anywhere is stripped in light theme (theme-only fallback holds WCAG AA)', () => {
    withTheme('light');
    const html = '<span style="background-color:darkred">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.light.color);
    expect(effectiveBackground(window, el)).toEqual(THEME.light.background);
  });

  // Issue #422 dark-theme acceptance: a lone half that collides with the
  // DARK theme's own background is still stripped, exactly as the light-
  // theme case above -- the broadened rule only stops stripping colors
  // that are actually legible, it does not stop protecting against a
  // genuine near-invisible-text collision in either theme.
  it('a lone half that collides with the dark theme background is still stripped (dark)', () => {
    withTheme('dark');
    const html = '<span style="color:#101010">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'dark');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.dark.color);
    expect(effectiveBackground(window, el)).toEqual(THEME.dark.background);
  });

  // The #1a1111 fixture above fails WCAG AA against the LIGHT theme's
  // near-black default text color, but clears it by a wide margin (~17.9:1)
  // against the DARK theme's near-white default text color -- this
  // theme-only-fallback lone half is preserved as authored in dark theme
  // rather than stripped, exactly as #231's original rule intended for an
  // already-legible color.
  it('a lone background that does NOT collide with the dark theme foreground is preserved, not force-stripped (dark)', () => {
    withTheme('dark');
    const html = '<span style="background-color:#1a1111">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'dark');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.dark.color);
    expect(effectiveBackground(window, el)).toEqual([26, 17, 17, 1]);
    assertReadable(window, el, 'preserved #1a1111 background against dark-theme text');
  });
});

/**
 * Acceptance fixtures for issue #422, built from the ticket's own reported
 * message shape: an inline-styled forum reply-notification mail with no
 * <style> block. Every link/text color is declared on the element it
 * paints (a link, a header title span) while the matching background sits
 * on an ancestor (a table cell, or the <body> itself) -- the pattern
 * #231's original blanket "strip every lone half" rule discarded.
 */
describe('issue #422 -- lone halves that pair with an ancestor background survive', () => {
  const FORUM_NOTIFICATION_HTML = `<html><body style="background-color:#fafafa;color:#3a3a3d">
    <table role="presentation"><tr><td style="background-color:#666666">
      <span style="color:#ffffff">Forum Update</span>
      <a style="color:#ffffff" href="https://example.test/thread">View thread</a>
    </td></tr></table>
    <p>Someone replied: <a style="color:#2671a6" href="https://example.test/reply">click here</a></p>
    <table role="presentation"><tr><td style="background-color:#2671a6">
      <a style="color:#fff" href="https://example.test/go">Zum Inhalt springen</a>
    </td></tr></table>
  </body></html>`;

  function linkContaining(document: ReturnType<typeof renderSrcdoc>['document'], text: string): HappyElement {
    const link = [...document.querySelectorAll('a')].find((a) => (a.textContent ?? '').includes(text));
    if (!link) throw new Error(`no <a> found containing "${text}"`);
    return link;
  }

  it('a link with a lone color inside a #fafafa body keeps its authored color', () => {
    withTheme('light');
    const srcdoc = sanitizeHtml(FORUM_NOTIFICATION_HTML, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const link = linkContaining(document, 'click here');
    expect(parseColor(window.getComputedStyle(link).color)).toEqual([0x26, 0x71, 0xa6, 1]);
  });

  it('a white title span inside a grey header cell keeps white', () => {
    withTheme('light');
    const srcdoc = sanitizeHtml(FORUM_NOTIFICATION_HTML, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const title = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(title).color)).toEqual([255, 255, 255, 1]);
  });

  it('a white link inside the same grey header cell keeps white', () => {
    withTheme('light');
    const srcdoc = sanitizeHtml(FORUM_NOTIFICATION_HTML, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const link = linkContaining(document, 'View thread');
    expect(parseColor(window.getComputedStyle(link).color)).toEqual([255, 255, 255, 1]);
  });

  it('a #fff link inside a coloured button cell keeps white', () => {
    withTheme('light');
    const srcdoc = sanitizeHtml(FORUM_NOTIFICATION_HTML, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const link = linkContaining(document, 'Zum Inhalt springen');
    expect(parseColor(window.getComputedStyle(link).color)).toEqual([255, 255, 255, 1]);
  });

  it('the <body> color pair is carried onto the fragment wrapper', () => {
    withTheme('light');
    const srcdoc = sanitizeHtml(FORUM_NOTIFICATION_HTML, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const wrap = document.body.firstElementChild!;
    expect(parseColor(window.getComputedStyle(wrap).backgroundColor)).toEqual([0xfa, 0xfa, 0xfa, 1]);
    expect(parseColor(window.getComputedStyle(wrap).color)).toEqual([0x3a, 0x3a, 0x3d, 1]);
  });

  it('a message with no <body> tag at all is unaffected -- no wrapper div added', () => {
    const srcdoc = sanitizeHtml('<p style="color:#2671a6">no body tag here</p>', { loadImages: false });
    const body = /<body>([\s\S]*?)<\/body>/.exec(srcdoc)?.[1] ?? '';
    expect(body.trim().startsWith('<p')).toBe(true);
  });

  // A lone color paired with an ancestor background still uses the looser
  // `DEGENERATE_CONTRAST_THRESHOLD` (1.5), not the theme-only fallback's
  // stricter WCAG AA bar (4.5): white text (~3.54:1) on an ancestor's
  // #888888 cell fails AA but is a real, legible, deliberately-paired
  // choice, and must survive exactly as authored. This distinguishes the
  // ancestor-pairing path (#422) from the theme-only-fallback path (#231's
  // original scenario, see the darkred fixture in the "issue #231" block
  // above) -- the two must not collapse onto the same threshold.
  it('a lone color on an ancestor-provided grey background below WCAG AA but above degenerate survives', () => {
    withTheme('light');
    const html = '<table><tr><td style="background-color:#888888"><span style="color:#ffffff">text</span></td></tr></table>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
  });
});

/**
 * Acceptance fixtures for issue #422's second round: the maintainer's
 * hand-back on comment 5358. `resolveEffectiveBackground` walked ancestors
 * reading `declaredColorHalves`, which parsed only the `style` attribute --
 * a background expressed as the legacy `bgcolor` presentational attribute
 * (kept by DOMPurify; not in `FORBID_ATTR`) was invisible to it, so a lone
 * `color` paired against a `bgcolor` ancestor resolved against the reading-
 * pane's own default foreground instead and was stripped as an apparent
 * collision. The reduced case is the Icelandair check-in mail's footer: a
 * `<td bgcolor="#001B71">` (no CSS background) whose own `color: #FFFFFF`
 * (and a nested span's own `color:#FFFFFF`) rendered near-black.
 */
describe('issue #422 second round -- bgcolor presentational attribute counts as a declared background', () => {
  it('reduced case: <td bgcolor> with a lone color -- fails before the fix, resolving near-black instead of white', () => {
    withTheme('light');
    // The control cell (CSS background-color) already worked before this
    // round's fix; the bgcolor cell is this ticket's regression.
    const html = `
      <table><tr>
        <td bgcolor="#001B71" style="color: #FFFFFF">
          <span style="color:#FFFFFF;">(c)1999-2026 Icelandair. All rights reserved.</span>
        </td>
        <td style="background-color:#001B71; color: #FFFFFF">
          <span style="color:#FFFFFF;">control cell, background-color in style</span>
        </td>
      </tr></table>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const spans = [...document.querySelectorAll('span')];
    const bgcolorSpan = spans.find((s) => (s.textContent ?? '').includes('Icelandair'))!;
    const controlSpan = spans.find((s) => (s.textContent ?? '').includes('control cell'))!;

    // The control (CSS background-color) cell keeps its authored white text.
    expect(parseColor(window.getComputedStyle(controlSpan).color)).toEqual([255, 255, 255, 1]);
    // The bgcolor cell's lone color must ALSO keep its authored white text --
    // the defect resolved it against the reading pane's own near-black
    // default (rgb(22, 22, 22)) instead of the navy `bgcolor` background.
    expect(parseColor(window.getComputedStyle(bgcolorSpan).color)).toEqual([255, 255, 255, 1]);
  });

  it('a lone color on the SAME element as its own bgcolor attribute survives (the reduced case\'s own <td> color)', () => {
    withTheme('light');
    const html = '<table><tr><td bgcolor="#001B71" style="color: #FFFFFF">text</td></tr></table>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const td = document.querySelector('td')!;
    expect(parseColor(window.getComputedStyle(td).color)).toEqual([255, 255, 255, 1]);
  });

  // The table/tr/th ancestors below are wrapped in a <body style="color:
  // #616161"> carrying a foreground chosen to clear BOTH contrast checks
  // this fixture's own-element pass performs: >= 4.5:1 (WCAG AA) against
  // the light theme's white default background, so `<body>`'s own lone
  // color survives as a theme-only fallback with no ancestor of its own,
  // AND >= 1.5:1 (the degenerate bar) against navy, so the table/tr/th's
  // own "background declared, no local color" check -- which resolves
  // ITS OWN contrast against this carried-over ancestor foreground -- does
  // not itself strip the very background this fixture means to exercise.
  // This sidesteps a separate, pre-existing characteristic of
  // `sanitizeInlineColorPairs`'s own-element check (present on current
  // `main`/train for a bare CSS `background-color` too, verified against
  // an unmodified checkout: a container that declares ONLY a background
  // with NO ancestor-declared foreground anywhere falls back to
  // `readingPaneTheme()` for its OWN contrast check and can strip its own
  // background if that theme default collides) -- out of scope for this
  // ticket, which is about `bgcolor` reaching parity with CSS
  // `background-color`, not about that pre-existing fallback behavior.
  const ANCESTOR_FOREGROUND_CONTEXT = '<body style="color:#616161">';

  it('bgcolor on <table> is treated as a declared background for a descendant lone color', () => {
    withTheme('light');
    const html = `<html>${ANCESTOR_FOREGROUND_CONTEXT}<table bgcolor="#001B71"><tr><td><span style="color:#ffffff">via table bgcolor</span></td></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
  });

  it('bgcolor on <tr> is treated as a declared background for a descendant lone color', () => {
    withTheme('light');
    const html = `<html>${ANCESTOR_FOREGROUND_CONTEXT}<table><tr bgcolor="#001B71"><td><span style="color:#ffffff">via tr bgcolor</span></td></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
  });

  it('bgcolor on <th> is treated as a declared background for a descendant lone color', () => {
    withTheme('light');
    const html = `<html>${ANCESTOR_FOREGROUND_CONTEXT}<table><tr><th bgcolor="#001B71"><span style="color:#ffffff">via th bgcolor</span></th></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
  });

  it('bgcolor on <body> is carried onto the fragment wrapper, same as a CSS background-color', () => {
    withTheme('light');
    // <body> declares both halves itself (bgcolor + a CSS color), matching
    // the reduced case's own same-element shape, so the wrapper's combined
    // pair is checked for a genuine collision rather than falling back to
    // the theme-only "no ancestor foreground" path exercised above.
    const html = '<html><body bgcolor="#001B71" style="color:#e8e8e8"><span style="color:#ffffff">via body bgcolor</span></body></html>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const wrap = document.body.firstElementChild!;
    expect(parseColor(window.getComputedStyle(wrap).backgroundColor)).toEqual([0x00, 0x1b, 0x71, 1]);
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
  });

  it('a CSS background-color on the same element wins over a conflicting bgcolor attribute', () => {
    withTheme('light');
    // bgcolor says white (which WOULD collide with a white descendant
    // color); the CSS background-color says navy (which would not). CSS
    // must win, so the descendant's white text must survive.
    const html = `<html>${ANCESTOR_FOREGROUND_CONTEXT}<table><tr><td bgcolor="#ffffff" style="background-color:#001B71"><span style="color:#ffffff">text</span></td></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([255, 255, 255, 1]);
  });

  it('a bgcolor that genuinely collides with the descendant color is still stripped', () => {
    withTheme('light');
    const html = '<table><tr><td bgcolor="#ffffff"><span style="color:#fefefe">text</span></td></tr></table>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    // #fefefe on #ffffff is a near-invisible collision -- stripped to the
    // reading pane's own theme foreground, not left as authored.
    expect(parseColor(window.getComputedStyle(el).color)).toEqual(THEME.light.color);
  });
});

/**
 * Acceptance fixtures for issue #422's third round: the maintainer's
 * hand-back on comment 5358 disclosed a second, deeper defect in the same
 * round-2 build (063dce2) beyond the `bgcolor` gap that round 2 fixed --
 * independent verification reconstructed the reported message's own link
 * band (the two "Icelandair Website" / "Terms & Conditions" links,
 * directly above the copyright cell that round 2 already fixed) using
 * this ticket's own established colors (`<body style="color:#3a3a3d">`,
 * a `<td style="background-color:#001B71">` declared purely in CSS --
 * no `bgcolor` at all -- and a descendant `<a style="color:#FFFFFF">`)
 * and found BOTH the cell's background and the anchor's white color
 * stripped.
 *
 * Cause: `sanitizeInlineColorPairs`'s "background declared, no local
 * color" branch resolved its collision check against
 * `resolveEffectiveForeground`, which walks ANCESTORS ONLY -- it never
 * looked at the DESCENDANT that actually declares the color which will
 * paint on this background. Navy (#001B71) against the body's dark-grey
 * `color:#3a3a3d` measures ~1.33:1, under `DEGENERATE_CONTRAST_THRESHOLD`
 * (1.5), so the background was stripped as an apparent collision --
 * even though the anchor's own white text, once the background actually
 * paints, is perfectly legible against it (white on navy is ~15:1). With
 * the background gone, the anchor's lone white color then resolved
 * against the theme's own white page background (no ancestor background
 * left to find) and was stripped too as a second, apparent white-on-white
 * collision -- silently downgrading a legible authored pairing into
 * default theme colors on both halves.
 *
 * Fix: `collectDescendantOwnColors` (`sanitize.ts`) looks ahead at every
 * color a descendant declares on its own halves before deciding whether a
 * lone background collides -- skipping past any nested element that
 * itself declares a background, since that starts its own independent
 * paint context. If any such descendant color is legible against this
 * background, the background is judged safe and kept; a descendant whose
 * own color is NOT legible against it is not decisive either way -- it is
 * still stripped individually when the walk reaches that descendant (its
 * own ancestor-background lookup, unchanged). Only when NO descendant
 * declares any color at all does the check fall back to the inherited
 * ancestor/`<body>`/theme foreground, exactly as issue #422's second
 * round left it.
 */
describe('issue #422 third round -- a lone background is judged against the text that will actually paint on it', () => {
  it('reconstructed link band: a background survives when a descendant declares a legible color (fails before the fix)', () => {
    withTheme('light');
    const html = `<html><body style="color:#3a3a3d"><table><tr><td style="background-color:#001B71"><a style="color:#FFFFFF">Icelandair Website</a></td></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const td = document.querySelector('td')!;
    const a = document.querySelector('a')!;
    // The background must survive -- it is what the anchor's own white
    // text needs to stay legible.
    expect(parseColor(window.getComputedStyle(td).backgroundColor)).toEqual([0x00, 0x1b, 0x71, 1]);
    // The anchor's own authored white must survive too, not be stripped
    // as a second, apparent collision once the background is gone.
    expect(parseColor(window.getComputedStyle(a).color)).toEqual([255, 255, 255, 1]);
  });

  it('a background whose only text is inherited and illegible is still stripped', () => {
    withTheme('light');
    // No descendant declares its own color anywhere -- the text painting
    // on this background is entirely the inherited body color, which
    // genuinely collides with navy (~1.33:1).
    const html = `<html><body style="color:#3a3a3d"><table><tr><td style="background-color:#001B71">plain text, no descendant color</td></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const td = document.querySelector('td')!;
    // Stripped -- td no longer declares its own background-color at all,
    // so the visible background is whatever the reading pane's own theme
    // paints underneath (light theme: white) -- the navy never had a
    // legible pairing to defer to.
    expect(effectiveBackground(window, td)).toEqual(THEME.light.background);
  });

  it('a background with one legible and one illegible descendant color: the background stays, the illegible descendant is stripped individually', () => {
    withTheme('light');
    // span1's white is legible against navy and keeps the background
    // alive; span2's own color EQUALS the background (a same-color,
    // maximally degenerate collision) and must still be stripped on ITS
    // OWN -- but only for span2, not for the shared background.
    const html = `<html><body style="color:#3a3a3d"><table><tr><td style="background-color:#001B71">
      <span style="color:#FFFFFF">legible</span>
      <span style="color:#001B71">illegible</span>
    </td></tr></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const td = document.querySelector('td')!;
    const spans = [...document.querySelectorAll('span')];
    const legibleSpan = spans.find((s) => s.textContent === 'legible')!;
    const illegibleSpan = spans.find((s) => s.textContent === 'illegible')!;
    // The background survives because of the legible descendant.
    expect(parseColor(window.getComputedStyle(td).backgroundColor)).toEqual([0x00, 0x1b, 0x71, 1]);
    // The legible descendant keeps its authored white.
    expect(parseColor(window.getComputedStyle(legibleSpan).color)).toEqual([255, 255, 255, 1]);
    // The illegible descendant's own color, which duplicates the
    // background exactly, is stripped on its own terms (resolved against
    // the now-settled navy background, DEGENERATE_CONTRAST_THRESHOLD) --
    // it falls through to whatever it inherits, the body's dark grey, not
    // the theme default (there IS an ancestor color: the carried-over
    // <body> color).
    expect(parseColor(window.getComputedStyle(illegibleSpan).color)).toEqual([0x3a, 0x3a, 0x3d, 1]);
  });

  it('nested backgrounds: the inner background/color pair is judged on its own, independent of the outer', () => {
    withTheme('light');
    // The outer <td> declares only a background and has no legible
    // descendant color of its own to defer to -- the only text inside it
    // sits behind the inner <div>'s OWN background, which is a separate
    // paint context and must not count towards (or against) the outer's
    // decision.
    const html = `<table><tr><td style="background-color:#001B71"><div style="background-color:#ffffff;color:#001B71">inner text</div></td></tr></table>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const div = document.querySelector('div')!;
    // The inner, complete pair (navy text on white) is highly legible on
    // its own and survives untouched, regardless of the outer's fate.
    expect(parseColor(window.getComputedStyle(div).backgroundColor)).toEqual([255, 255, 255, 1]);
    expect(parseColor(window.getComputedStyle(div).color)).toEqual([0x00, 0x1b, 0x71, 1]);
  });

  // Real markup, not a reconstruction: the maintainer's Icelandair
  // check-in mail (thread t3777, message id 3777), read READ-ONLY from
  // production (ssh outpost.netzhansa.com, sqlite3 -readonly against
  // /var/lib/herold/herold.sqlite, the HTML part's byte range decoded
  // from its blob file locally -- production untouched). This is the
  // link band (two <td>s, each declaring its OWN color:#435064 AND
  // background-color:#001B71 -- a same-element pair, ~1.84:1, legible
  // enough to survive the existing same-element check unchanged) plus
  // the copyright cell (<td bgcolor="#001B71" style="...;color:#FFFFFF">,
  // round 2's own fixture) exactly as they appear on the wire, including
  // the enclosing tables/rows the sender's markup nests them in. This
  // fixture predates round 3's fix and was already passing on 843d876d
  // (round 2) -- it is not a reproduction of the third-round defect
  // (that needs the reconstructed lone-background/descendant-color shape
  // above, which this real message does not happen to contain), but a
  // ground-truth regression lock so a future change cannot silently
  // regress the actual reported message.
  it('the real Icelandair footer (link band + copyright cell) renders all authored colors', () => {
    withTheme('light');
    const html = `<html><body style="margin: 0px;padding: 0px;background-color: #ffffff;">
<table cellpadding="0" cellspacing="0" width="100%" role="presentation" style="background-color: #001B71; min-width: 100%; " class="stylingblock-content-wrapper"><tbody><tr><td style="padding: 0px; " class="stylingblock-content-wrapper camarker-inner"><table align="center" border="0" cellpadding="0" cellspacing="0" style="width:600px;">

  <tbody><tr>
   <td>
    </td><td align="center" bgcolor="#001B71" style="padding: 0px 0px 15px 0px; font-family: Gotham, 'Helvetica Neue', Helvetica, Arial, sans-serif; font-style: normal; font-weight: normal; font-size: 13px; color: #FFFFFF " width="100%">
        <br/>
        <span style="color:#FFFFFF;">logo</span></td></tr><tr>
   <td>
    </td><td align="center" bgcolor="#001B71" style="padding: 20px 0px 20px 0px; font-family: Gotham, 'Helvetica Neue', Helvetica, Arial, sans-serif; font-style: normal; font-weight: normal; font-size: 14px; color: #001B71" width="100%">
        <table bgcolor="#001B71" border="0" cellpadding="0" cellspacing="0" width="100%">

          <tbody><tr>
           <td align="right" bgcolor="#001B71" style="padding: 0 20px 0 0; font-family: Gotham, 'Helvetica Neue', Helvetica, Arial, sans-serif; font-style: normal; font-weight: normal; font-size: 14px; color: #435064; border-right: 1px solid #435064; background-color:#001B71;" width="50%">
            <b><a href="http://click.email.icelandair.is/manage" style="color:#FFFFFF;text-decoration:none;" title="Icelandair"><b>Icelandair Website</b></a></b></td><td align="left" bgcolor="#eaeaea" style="padding: 0 0 0 20px; font-family: Gotham, 'Helvetica Neue', Helvetica, Arial, sans-serif; font-style: normal; font-weight: normal; font-size: 14px; color: #435064; background-color:#001B71" width="50%">
            <b><a href="http://click.email.icelandair.is/terms" style="color:#FFFFFF;text-decoration:none;" title="Terms &amp; Conditions"><b>Terms &amp; Conditions</b></a></b></td></tr></tbody></table></td></tr><tr>
   <td>
    </td><td align="center" bgcolor="#001B71" style="padding: 0px 0px 43px 0px; font-family: Gotham, 'Helvetica Neue', Helvetica, Arial, sans-serif; font-style: normal; font-weight: normal; font-size: 13px; color: #FFFFFF " width="100%">
        <br/>
        <span style="color:#FFFFFF;">&#169;1999-2026 Icelandair. All rights reserved.<br/>
        Icelandair - Flugvellir 1, 221 Hafnarfj&#246;r&#240;ur</span></td></tr></tbody></table></td></tr></tbody></table></body></html>`;
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const anchors = [...document.querySelectorAll('a')];
    const websiteLink = anchors.find((a) => a.textContent === 'Icelandair Website')!;
    const termsLink = anchors.find((a) => a.textContent === 'Terms & Conditions')!;
    const spans = [...document.querySelectorAll('span')];
    const copyrightSpan = spans.find((s) => (s.textContent ?? '').includes('Icelandair'))!;

    // The link band: both anchors keep their authored white.
    expect(parseColor(window.getComputedStyle(websiteLink).color)).toEqual([255, 255, 255, 1]);
    expect(parseColor(window.getComputedStyle(termsLink).color)).toEqual([255, 255, 255, 1]);
    // Both anchors' enclosing <td> keeps its navy (own color:#435064 +
    // own background-color:#001B71 -- a same-element pair at ~1.84:1,
    // above the degenerate bar).
    expect(parseColor(window.getComputedStyle(websiteLink.parentElement!.parentElement!).backgroundColor)).toEqual([
      0x00, 0x1b, 0x71, 1,
    ]);
    // The copyright cell's own text keeps its authored white on navy.
    expect(parseColor(window.getComputedStyle(copyrightSpan).color)).toEqual([255, 255, 255, 1]);
  });
});

/**
 * Regression lock for issue #441's own follow-up: the O(N)-to-O(1)
 * ancestor-resolution rewrite (`InheritedColorPair`, replacing the
 * per-element ancestor walk) initially got a same-element degenerate pair
 * wrong. A 300-trial fuzz of nested tags mixing `color`, `background-color`
 * and `bgcolor` found the minimal case below (independent verification of
 * commit d2e24425): a `<td>` whose CSS `color`/`background-color` are the
 * SAME color (a same-element degenerate pair, stripped) also carries a
 * `bgcolor` attribute. Stripping the degenerate CSS pair removes only the
 * CSS `background-color` -- `bgcolor` is a different presentational
 * carrier the #422 "CSS wins over bgcolor" rule never said to remove, and
 * it still paints in a real browser. A version of `resolveDeclaredColorPair`
 * that tracked "was this half stripped" via local flags could not express
 * "a different carrier on the same element still paints", collapsed the
 * td's contribution to "no background at all", and wrongly stripped a
 * descendant's legible lone color as a result. The fix re-reads each
 * element's actually-surviving pair off the live (post-mutation) DOM via
 * `declaredColorHalves` -- the same function the pre-#441 ancestor walk
 * used -- instead of tracking flags.
 */
describe('issue #441 fuzz regression -- a stripped same-element CSS pair leaves a surviving bgcolor for descendants', () => {
  it('minimal fuzz case: <td> degenerate CSS pair strips CSS but bgcolor survives for a descendant lone color', () => {
    withTheme('light');
    const html =
      '<table><tr><td style="color:#4a4a4a;background-color:#4a4a4a" bgcolor="#0000ff">' +
      '<p style="color:#eeeeee">text</p>' +
      '</td></tr></table>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const td = document.querySelector('td')!;
    const p = document.querySelector('p')!;

    // The td's own degenerate CSS pair (identical color on both halves) is
    // stripped from its `style`, but the DIFFERENT `bgcolor` carrier is
    // left untouched -- it still paints blue in a real browser (happy-dom,
    // used here, does not map the legacy presentational `bgcolor`
    // attribute to a computed `background-color` the way a real UA does,
    // so this locks the attribute's presence, not `getComputedStyle`; the
    // live-browser verification in the issue thread confirms the paint).
    expect(td.getAttribute('style')).toBeNull();
    expect(td.getAttribute('bgcolor')).toBe('#0000ff');
    // The paragraph's own lone color must survive: it is legible against
    // the td's surviving `bgcolor` background (a declared counterpart),
    // not judged against "no background at all" and held to the
    // stricter theme-only WCAG AA bar.
    expect(parseColor(window.getComputedStyle(p).color)).toEqual([0xee, 0xee, 0xee, 1]);
  });

  it('the surviving bgcolor carrier is two levels up, through an intervening element that declares only a foreground', () => {
    withTheme('light');
    const html =
      '<table><tr><td style="color:#4a4a4a;background-color:#4a4a4a" bgcolor="#0000ff">' +
      '<div style="color:#dddddd"><span style="color:#eeeeee">deep text</span></div>' +
      '</td></tr></table>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const div = document.querySelector('div')!;
    const span = document.querySelector('span')!;

    // The intervening <div> declares only a foreground -- it must pass
    // the td's surviving `bgcolor` through to its own child unchanged,
    // not reset the inherited background to "none" merely because the
    // div itself declares no background of its own.
    expect(parseColor(window.getComputedStyle(div).color)).toEqual([0xdd, 0xdd, 0xdd, 1]);
    expect(parseColor(window.getComputedStyle(span).color)).toEqual([0xee, 0xee, 0xee, 1]);
  });
});
