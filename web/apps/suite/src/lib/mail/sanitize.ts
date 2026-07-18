/**
 * HTML mail sanitisation per docs/architecture/04-rendering.md.
 *
 * Layered defence:
 *   1. DOMPurify drops unsafe tags / attributes / URL schemes.
 *   2. Anchors get target="_blank" rel="noopener noreferrer".
 *   3. Images:
 *      - cid: → resolved via the per-message attachment map (blocked
 *        when no map entry exists).
 *      - data:image/<raster>: → passed through (inline raster images
 *        are common for logos / signatures; they issue no network
 *        request so they bypass nothing the external-image gate
 *        protects against). data:image/svg+xml is explicitly excluded
 *        because SVG can carry scripts and external references.
 *      - http(s): when loadImages=false → src removed, alt swapped.
 *      - http(s): when loadImages=true  → rewritten to /proxy/image (REQ-SEC-07).
 *      - Anything else → src removed.
 *   4. Output wrapped in a minimal HTML document with an inline CSP that
 *      restricts the iframe to img-src 'self' data: (the parent origin
 *      hosts the proxy, and inline-base64 images are common in mail).
 *
 * The iframe's `sandbox="allow-same-origin"` (with NO allow-scripts) is the
 * primary defence — DOMPurify and CSP are defence-in-depth.
 */

import DOMPurify from 'dompurify';

import { t } from '../i18n/i18n.svelte';
import {
  INTERNALIZE_PLACEHOLDER_PREFIX,
  isInternalizePlaceholder,
} from './internalize-placeholder';
import { isAttributionLine, SIG_DELIM_RE } from './quoted';

export interface SanitizeOptions {
  /** When true, http(s) <img src> rewrites through the image proxy. */
  loadImages: boolean;
  /**
   * Map of cid (with no angle brackets, no `cid:` prefix) → resolved
   * URL. Inline images referenced by Content-ID get rewritten to the
   * matching URL when present. Resolution is unconditional: cid refs
   * point at attachments of the same email so there is no privacy leak.
   */
  cidMap?: Record<string, string>;
  /**
   * Server-supplied intrinsic pixel dimensions keyed by cid (no angle
   * brackets, no `cid:` prefix). When present for a resolved cid image,
   * an `aspect-ratio: W / H` declaration is appended to the img's inline
   * style so the browser can reserve the correct height before the image
   * bytes arrive (issue #47). Absent keys leave the image unchanged.
   */
  cidDimensions?: Record<string, { width: number; height: number }>;
  /**
   * True when the server has not finished the background internalize pass
   * for this message (REQ-EXTIMG-BG-INTERNAL-40) -- i.e. `Email/get` sent
   * `InternalizePending = true` and every external <img src> was rewritten
   * to `extimg.PlaceholderDataURI`. Gates the iframe stylesheet's
   * placeholder-sizing rule (see `wrapInIframeDocument`): the rule matches
   * on a bare data-URI byte prefix, which a genuine already-internalized
   * tracking pixel can coincidentally share (issue #160), so the rule must
   * only apply while this specific message is still pending.
   */
  internalizePending?: boolean;
}

const ALLOWED_TAGS = [
  'a', 'abbr', 'address', 'b', 'blockquote', 'br', 'caption',
  'cite', 'code', 'col', 'colgroup', 'details', 'div', 'dl', 'dt', 'dd',
  'em', 'figcaption', 'figure', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'hr', 'i', 'img', 'kbd', 'li', 'mark', 'ol', 'p', 'pre', 'q',
  's', 'samp', 'small', 'span', 'strong', 'sub', 'summary', 'sup', 'table',
  'tbody', 'td', 'tfoot', 'th', 'thead', 'tr', 'u', 'ul', 'var',
];

const FORBID_TAGS = [
  'script', 'style', 'iframe', 'object', 'embed', 'form', 'input',
  'button', 'textarea', 'select', 'option', 'noscript', 'meta',
  'link', 'base',
];

const FORBID_ATTR = [
  'onerror', 'onload', 'onclick', 'onfocus', 'onblur',
  'onmouseover', 'onmouseout', 'onmouseenter', 'onmouseleave',
  'onkeydown', 'onkeyup', 'onsubmit', 'onchange', 'oninput',
  'formaction', 'srcdoc',
];

// Inline raster image data URIs we accept on <img src>. The trailing
// [;,] anchors the mediatype boundary so that data:image/svg+xml or a
// crafted "data:image/png-something-evil" cannot match. Matching is
// case-insensitive because MIME types are case-insensitive per RFC 2045.
const INLINE_IMAGE_DATA_URI =
  /^data:image\/(?:png|jpeg|jpg|gif|webp|bmp|x-icon|vnd\.microsoft\.icon|tiff|avif|apng|heic|heif)[;,]/i;

