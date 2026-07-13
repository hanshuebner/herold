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

export function sanitizeHtml(raw: string, options: SanitizeOptions): string {
  const fragment = DOMPurify.sanitize(raw, {
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
 * Split an inline style attribute value into individual declarations.
 * A plain split on ";" is sufficient here: sender inline styles are text
 * data (never executed), and the color/background-color values this
 * function inspects (keywords, #hex, rgb()/rgba()/hsl()) do not contain
 * semicolons, so no CSS-value-aware parser is needed.
 */
function splitStyleDeclarations(style: string): string[] {
  return style
    .split(';')
    .map((d) => d.trim())
    .filter((d) => d.length > 0);
}

/**
 * Honour a sender's inline `color` / `background-color` declaration only
 * when BOTH halves of the pair are present on the same element. When only
 * one half is declared, the sender did not fully specify a foreground/
 * background pair — leaving the other half to inherit from the reading
 * pane's own theme (`wrapInIframeDocument`'s light/dark body color) can
 * produce a combination the sender never intended and never saw, including
 * unreadable near-invisible text (issue #231). Stripping the lone half
 * makes the element fall back to Herold's own paired foreground and
 * background, which are always mutually legible in both themes.
 *
 * An element that declares both halves is left untouched: that is a
 * complete, self-consistent pair the sender chose deliberately, and
 * removing either half would break intentional styling (e.g. a callout
 * box with light text on a dark brand color).
 *
 * Because CSS `color`/`background-color` are inherited/painted per
 * element, not merged across ancestors, checking each styled element in
 * isolation is sufficient to close the reported collision: the failure
 * mode is one element's declared half meeting the iframe body's inherited
 * half, not two different elements' halves meeting each other.
 */
function sanitizeInlineColorPairs(fragment: DocumentFragment): void {
  for (const el of fragment.querySelectorAll<HTMLElement>('[style]')) {
    const style = el.getAttribute('style');
    if (!style) continue;
    const declarations = splitStyleDeclarations(style);
    const hasColor = declarations.some((d) => /^color\s*:/i.test(d));
    const hasBackground = declarations.some((d) => /^background-color\s*:/i.test(d));
    if (hasColor === hasBackground) continue; // both present or neither — leave as-is.
    const kept = declarations.filter((d) => {
      if (hasColor && /^color\s*:/i.test(d)) return false;
      if (hasBackground && /^background-color\s*:/i.test(d)) return false;
      return true;
    });
    if (kept.length > 0) {
      el.setAttribute('style', kept.join('; '));
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
 * quote/empty siblings that would be absorbed with it). Bottom-posted
 * and interleaved replies leave fresh content after the quote — in those
 * cases the quote is left expanded so the context remains readable
 * (issue #49).
 */
function collapseQuotedRegions(fragment: DocumentFragment): void {
  const root = fragment;
  const candidate = findFirstQuotedRegion(root);
  if (!candidate) return;

  // Guard: only collapse when the quoted region is truly trailing.
  // Walk past the contiguous block of quote-or-empty siblings that
  // would be absorbed with the candidate to find the first fresh
  // sibling. If one exists, the quote is not trailing (bottom-posted
  // or interleaved reply content follows) and must be left expanded.
  let probe: Node | null = candidate.nextSibling;
  while (probe !== null && isQuoteOrEmptyNode(probe)) {
    probe = probe.nextSibling;
  }
  if (probe !== null) {
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
  candidate.parentNode?.insertBefore(details, candidate);
  details.appendChild(candidate);
  // Absorb trailing siblings that are themselves quoted regions or empty
  // separators into <details> — quoted history sometimes continues outside
  // the first <blockquote> with an attribution div or a <hr>. The
  // pre-check above guarantees that every remaining sibling is
  // quote-or-empty, so the loop will reach the end without hitting the
  // break condition; the guard is kept as a defensive boundary.
  let next = details.nextSibling;
  while (next) {
    if (!isQuoteOrEmptyNode(next)) break;
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
</style></head><body>${body}</body></html>`;
}
