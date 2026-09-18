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