/** Quick check before sanitising — used to decide whether to show the banner. */
export function htmlHasExternalImages(html: string): boolean {
  return /<img\b[^>]*\bsrc\s*=\s*["']?https?:/i.test(html);
}

// Matches an <a ...> opening tag or an </a> closing tag, tolerating '>'
// inside quoted attribute values (e.g. an href with an encoded query
// string) so the tag boundary is found correctly.
const ANCHOR_TAG_RE = /<(\/?)a\b((?:[^>"']|"[^"]*"|'[^']*')*)>/gi;

/**
 * Demote every <a> nested inside another <a> to a <span> in the raw HTML
 * string, before it reaches DOMPurify's own parse (issue #252, "bulletproof
 * button" markup: LinkedIn nests a hidden `<a aria-hidden="true">` inside a
 * visible `<a aria-label="...">`).
 *
 * Nested anchors are invalid per the HTML5 content model. Left alone, the
 * browser's own HTML parser -- which DOMPurify's `RETURN_DOM_FRAGMENT` mode
 * runs on internally, and which the iframe's `srcdoc` load runs on again for
 * the final sanitized output -- implicitly closes the OUTER `<a>` the moment
 * the inner one opens. That relocates the inner `<a>` (and everything inside
 * it) out from under any ancestor `<td>`/`<table>` cell that was carrying a
 * `background-color`/`color` pair, turning it into a sibling instead of a
 * descendant. `sanitizeInlineColorPairs` then sees the caption's own lone
 * `color` declaration with no local `background-color` and strips it as an
 * apparent lone half, even though it was authored to pair against the
 * now-detached ancestor's background -- the caption ends up with no color
 * at all, sitting next to the still-filled, now-empty pill.
 *
 * Renaming the nested tag to `<span>` before the browser ever parses the
 * string sidesteps the implicit close entirely: the DOM keeps its authored
 * nesting, so the caption's `color` naturally inherits down through any
 * intermediate elements from the ancestor `<td>`'s pair via ordinary CSS
 * inheritance -- independent of whatever `sanitizeInlineColorPairs` decides
 * to do with each element's own (now often redundant) inline declaration.
 * `href`/`target`/`rel` are dropped from the demoted tag since a `<span>`
 * cannot act as a navigation target and a dangling `href` would be
 * misleading; every other attribute (style, aria-hidden, class, ...) is
 * kept verbatim.
 *
 * Only literal `<a>`/`</a>` tag boundaries are tracked (a lightweight
 * stack of "was this occurrence renamed"), not general HTML well-
 * formedness -- this only needs to catch the nested-anchor case; any other
 * malformed markup is left to DOMPurify's own parser as before. An
 * unmatched stray `</a>` (stack empty) falls back to emitting `</a>`
 * unchanged, which is exactly what the input already was.
 */
function neutralizeNestedAnchors(raw: string): string {
  const anchorStack: boolean[] = [];
  return raw.replace(ANCHOR_TAG_RE, (match: string, slash: string, attrs: string) => {
    if (slash) {
      const wasRenamed = anchorStack.pop();
      return wasRenamed ? '</span>' : '</a>';
    }
    const nested = anchorStack.length > 0;
    anchorStack.push(nested);
    if (!nested) return match;
    const cleanedAttrs = attrs.replace(
      /\s+(?:href|target|rel)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi,
      '',
    );
    return `<span data-herold-nested-anchor="1"${cleanedAttrs}>`;
  });
}

export function sanitizeHtml(raw: string, options: SanitizeOptions): string {
  const fragment = DOMPurify.sanitize(neutralizeNestedAnchors(raw), {
    ALLOWED_TAGS,
    FORBID_TAGS,
    FORBID_ATTR,
    ALLOW_DATA_ATTR: false,
    ALLOW_UNKNOWN_PROTOCOLS: false,
    KEEP_CONTENT: true,
    RETURN_DOM_FRAGMENT: true,
  }) as DocumentFragment;

  // Auto-link plain-text URLs and mailto addresses in text nodes
  // (issue #103). Runs before the anchor rewrite below so the new
  // <a> elements pick up target/rel in the same pass.
  linkifyTextNodes(fragment);

  // Anchor rewriting: every <a> opens in a new tab with no referrer leak.
  for (const a of fragment.querySelectorAll('a')) {
    a.setAttribute('target', '_blank');
    a.setAttribute('rel', 'noopener noreferrer');
  }

  // Image rewriting per REQ-SEC-05/07.
  for (const img of fragment.querySelectorAll('img')) {
    rewriteImage(img, options);
  }

  // Strip a lone half of an inline color/background-color pair (issue #231):
  // an element that declares only one of the two inherits the other from
  // the iframe's own theme, which can collide (sender's dark background +
  // Herold's dark-mode text color, or the mirror case in light mode).
  sanitizeInlineColorPairs(fragment);

  // Wrap quoted-history regions (blockquote / gmail_quote / Apple-style
  // attribution divs) in <details>. Only the first quoted region per
  // body is wrapped — nested replies inside a quote stay nested.
  collapseQuotedRegions(fragment);

  // Serialise back to HTML. (innerHTML round-trip preserves attribute changes.)
  const wrap = document.createElement('div');
  wrap.appendChild(fragment);
  const cleanBody = wrap.innerHTML;
  return wrapInIframeDocument(cleanBody, options.internalizePending === true);
}

// Tags whose text content is treated as opaque -- we never rewrite
// text inside them. <a> guards against double-linking an existing
// anchor; <code>/<pre>/<kbd>/<samp>/<tt> preserve typewritten text
// verbatim; <script>/<style> are already excluded by the sanitiser
// but we keep them in the list as a defensive measure.
const LINKIFY_SKIP_ANCESTORS = new Set([
  'A',
  'CODE',
  'PRE',
  'KBD',
  'SAMP',
  'TT',
  'SCRIPT',
  'STYLE',
]);

// URL/mailto pattern matched in text nodes. The match is greedy up to
// whitespace and angle-bracket / quote terminators only; closing
// punctuation like ")" or "." stays in the match and is shed by
// trimTrailingPunctuation below in a balance-aware way.
//
// - "https?://" requires the scheme prefix.
// - bare "www." paths are linkified with an implicit https:// prefix.
// - "mailto:..." plain-text addresses linkify as mailto links.
// - bare addresses (foo@example.com) are intentionally NOT linkified
//   to avoid false positives in plain-text quoting like "On X wrote:
//   alice@example.com said". A user copying the address out is the
//   safer default.
const LINKIFY_RE = /(?:https?:\/\/|www\.)[^\s<>"'`]+|mailto:[^\s<>"'`]+/gi;

// Mail clients commonly wrap URLs in punctuation ("see
// https://example.com.") and we strip the trailing dot/comma so the
// link target is correct. Closing brackets are also dropped when
// unmatched, but kept when they balance an opening bracket inside the
// URL (Wikipedia-style "https://x/wiki/Foo_(bar)").
function trimTrailingPunctuation(url: string): { kept: string; dropped: string } {
  let kept = url;
  let dropped = '';
  for (;;) {
    const last = kept.charAt(kept.length - 1);
    if (last === '') break;
    if ('.,;:!?>'.includes(last)) {
      dropped = last + dropped;
      kept = kept.slice(0, -1);
      continue;
    }
    if (last === ')' || last === ']' || last === '}') {
      const open = last === ')' ? '(' : last === ']' ? '[' : '{';
      const opens = (kept.match(new RegExp('\\' + open, 'g')) ?? []).length;
      const closes = (kept.match(new RegExp('\\' + last, 'g')) ?? []).length;
      if (closes > opens) {
        dropped = last + dropped;
        kept = kept.slice(0, -1);
        continue;
      }
    }
    break;
  }
  return { kept, dropped };
}

function hasSkippedAncestor(node: Node): boolean {
  for (let p: Node | null = node.parentNode; p; p = p.parentNode) {
    if (p.nodeType === 1 /* ELEMENT_NODE */) {
      if (LINKIFY_SKIP_ANCESTORS.has((p as Element).tagName)) {
        return true;
      }
    }
  }
  return false;
}

function linkifyTextNodes(root: ParentNode): void {
  // Collect text nodes first so the live tree mutation does not
  // confuse the walker. Using TreeWalker rather than recursion
  // because a typed iteration over text nodes is straightforward
  // and matches the DOM stdlib idiom.
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const targets: Text[] = [];
  let current: Node | null = walker.nextNode();
  while (current) {
    const t = current as Text;
    if (t.nodeValue && LINKIFY_RE.test(t.nodeValue) && !hasSkippedAncestor(t)) {
      targets.push(t);
    }
    LINKIFY_RE.lastIndex = 0;
    current = walker.nextNode();
  }
  for (const node of targets) {
    linkifyOneTextNode(node);
  }
}

function linkifyOneTextNode(node: Text): void {
  const text = node.nodeValue ?? '';
  const frag = document.createDocumentFragment();
  let lastIndex = 0;
  LINKIFY_RE.lastIndex = 0;
  for (;;) {
    const m = LINKIFY_RE.exec(text);
    if (!m) break;
    const start = m.index;
    const raw = m[0];
    const { kept, dropped } = trimTrailingPunctuation(raw);
    if (start > lastIndex) {
      frag.appendChild(document.createTextNode(text.slice(lastIndex, start)));
    }
    const a = document.createElement('a');
    let href = kept;
    if (kept.toLowerCase().startsWith('www.')) {
      href = 'https://' + kept;
    }
    a.setAttribute('href', href);
    a.textContent = kept;
    frag.appendChild(a);
    if (dropped) {
      frag.appendChild(document.createTextNode(dropped));
    }
    lastIndex = start + raw.length;
  }
  if (lastIndex < text.length) {
    frag.appendChild(document.createTextNode(text.slice(lastIndex)));
  }
  node.parentNode?.replaceChild(frag, node);
}

/**
 * Extract a positive pixel value for a CSS property from a raw style
 * string. Returns null when the property is absent, not in px units,
 * or not a positive finite number.
 *
 * The regex matches the property name at a declaration boundary (start
 * of string or after a semicolon) so that a prefix like `max-width`
 * cannot shadow `width`.
 */
function parsePxFromStyle(style: string, prop: string): number | null {
  const re = new RegExp(
    `(?:^|;)\\s*${prop}\\s*:\\s*(\\d+(?:\\.\\d+)?)\\s*px`,
    'i',
  );
  const m = re.exec(style);
  if (!m?.[1]) return null;
  const v = parseFloat(m[1]);
  return isFinite(v) && v > 0 ? Math.round(v) : null;
}

/**
 * Promote pixel dimensions from an <img>'s inline style attribute to
 * HTML width/height attributes so the browser can apply an implicit
 * aspect-ratio and reserve layout space before the image loads (issue
 * #47).
 *
 * Only missing attributes are filled in — existing HTML attributes are
 * never overwritten. Only px values are accepted; relative units
 * (%, em, rem, …) are skipped because they cannot be converted to
 * meaningful absolute attribute values without knowing the containing
 * block.
 *
 * No style content other than the extracted numeric pixel values is
 * forwarded to the attribute, so this cannot smuggle arbitrary CSS
 * through the sanitiser.
 */
function promoteStyleDimensions(img: Element): void {
  const style = img.getAttribute('style');
  if (!style) return;
  if (!img.hasAttribute('width')) {
    const w = parsePxFromStyle(style, 'width');
    if (w !== null) img.setAttribute('width', String(w));
  }
  if (!img.hasAttribute('height')) {
    const h = parsePxFromStyle(style, 'height');
    if (h !== null) img.setAttribute('height', String(h));
  }
}

/**
 * Append `aspect-ratio: W / H` to the img's inline style so the browser
 * can reserve layout space before the image bytes arrive (issue #47).
 *
 * Safety invariants:
 *   - W and H are validated as positive finite numbers; NaN / Infinity /
 *     negative / zero values are silently dropped so nothing executable
 *     can be smuggled into the style declaration.
 *   - An existing `aspect-ratio` declaration is detected and skipped so
 *     this function is idempotent.
 *   - The author's width and height HTML attributes are never touched here;
 *     only the inline style is extended.
 */
function applyAspectRatio(img: Element, w: number, h: number): void {
  if (!Number.isFinite(w) || !Number.isFinite(h) || w <= 0 || h <= 0) return;
  const wi = Math.round(w);
  const hi = Math.round(h);
  // After rounding, values below 0.5 become 0 — skip those too.
  if (wi <= 0 || hi <= 0) return;
  const existing = (img.getAttribute('style') ?? '').trim();
  // Do not duplicate an aspect-ratio that is already present (e.g. authored
  // by the message itself or added by an earlier sanitiser pass).
  if (/(?:^|;)\s*aspect-ratio\s*:/i.test(existing)) return;
  const declaration = `aspect-ratio: ${wi} / ${hi}`;
  const newStyle = existing
    ? existing.endsWith(';')
      ? `${existing} ${declaration}`
      : `${existing}; ${declaration}`
    : declaration;
  img.setAttribute('style', newStyle);
}

function rewriteImage(img: Element, options: SanitizeOptions): void {
  const src = img.getAttribute('src');
  const alt = img.getAttribute('alt') ?? '';
  if (!src) {
    return;
  }
  // cid: inline images — resolve via the per-message attachment map
  // when present; otherwise leave the placeholder data attribute so the
  // user knows the image is missing.
  if (src.startsWith('cid:')) {
    const cid = src.slice(4).trim();
    const resolved = options.cidMap?.[cid];
    if (resolved) {
      img.setAttribute('src', resolved);
      img.setAttribute('referrerpolicy', 'no-referrer');
      img.setAttribute('loading', 'lazy');
      // Promote inline-style pixel dimensions to HTML attributes so the
      // browser can compute an implicit aspect-ratio and reserve layout
      // space before the image bytes arrive (issue #47). When both width
      // and height attributes are already present (the common marketing-
      // HTML case) the function is a no-op. The style attribute itself is
      // left untouched — this only adds the missing HTML attributes.
      promoteStyleDimensions(img);
      // Apply server-supplied intrinsic aspect-ratio (issue #47 cid-dimension
      // path). When the JMAP response carries width/height on the body part,
      // inject `aspect-ratio: W / H` into the inline style so the browser
      // can reserve the correct height at the author's display width before
      // the image bytes arrive. The author's width/height attributes are
      // never overwritten — the intrinsic dimensions may be a retina
      // multiple of the author's intended display width.
      const dims = options.cidDimensions?.[cid];
      if (dims) {
        applyAspectRatio(img, dims.width, dims.height);
      }
      return;
    }
    img.removeAttribute('src');
    img.setAttribute('alt', alt || '[inline image]');
    img.setAttribute('data-herold-blocked', 'cid');
    return;
  }
  // Server-emitted internalize placeholder (REQ-EXTIMG-BG-INTERNAL-40):
  // Email/get rewrites every external <img src> to a self-contained
  // 1x1 transparent GIF data URI when the message row carries
  // InternalizePending = true. Without this pass-through the http(s)-
  // only allowlist below strips the src and the user sees the broken-
  // image icon (the failure mode reported on 2026-05-10 against
  // pending Google-receipt mail). The allowlist is the literal
  // placeholder prefix only, so user-supplied bodies cannot smuggle
  // inline images past the external-fetch gate.
  if (isInternalizePlaceholder(src)) {
    return;
  }
  // Inline raster data URIs are passed through unchanged. They make
  // no network request (so the external-image gate has nothing to
  // gate) and cannot execute script when used as <img src>. Mail
  // commonly uses them for logos and signature graphics — stripping
  // them is what produced the broken-image icon reported on
  // 2026-05-26. SVG is excluded: it can embed <script> and external
  // references, so it does not satisfy the "inert payload" property
  // the rest of the allowlist relies on.
  if (INLINE_IMAGE_DATA_URI.test(src)) {
    return;
  }
  if (!/^https?:/i.test(src)) {
    // Anything other than http(s) — remove.
    img.removeAttribute('src');
    img.setAttribute('alt', alt || '[image]');
    return;
  }
  if (!options.loadImages) {
    img.removeAttribute('src');
    img.setAttribute('alt', alt || '[image blocked]');
    img.setAttribute('data-herold-blocked', 'external');
    return;
  }
  // External + loading enabled — proxy.
  img.setAttribute('src', `/proxy/image?url=${encodeURIComponent(src)}`);
  img.setAttribute('referrerpolicy', 'no-referrer');
  img.setAttribute('loading', 'lazy');
}

/**
 * True when a resolved CSS color value (as read off a
 * CSSStyleDeclaration) represents an actually declared color, as
 * opposed to "nothing was declared here". `''` is the DOM's answer for
 * "property never set"; `background` shorthands that carry no color
 * layer at all (e.g. `url(x) no-repeat`) resolve their `background-color`
 * sub-property to the literal keyword `initial`.
 */
function isDeclaredColor(value: string): boolean {
  const v = value.trim().toLowerCase();
  return v !== '' && v !== 'initial';
}

/**
 * `currentColor` never constitutes an independently declared half of a
 * color pair: as `color`'s own value it is a pass-through to whatever
 * this element would inherit anyway (identical to not declaring `color`
 * at all); as `background-color` (longhand or resolved out of a
 * `background` shorthand) it is *tautologically* identical to this same
 * element's foreground, so it can never provide independent contrast --
 * `color:red; background-color:currentColor` paints solid red on red,
 * whatever `red` ends up being. Detecting the literal keyword is enough;
 * no color math is needed for this rule.
 */
function isCurrentColorKeyword(value: string): boolean {
  return value.trim().toLowerCase() === 'currentcolor';
}

/**
 * Parse a CSS color value into concrete, unpremultiplied sRGBA
 * components in [0, 255] (alpha in [0, 1]), or `null` when the syntax
 * is not one of the finite, well-defined forms this function covers:
 * hex (#rgb/#rgba/#rrggbb/#rrggbbaa), rgb()/rgba(), hsl()/hsla() (both
 * legacy comma and modern space/slash argument syntax), and a modest
 * set of CSS keyword colors.
 *
 * This is used ONLY by `colorsAreIndistinguishable` below, to compare
 * two colors the CSSOM has already told us are independently declared.
 * It never decides "is a color declared at all" -- that question is
 * answered entirely by `isDeclaredColor` against the browser's own
 * `CSSStyleDeclaration`, which handles every CSS color syntax including
 * ones this parser does not (`oklch()`, `lab()`, `color-mix()`, ...).
 * An unresolvable value here simply means "cannot prove these two
 * colors are the same", which safely defaults to leaving the sender's
 * pair untouched -- never to stripping it.
 */
function parseCssColorForComparison(
  value: string,
): [number, number, number, number] | null {
  const v = value.trim().toLowerCase();
  if (v === '') return null;

  const hex = /^#([0-9a-f]{3}|[0-9a-f]{4}|[0-9a-f]{6}|[0-9a-f]{8})$/.exec(v);
  if (hex) {
    const h = hex[1]!;
    const chan = (s: string) => parseInt(s.length === 1 ? s + s : s, 16);
    if (h.length <= 4) {
      const [r, g, b, a] = h.split('');
      return [chan(r!), chan(g!), chan(b!), a !== undefined ? chan(a) / 255 : 1];
    }
    return [
      chan(h.slice(0, 2)),
      chan(h.slice(2, 4)),
      chan(h.slice(4, 6)),
      h.length === 8 ? chan(h.slice(6, 8)) / 255 : 1,
    ];
  }

  const fn = /^(rgba?|hsla?)\(([^)]*)\)$/.exec(v);
  if (fn) {
    const kind = fn[1]!.startsWith('rgb') ? 'rgb' : 'hsl';
    const args = fn[2]!.split(/[\s,/]+/).map((s) => s.trim()).filter(Boolean);
    if (args.length < 3) return null;
    const alpha = parseAlphaComponent(args[3]);
    if (alpha === null) return null;
    if (kind === 'rgb') {
      const r = parseRgbChannel(args[0]);
      const g = parseRgbChannel(args[1]);
      const b = parseRgbChannel(args[2]);
      if (r === null || g === null || b === null) return null;
      return [r, g, b, alpha];
    }
    const h = parseHueComponent(args[0]);
    const s = parsePercentComponent(args[1]);
    const l = parsePercentComponent(args[2]);
    if (h === null || s === null || l === null) return null;
    return [...hslToRgb(h, s, l), alpha];
  }

  const named = NAMED_COLORS_FOR_COMPARISON[v];
  if (named) return [named[0], named[1], named[2], 1];
  return null;
}

function parseRgbChannel(tok: string | undefined): number | null {
  if (tok === undefined) return null;
  if (tok.endsWith('%')) {
    const pct = parseFloat(tok);
    return isFinite(pct) ? clampByte((pct / 100) * 255) : null;
  }
  const n = parseFloat(tok);
  return isFinite(n) ? clampByte(n) : null;
}

function parsePercentComponent(tok: string | undefined): number | null {
  if (tok === undefined || !tok.endsWith('%')) return null;
  const pct = parseFloat(tok);
  return isFinite(pct) ? Math.min(100, Math.max(0, pct)) / 100 : null;
}

function parseHueComponent(tok: string | undefined): number | null {
  if (tok === undefined) return null;
  const n = parseFloat(tok); // deg suffix parses as its numeric prefix
  return isFinite(n) ? ((n % 360) + 360) % 360 : null;
}

function parseAlphaComponent(tok: string | undefined): number | null {
  if (tok === undefined) return 1;
  if (tok.endsWith('%')) {
    const pct = parseFloat(tok);
    return isFinite(pct) ? Math.min(1, Math.max(0, pct / 100)) : null;
  }
  const n = parseFloat(tok);
  return isFinite(n) ? Math.min(1, Math.max(0, n)) : null;
}

function clampByte(n: number): number {
  return Math.round(Math.min(255, Math.max(0, n)));
}

/** Standard HSL -> sRGB conversion (CSS Color Level 3 formula). */
function hslToRgb(h: number, s: number, l: number): [number, number, number] {
  const c = (1 - Math.abs(2 * l - 1)) * s;
  const hp = h / 60;
  const x = c * (1 - Math.abs((hp % 2) - 1));
  let [r1, g1, b1] = [0, 0, 0];
  if (hp < 1) [r1, g1, b1] = [c, x, 0];
  else if (hp < 2) [r1, g1, b1] = [x, c, 0];
  else if (hp < 3) [r1, g1, b1] = [0, c, x];
  else if (hp < 4) [r1, g1, b1] = [0, x, c];
  else if (hp < 5) [r1, g1, b1] = [x, 0, c];
  else [r1, g1, b1] = [c, 0, x];
  const m = l - c / 2;
  return [clampByte((r1 + m) * 255), clampByte((g1 + m) * 255), clampByte((b1 + m) * 255)];
}

// Modest keyword table for the comparison parser above -- CSS1 basic 16
// plus the extended names issue #231 was specifically reported against
// (the old detection-side keyword list this replaced covered only 41 of
// the 148 CSS Color Level 4 names; incompleteness here is safe by
// construction, since it only ever weakens the NEW same-color check
// below toward "cannot prove, so preserve" -- it never weakens whether a
// color is detected as declared at all, which the CSSOM already answers
// completely in `isDeclaredColor`).
const NAMED_COLORS_FOR_COMPARISON: Record<string, [number, number, number]> = {
  black: [0, 0, 0], silver: [192, 192, 192], gray: [128, 128, 128], grey: [128, 128, 128],
  white: [255, 255, 255], maroon: [128, 0, 0], red: [255, 0, 0], purple: [128, 0, 128],
  fuchsia: [255, 0, 255], green: [0, 128, 0], lime: [0, 255, 0], olive: [128, 128, 0],
  yellow: [255, 255, 0], navy: [0, 0, 128], blue: [0, 0, 255], teal: [0, 128, 128],
  aqua: [0, 255, 255], orange: [255, 165, 0],
  midnightblue: [25, 25, 112], darkblue: [0, 0, 139], darkred: [139, 0, 0],
  darkgreen: [0, 100, 0], dimgray: [105, 105, 105], dimgrey: [105, 105, 105],
  darkslategray: [47, 79, 79], darkslategrey: [47, 79, 79], firebrick: [178, 34, 34],
  saddlebrown: [139, 69, 19], indianred: [205, 92, 92], sienna: [160, 82, 45],
};

function relativeLuminanceForComparison([r, g, b]: [number, number, number, number]): number {
  const chan = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * chan(r) + 0.7152 * chan(g) + 0.0722 * chan(b);
}

/**
 * Below this WCAG contrast ratio, two independently declared colors are
 * treated as painting the same practical color -- i.e. a "complete pair"
 * that is actually invisible by construction (issue #231 adversarial
 * case: `color:red; background-color:currentColor`, and the equivalent
 * `color:#f00; background-color:#ff0000`). Chosen well below any
 * accessibility compliance threshold (WCAG AA is 3:1 for large text,
 * 4.5:1 for normal text) so this rule only catches colors that are
 * visually the same or nearly so, and never strips a sender's
 * deliberate low-but-legible color choice -- e.g. `#999999` on white is
 * ~2.85:1 and stays untouched; a lone-half true collision like the
 * reported `rgb(26,17,17)` background against the iframe's own default
 * text color measures ~1.02:1, and `currentColor` measures exactly
 * 1:1 -- both are comfortably below this threshold, and nothing a
 * sender would plausibly choose on purpose is this close.
 */
const DEGENERATE_CONTRAST_THRESHOLD = 1.5;

/**
 * True when `colorValue` and `bgValue` (two independently declared,
 * CSSOM-confirmed colors) resolve to practically the same paint color.
 * Colors that either parser cannot resolve, or that carry any
 * transparency (compositing a translucent color against its actual
 * backdrop needs ambient context this detached-fragment pass does not
 * have), are treated as "cannot determine" and never reported as
 * indistinguishable -- see `parseCssColorForComparison`'s doc comment on
 * why an unresolvable value must never cause a strip.
 */
function colorsAreIndistinguishable(colorValue: string, bgValue: string): boolean {
  const c = parseCssColorForComparison(colorValue);
  const b = parseCssColorForComparison(bgValue);
  if (!c || !b) return false;
  if (c[3] < 1 || b[3] < 1) return false;
  const lc = relativeLuminanceForComparison(c);
  const lb = relativeLuminanceForComparison(b);
  const ratio = (Math.max(lc, lb) + 0.05) / (Math.min(lc, lb) + 0.05);
  return ratio < DEGENERATE_CONTRAST_THRESHOLD;
}

/**
 * Honour a sender's inline foreground/background color declaration only
 * when BOTH halves of the pair are present on the same element. When only
 * one half is declared, the sender did not fully specify a foreground/
 * background pair — leaving the other half to inherit from the reading
 * pane's own theme (`wrapInIframeDocument`'s light/dark body color) can
 * produce a combination the sender never intended and never saw, including
 * unreadable near-invisible text (issue #231). Stripping the lone half
 * makes the element fall back to Herold's own paired foreground and
 * background, which are always mutually legible in both themes.
 *
 * Detection goes through the browser's own CSS parser rather than
 * scanning the style string: the declaration is assigned to a detached
 * probe element and the resolved `color` / `background-color` are read
 * back off its `CSSStyleDeclaration`. That is what correctly handles:
 *   - the `background` shorthand (image/position/repeat plus an
 *     optional color layer) -- the parser splits it into its
 *     sub-properties for us;
 *   - all CSS named colors, not a curated subset -- there is no keyword
 *     list to fall short of the full Color Level 4 table;
 *   - `url(data:image/gif;base64,...)` values, which contain a bare
 *     semicolon that broke a naive "split the style string on ;"
 *     approach. That data URI is not a hypothetical: `internal/extimg`'s
 *     server-side internalize/placeholder pass (see
 *     `internal/protojmap/mail/email/get.go`, `render.go`) rewrites
 *     `url(...)` inside inline `style=""` to exactly that shape for
 *     every message still pending background internalize -- the default
 *     state of every freshly received HTML message;
 *   - `!important` and any future CSS color syntax (`color-mix()`,
 *     `oklch()`, ...) -- it IS the parser the page will otherwise use to
 *     render the same declaration.
 *
 * "Background" is recognized from either the `background-color` longhand
 * or a `background` shorthand that carries a color component -- a sender
 * using `background:` instead of `background-color:` gets the same
 * protection. When the lone background half comes from a shorthand that
 * also carries image/position/repeat, the entire shorthand declaration is
 * dropped rather than surgically extracting just the color token: this
 * trades rare loss of an accompanying background-image in that specific
 * mixed, no-`color` case for a simple, unambiguous transformation with no
 * way to leave a residual color behind. A shorthand with NO color
 * component at all (image/repeat/position only) is never touched --
 * `probe.style.backgroundColor` resolves to `initial` for it, so it does
 * not count as "background declared" and cannot trigger stripping of an
 * unrelated lone `color`.
 *
 * An element that declares both halves is left untouched -- its original
 * style attribute text is kept byte-for-byte rather than reserialized
 * through the probe, so formatting/spacing is undisturbed. That is a
 * complete, self-consistent pair the sender chose deliberately, and
 * removing either half would break intentional styling (e.g. a callout
 * box with light text on a dark brand color, or a `background: url(x)
 * #fff` shorthand paired with an explicit `color`).
 *
 * Because CSS `color`/`background-color` are inherited/painted per
 * element, not merged across ancestors, checking each styled element in
 * isolation is sufficient to close the reported collision: the failure
 * mode is one element's declared half meeting the iframe body's inherited
 * half, not two different elements' halves meeting each other.
 *
 * Known trade-off (safe, not a bug): a NESTED cross-element pair -- a
 * parent declaring only `background-color` and a child declaring only
 * `color` -- has both halves stripped independently, because each
 * element is evaluated on its own declarations. That can never produce
 * illegible text (each element falls back to Herold's own paired
 * theme colors), but it does lose the sender's intended nested styling
 * (e.g. a colored panel with white text authored as two separate
 * elements rather than one). Detecting and preserving that case would
 * require resolving each element's *computed* style against its
 * ancestors rather than its own inline declarations, which this
 * sanitizer pass -- run on a detached DOMPurify fragment with no layout
 * -- cannot do.
 *
 * A "complete pair" is not automatically a real one, though: two further
 * checks run before a pair is accepted as-is.
 *   - `currentColor` is never counted as an independently declared half
 *     (see `isCurrentColorKeyword`) -- `color:red;
 *     background-color:currentColor` looks complete to a naive check
 *     but paints solid red-on-red. Stripped unconditionally before
 *     classification, so it always falls through to the lone-half (or
 *     no-half) branch below.
 *   - When both halves ARE independently, concretely declared, they are
 *     additionally checked for whether they resolve to practically the
 *     SAME paint color (`colorsAreIndistinguishable`, WCAG contrast
 *     ratio below `DEGENERATE_CONTRAST_THRESHOLD`) -- e.g.
 *     `color:#f00; background-color:#ff0000`. A pair that is invisible
 *     by construction is exactly as broken as a lone half and is
 *     dropped in full, falling back to Herold's own paired theme colors
 *     like any other undeclared pair. This check is deliberately
 *     conservative: an unresolvable color syntax (`oklch()`,
 *     `color-mix()`, ...) or any non-opaque alpha never triggers it, so
 *     it can under-detect exotic same-color pairs but can never
 *     over-strip a sender's legitimate, merely-low-contrast styling.
 *
 * Requires `document` (see the file header: `sanitizeHtml` already
 * depends on it via DOMPurify's `RETURN_DOM_FRAGMENT` mode and
 * `document.createTreeWalker`), so this carries no new environment
 * requirement. Verified equivalent between happy-dom (the vitest
 * environment) and a live Chrome instance for every fixture this file's
 * acceptance tests cover.
 */
function sanitizeInlineColorPairs(fragment: DocumentFragment): void {
  for (const el of fragment.querySelectorAll<HTMLElement>('[style]')) {
    const style = el.getAttribute('style');
    if (!style) continue;
    const probe = document.createElement('div');
    probe.setAttribute('style', style);

    if (isCurrentColorKeyword(probe.style.color)) probe.style.removeProperty('color');
    if (isCurrentColorKeyword(probe.style.backgroundColor)) {
      probe.style.removeProperty('background-color');
      probe.style.removeProperty('background');
    }

    let hasColor = isDeclaredColor(probe.style.color);
    let hasBackground = isDeclaredColor(probe.style.backgroundColor);

    if (
      hasColor &&
      hasBackground &&
      colorsAreIndistinguishable(probe.style.color, probe.style.backgroundColor)
    ) {
      hasColor = false;
      hasBackground = false;
      probe.style.removeProperty('color');
      probe.style.removeProperty('background-color');
      probe.style.removeProperty('background');
    } else if (hasColor !== hasBackground) {
      if (hasColor) probe.style.removeProperty('color');
      if (hasBackground) {
        probe.style.removeProperty('background-color');
        probe.style.removeProperty('background');
      }
    }
    // both true (a genuine, distinguishable, complete pair) or both
    // false (nothing declared, or both halves stripped above) -- no
    // further mutation.

    const kept = probe.getAttribute('style');
    if (kept === style) continue; // nothing changed -- keep original text byte-for-byte.
    if (kept) {
      el.setAttribute('style', kept);
    } else {
      el.removeAttribute('style');
    }
  }
}

/**
 * Wrap top-level quoted-history regions in <details><summary>...</summary>
 * so the iframe collapses them by default and the user expands them with
 * a single click. The detection rule: first <blockquote> child of <body>,
 * or a <div class*="gmail_quote"|"yahoo_quoted">. Only the *first* such
 * region per body is wrapped — nested replies inside a quote remain
 * inert because they are deeper in the tree.
 *
 * A quoted region is collapsed ONLY when it is trailing: no fresh
 * (non-quoted, non-whitespace) content may follow it (or follow the
 * quote/empty siblings that would be absorbed with it) — UNLESS that
 * content is a trailing signature block, which folds away with the quote
 * instead of blocking the collapse (re #234; see the forward-sweep comment
 * below). Bottom-posted and interleaved replies leave genuine fresh
 * content after the quote — in those cases the quote is left expanded so
 * the context remains readable (issue #49).
 *
 * A citation-introducer paragraph preceding the quote (e.g. compose.svelte.ts's
 * own `<p>On {date}, {sender} wrote:</p><blockquote>`, or the same shape with
 * a newline text node between the tags as real mail clients serialize it) is
 * folded into the collapsed region too (re #234) — see the backward sweep
 * below.
 */
function collapseQuotedRegions(fragment: DocumentFragment): void {
  const root = fragment;
  const candidate = findFirstQuotedRegion(root);
  if (!candidate) return;

  // Guard: only collapse when the quoted region is truly trailing. Walk
  // past the contiguous block of quote-or-empty siblings that would be
  // absorbed with the candidate. Reaching a signature-delimiter node ends
  // the guard successfully: from there to the end of the message is a
  // signature block, which folds away with the quote unconditionally (re
  // #234), mirroring the plain-text contract (quoted.ts splitQuotedText)
  // that a signature block runs to the end of the message. Reaching
  // anything else non-quote-or-empty means genuine fresh content follows
  // and the quote must stay expanded.
  let probe: Node | null = candidate.nextSibling;
  let foundSignature = false;
  while (probe !== null) {
    if (isSignatureDelimiterNode(probe)) {
      foundSignature = true;
      break;
    }
    if (!isQuoteOrEmptyNode(probe)) break;
    probe = probe.nextSibling;
  }
  if (!foundSignature && probe !== null) {
    // Fresh content follows the quoted group — leave it expanded.
    return;
  }

  const owner = candidate.ownerDocument!;
  const details = owner.createElement('details');
  details.setAttribute('class', 'herold-quoted');
  const summary = owner.createElement('summary');
  // The visible label ("···" / "Hide trimmed content") is supplied by the
  // CSS ::before pseudo-element so that it toggles with the details[open]
  // state without JavaScript. The textContent must be empty; setting it to
  // the label text caused "···Show trimmed content" / "Hide trimmed
  // contentShow trimmed content" to appear (both the pseudo-element text
  // and the node text rendered side-by-side).
  summary.setAttribute('aria-label', 'Show trimmed content');
  details.appendChild(summary);

  // Backward sweep: fold a citation-introducer line preceding the quote
  // into the collapsed region too (re #234). Skips whitespace-only /
  // empty siblings first, using the SAME isQuoteOrEmptyNode predicate the
  // forward sweep above uses -- real mail clients (Thunderbird and
  // essentially every other client) serialize a newline text node between
  // block-level tags ("<p>...wrote:</p>\n<blockquote>..."), unlike
  // compose.svelte.ts's own zero-whitespace output. Without this skip the
  // introducer's immediate previous sibling is that whitespace text node,
  // not the introducer paragraph, and the introducer is missed entirely.
  // At most one non-empty node is inspected, matching the plain-text
  // contract of at most one attribution line per citation.
  let introducer: Node | null = candidate.previousSibling;
  while (introducer !== null && isQuoteOrEmptyNode(introducer)) {
    introducer = introducer.previousSibling;
  }
  if (introducer === null || !isAttributionNode(introducer)) {
    introducer = null;
  }

  candidate.parentNode?.insertBefore(details, introducer ?? candidate);
  if (introducer !== null) {
    // Walk forward from the introducer to the candidate, absorbing it and
    // every node in between (the whitespace/empty siblings the backward
    // skip just walked past) into the collapsed region, in source order.
    let n: Node | null = introducer;
    while (n !== null && n !== candidate) {
      const after: Node | null = n.nextSibling;
      details.appendChild(n);
      n = after;
    }
  }
  details.appendChild(candidate);

  // Absorb trailing siblings: quote-or-empty nodes as before — quoted
  // history sometimes continues outside the first <blockquote> with an
  // attribution div or a <hr>. Once a signature-delimiter node is reached,
  // absorb it and every remaining sibling unconditionally (re #234): the
  // signature block runs to the end of the message regardless of its own
  // content (the addressee's name, postal address, etc. are all real text
  // that would otherwise fail the quote-or-empty check).
  let next = details.nextSibling;
  let inSignature = false;
  while (next) {
    if (!inSignature) {
      if (isSignatureDelimiterNode(next)) {
        inSignature = true;
      } else if (!isQuoteOrEmptyNode(next)) {
        break;
      }
    }
    const after = next.nextSibling;
    details.appendChild(next);
    next = after;
  }
}

/**
 * Returns true for nodes that should be swept into the collapsed
 * <details> region alongside the first quoted block:
 *  - whitespace-only text nodes
 *  - <br> and <hr> elements (separators with no text content)
 *  - additional quoted blocks (<blockquote>, gmail_quote-class divs)
 *  - structurally empty elements (no visible text content)
 *
 * Returns false for elements with substantive non-quoted text so
 * that fresh reply content (e.g. a bottom-posted reply after a
 * blockquote as Apple Mail emits) remains visible outside the
 * collapsed region.
 */
function isQuoteOrEmptyNode(node: Node): boolean {
  if (node.nodeType === Node.TEXT_NODE) {
    return !(node.nodeValue?.trim());
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return false;
  const el = node as Element;
  if (el.tagName === 'BR' || el.tagName === 'HR') return true;
  if (el.tagName === 'BLOCKQUOTE') return true;
  if (el.tagName === 'DIV') {
    const cls = el.getAttribute('class') ?? '';
    if (/gmail_quote|yahoo_quoted|moz-cite-prefix/i.test(cls)) return true;
  }
  // An element whose visible text is entirely whitespace is empty.
  return !(el.textContent?.trim());
}

/**
 * True when `node`'s full text content, trimmed, is exactly the RFC 3676
 * signature delimiter ("-- " or "--") — e.g. compose.svelte.ts's own
 * `<p>-- </p>`. Marks the start of a trailing signature block, which folds
 * into the collapsed quoted region (re #234).
 */
function isSignatureDelimiterNode(node: Node): boolean {
  if (node.nodeType === Node.TEXT_NODE) {
    return SIG_DELIM_RE.test((node.nodeValue ?? '').trim());
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return false;
  return SIG_DELIM_RE.test(((node as Element).textContent ?? '').trim());
}

/**
 * True when `node`'s full text content, trimmed, is an auto-generated
 * citation-introducer line (re #234) rather than genuine new text. Shared
 * with the plain-text heuristic in quoted.ts so both render paths fold the
 * same set of introducer shapes.
 */
function isAttributionNode(node: Node): boolean {
  if (node.nodeType === Node.TEXT_NODE) {
    return isAttributionLine((node.nodeValue ?? '').trim());
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return false;
  return isAttributionLine(((node as Element).textContent ?? '').trim());
}

function findFirstQuotedRegion(root: ParentNode): Element | null {
  // Walk in document order; the first match wins.
  const walker = root.querySelectorAll
    ? root.querySelectorAll('blockquote, div, hr')
    : null;
  if (!walker) return null;
  for (const el of walker) {
    if (el.tagName === 'BLOCKQUOTE') return el;
    if (el.tagName === 'DIV') {
      const cls = el.getAttribute('class') ?? '';
      if (/gmail_quote|yahoo_quoted|moz-cite-prefix/i.test(cls)) return el;
    }
  }
  return null;
}

/**
 * Server-emitted internalize placeholder (REQ-EXTIMG-BG-INTERNAL-41).
 * The selector matches the literal prefix of extimg.PlaceholderDataURI
 * only -- never a generic data: selector -- so user-supplied bodies
 * cannot leverage this style for their own data URIs. Sized to a
 * visible gray box so the user opening an image-heavy email sees
 * *where* the images will land while the worker drains, instead of
 * the 1x1 transparent GIF collapsing the layout and orphaning the
 * "Bilder werden verarbeitet" banner.
 *
 * Only injected into the iframe stylesheet when the caller confirms this
 * specific message is still `InternalizePending` (see `wrapInIframeDocument`).
 * A prefix-only `img[src^=...]` selector cannot tell "herold's own pending
 * placeholder" from "a real, already-internalized image that happens to
 * start with the same bytes" (a minimal transparent tracking-pixel GIF is a
 * near-universal byte pattern many ESPs reuse verbatim), so gating on the
 * message's own state is what keeps genuine tracking pixels from being
 * forced into 96px-tall gray blocks after internalizing finishes (issue #160).
 */
const INTERNALIZE_PLACEHOLDER_CSS = `img[src^="${INTERNALIZE_PLACEHOLDER_PREFIX}"] {
    display: block;
    min-height: 6em;
    width: 100%;
    max-width: 100%;
    background: #f4f4f4;
  }`;

/**
 * Wrap the sanitised body in a minimal HTML document with an inline CSP
 * and base styles. The iframe sandbox + same-origin parent combination
 * means `'self'` in the CSP refers to the suite origin where the image
 * proxy lives.
 */
function wrapInIframeDocument(body: string, internalizePending: boolean): string {
  const csp =
    "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self';";
  // The "Hide trimmed content" label is supplied by the iframe's CSS
  // ::before pseudo-element rather than a JS-rendered string, so we
  // interpolate the translation here at sanitize-time. CSS string
  // values escape with backslash-hex; this label is plain ASCII once
  // the translation is fetched, so we just pass the raw text in
  // double quotes after JSON-escaping any embedded quote characters.
  const hideLabel = JSON.stringify(t('mail.trimmed.hide'));
  return `<!doctype html>
<html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><style>
  html, body { margin: 0; padding: 0; }
  body {
    font-family: 'IBM Plex Sans', system-ui, -apple-system, 'Segoe UI', sans-serif;
    font-size: 16px;
    line-height: 1.5;
    color: #161616;
    background: #ffffff;
    word-wrap: break-word;
    /* Establishes a block formatting context so the bottom margin of the
       last body child (e.g. a trailing <p>'s default UA margin) stays
       inside body's own border box instead of collapsing through it.
       Without this, body.scrollHeight under-reports the true content
       height by the collapsed margin and the parent HtmlBody.svelte
       iframe is sized too short, clipping the last line (issue #158). */
    display: flow-root;
  }
  @media (prefers-color-scheme: dark) {
    body { color: #f4f4f4; background: #161616; }
  }
  a { color: #0f62fe; }
  img { max-width: 100%; height: auto; }
  ${internalizePending ? INTERNALIZE_PLACEHOLDER_CSS : ''}
  blockquote {
    border-left: 3px solid #c6c6c6;
    margin: 0 0 0 8px;
    padding: 0 0 0 12px;
    color: #525252;
  }
  pre { white-space: pre-wrap; word-break: break-word; }
  /* A concatenated text/plain part promoted into htmlBody (re #258 —
     e.g. a forwarder's own note ahead of a forwarded HTML original) is
     wrapped in this class by emailHtmlBody so it reads in the same
     proportional font as the rest of the message body. A bare <pre>
     (or <code>) genuinely authored in real HTML content — an actual
     code sample — is intentionally left out of this rule and stays
     monospace via the browser/UA default, per the maintainer's rule
     that fixed-width only applies where explicitly requested. */
  pre.herold-plain-part {
    font-family: inherit;
    white-space: pre-wrap;
    word-break: break-word;
  }
  table { border-collapse: collapse; max-width: 100%; }
  details.herold-quoted { margin-top: 8px; }
  details.herold-quoted > summary {
    cursor: pointer;
    list-style: none;
    display: inline-block;
    padding: 2px 12px;
    margin-bottom: 8px;
    background: #f4f4f4;
    color: #525252;
    border-radius: 16px;
    font-size: 14px;
  }
  details.herold-quoted > summary::-webkit-details-marker { display: none; }
  details.herold-quoted > summary::before { content: "···"; letter-spacing: 1px; }
  details.herold-quoted[open] > summary { background: #e0e0e0; }
  details.herold-quoted[open] > summary::before { content: ${hideLabel}; letter-spacing: normal; }
  @media (prefers-color-scheme: dark) {
    details.herold-quoted > summary { background: #393939; color: #c6c6c6; }
    details.herold-quoted[open] > summary { background: #525252; }
  }
  /* Divider between concatenated top-level htmlBody parts (re #258
     follow-up): a labeled rule so a forwarder's note and the forwarded
     original no longer read as one continuous message. Injected as
     trusted markup by emailHtmlBody (types.ts), never derived from
     message content. */
  .herold-part-separator {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 20px 0;
    color: #525252;
    font-size: 13px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .herold-part-separator::before,
  .herold-part-separator::after {
    content: "";
    flex: 1;
    height: 1px;
    background: #c6c6c6;
  }
  @media (prefers-color-scheme: dark) {
    .herold-part-separator { color: #c6c6c6; }
    .herold-part-separator::before,
    .herold-part-separator::after { background: #525252; }
  }
</style></head><body>${body}</body></html>`;
}
