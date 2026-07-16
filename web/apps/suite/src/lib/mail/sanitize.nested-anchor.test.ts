/**
 * Acceptance test for issue #252 ("HTML mail: nested-anchor bulletproof
 * button markup loses its color/background pairing, rendering the label
 * illegible -- LinkedIn Accept button").
 *
 * LinkedIn's "bulletproof button" template nests a hidden
 * `<a aria-hidden="true">` inside a visible `<a aria-label="Accept">`,
 * with the actual color/background pair declared on an intervening
 * `<td>`. Nested `<a>` is invalid HTML5; left alone, the browser's own
 * parser (which both DOMPurify's internal parse and the final iframe
 * `srcdoc` load run) implicitly closes the OUTER anchor the moment the
 * inner one opens, detaching the caption from the `<td>` that declared
 * its color pairing. The pre-existing #231 lone-half guard
 * (`sanitizeInlineColorPairs`) then strips the caption's now-orphaned
 * lone `color` declaration, leaving it with no color at all --
 * reproducing the reported blue-on-blue.
 *
 * The fix (`neutralizeNestedAnchors` in sanitize.ts) demotes the nested
 * `<a>` to a `<span>` in the raw HTML string before DOMPurify's parse,
 * so the DOM keeps its authored nesting and the caption inherits the
 * ancestor `<td>`'s `color` normally, regardless of what
 * `sanitizeInlineColorPairs` does to any element's own inline
 * declaration.
 *
 * As with sanitize.color-contrast.test.ts, this file asserts the
 * *visible* outcome -- resolved paint colors in a rendered (happy-dom)
 * document -- not the presence/absence of a substring in the sanitized
 * markup.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { Window } from 'happy-dom';
import type { Element as HappyElement } from 'happy-dom';
import { sanitizeHtml } from './sanitize';

function parseColor(value: string): [number, number, number, number] | null {
  const v = value.trim().toLowerCase();
  if (v === '' || v === 'initial' || v === 'transparent' || v === 'inherit') return null;
  const rgbMatch = /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(v);
  if (rgbMatch) {
    const [, r, g, b, alpha] = rgbMatch;
    const a = alpha !== undefined ? parseFloat(alpha) : 1;
    if (a === 0) return null;
    return [parseInt(r!, 10), parseInt(g!, 10), parseInt(b!, 10), a];
  }
  const hex6Match = /^#([0-9a-f]{6})$/.exec(v);
  if (hex6Match) {
    const n = hex6Match[1]!;
    return [parseInt(n.slice(0, 2), 16), parseInt(n.slice(2, 4), 16), parseInt(n.slice(4, 6), 16), 1];
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

function computedForeground(win: InstanceType<typeof Window>, el: HappyElement): [number, number, number, number] {
  const c = parseColor(win.getComputedStyle(el).color);
  if (!c) throw new Error(`unparseable computed color: "${win.getComputedStyle(el).color}"`);
  return c;
}

function effectiveBackground(win: InstanceType<typeof Window>, el: HappyElement): [number, number, number, number] {
  let node: HappyElement | null = el;
  while (node) {
    const parsed = parseColor(win.getComputedStyle(node).backgroundColor);
    if (parsed) return parsed;
    node = node.parentElement;
  }
  throw new Error('no ancestor declared a resolvable background-color');
}

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

// Reconstructed from the reported LinkedIn "I want to connect" invitation:
// a visible `<a aria-label="Accept">` wraps a table cell carrying the
// button's fill/caption color pair, which itself wraps a second, hidden
// `<a aria-hidden="true">` around the caption <span>.
const LINKEDIN_ACCEPT_BUTTON = `
  <a href="https://www.linkedin.com/comm/accept" aria-label="Accept">
    <table role="presentation"><tr>
      <td style="background-color:#0a66c2; color:#ffffff; border-radius:24px; padding:8px 24px;">
        <a href="https://www.linkedin.com/comm/accept" aria-hidden="true" style="color:#0a66c2; text-decoration:none;">
          <span style="color:#ffffff">Accept</span>
        </a>
      </td>
    </tr></table>
  </a>
`;

describe('issue #252 -- nested-anchor bulletproof button keeps its color pairing', () => {
  const themes: Array<'light' | 'dark'> = ['light', 'dark'];

  for (const theme of themes) {
    it(`${theme} theme: LinkedIn Accept button caption is legible against the pill fill`, () => {
      const srcdoc = sanitizeHtml(LINKEDIN_ACCEPT_BUTTON, { loadImages: false });
      const { window, document } = renderSrcdoc(srcdoc, theme);

      // The button fill must survive on the <td>.
      const td = document.querySelector('td')!;
      expect(parseColor(window.getComputedStyle(td).backgroundColor)).toEqual([0x0a, 0x66, 0xc2, 1]);

      // The caption text ("Accept") must be legible against that fill --
      // not the reported blue-on-blue collision.
      const caption = Array.from(document.querySelectorAll('span')).find(
        (el) => el.textContent?.trim() === 'Accept',
      )!;
      expect(caption).toBeTruthy();
      assertReadable(window, caption, 'LinkedIn Accept caption vs pill fill');

      // Nested <a> must not survive into the rendered output -- it was
      // structurally invalid and the fix demotes it, it does not merely
      // relocate the problem.
      expect(document.querySelectorAll('a').length).toBeLessThanOrEqual(1);
    });
  }

  it('the outer anchor keeps its href and target/rel rewrite', () => {
    const srcdoc = sanitizeHtml(LINKEDIN_ACCEPT_BUTTON, { loadImages: false });
    const { document } = renderSrcdoc(srcdoc, 'light');
    const outer = document.querySelector('a')!;
    expect(outer.getAttribute('href')).toBe('https://www.linkedin.com/comm/accept');
    expect(outer.getAttribute('target')).toBe('_blank');
    expect(outer.getAttribute('rel')).toBe('noopener noreferrer');
  });

  it('REGRESSION GUARD (#231): a genuine lone background-color half is still stripped', () => {
    const html = '<span style="background-color:#1a1111">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    // Falls back to Herold's own paired light theme (#161616 on #ffffff),
    // not the sender's near-invisible dark-on-dark.
    expect(parseColor(window.getComputedStyle(el).color)).toEqual([0x16, 0x16, 0x16, 1]);
    expect(effectiveBackground(window, el)).toEqual([0xff, 0xff, 0xff, 1]);
  });

  it('REGRESSION GUARD (#231): a genuine lone color half (no nesting involved) is still stripped', () => {
    const html = '<span style="color:#f0f0f0">text</span>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { window, document } = renderSrcdoc(srcdoc, 'light');
    const el = document.querySelector('span')!;
    assertReadable(window, el, 'lone light color, no background, still stripped');
  });

  it('a NON-nested single <a> button (View profile outline style) is unaffected by the neutralizer', () => {
    const html =
      '<a href="https://linkedin.com/profile/x" style="color:#0a66c2; border:1px solid #0a66c2;">View profile</a>';
    const srcdoc = sanitizeHtml(html, { loadImages: false });
    const { document } = renderSrcdoc(srcdoc, 'light');
    const a = document.querySelector('a')!;
    expect(a.textContent?.trim()).toBe('View profile');
    expect(a.tagName).toBe('A');
  });
});
