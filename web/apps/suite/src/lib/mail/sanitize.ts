/**
 * HTML mail sanitisation per docs/architecture/04-rendering.md.
 *
 * Layered defence:
 *   1. DOMPurify drops unsafe tags / attributes / URL schemes.
 *   2. Anchors get target="_blank" rel="noopener noreferrer".
 *   3. Images:
 *      - cid: → blocked (inline images use the same opt-in as external).
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
}

const ALLOWED_TAGS = [
  'a', 'abbr', 'address', 'b', 'blockquote', 'br', 'caption',
  'cite', 'code', 'col', 'colgroup', 'div', 'dl', 'dt', 'dd',
  'em', 'figcaption', 'figure', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'hr', 'i', 'img', 'kbd', 'li', 'mark', 'ol', 'p', 'pre', 'q',
  's', 'samp', 'small', 'span', 'strong', 'sub', 'sup', 'table',
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

  // Anchor rewriting: every <a> opens in a new tab with no referrer leak.
  for (const a of fragment.querySelectorAll('a')) {
    a.setAttribute('target', '_blank');
    a.setAttribute('rel', 'noopener noreferrer');
  }

  // Image rewriting per REQ-SEC-05/07.
  for (const img of fragment.querySelectorAll('img')) {
    rewriteImage(img, options);
  }

  // Serialise back to HTML. (innerHTML round-trip preserves attribute changes.)
  const wrap = document.createElement('div');
  wrap.appendChild(fragment);
  const cleanBody = wrap.innerHTML;
  return wrapInIframeDocument(cleanBody);
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
      return;
    }
    img.removeAttribute('src');
    img.setAttribute('alt', alt || '[inline image]');
    img.setAttribute('data-herold-blocked', 'cid');
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
 * Wrap the sanitised body in a minimal HTML document with an inline CSP
 * and base styles. The iframe sandbox + same-origin parent combination
 * means `'self'` in the CSP refers to the suite origin where the image
 * proxy lives.
 */
function wrapInIframeDocument(body: string): string {
  const csp =
    "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self';";
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
  }
  @media (prefers-color-scheme: dark) {
    body { color: #f4f4f4; background: #161616; }
  }
  a { color: #0f62fe; }
  img { max-width: 100%; height: auto; }
  blockquote {
    border-left: 3px solid #c6c6c6;
    margin: 0 0 0 8px;
    padding: 0 0 0 12px;
    color: #525252;
  }
  pre { white-space: pre-wrap; word-break: break-word; }
  table { border-collapse: collapse; max-width: 100%; }
</style></head><body>${body}</body></html>`;
}
