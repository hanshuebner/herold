/**
 * Live unread-count favicon badge — ported from favicon.js (design artifact).
 *
 * Renders a Gmail-style red badge over the envelope icon at both 16px and 32px,
 * then replaces the document's icon <link> tags with PNG data-URL entries so
 * the browser picks up the update without a page reload.
 *
 *   setMailFavicon(13);   // envelope + badge "13"
 *   setMailFavicon(0);    // plain envelope, no badge
 *
 * The badge shows up to two digits; counts above 99 render as "99+".
 * The envelope stroke flips between dark (#222a3d) and light (#e9ecef) based on
 * the OS prefers-color-scheme so the icon stays visible on both light and dark
 * browser chrome.
 *
 * Race safety: rasterization is asynchronous (SVG → Image → canvas → PNG).
 * If setMailFavicon is called again before a prior call's rasterization
 * completes, the stale result is discarded. Only the most-recent call's result
 * is applied to the DOM. This keeps the favicon badge in sync with the
 * document.title update, which is synchronous (re #71).
 */

interface SizeDesign {
  env: string;
  badge: string;
  // font size + baseline by digit count: [1 digit, 2 digits, 3 chars]
  fs: Record<1 | 2 | 3, [number, number]>;
}

const DESIGNS: Record<16 | 32, SizeDesign> = {
  16: {
    env:
      '<rect x="2.5" y="5.5" width="27" height="21" rx="3.5" fill="none" stroke="{S}" stroke-width="2.6"/>' +
      '<path d="M4.7 8.5 L16 16.8 L27.3 8.5" fill="none" stroke="{S}" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>',
    badge:
      '<rect x="10" y="11" width="20.5" height="20" rx="4.5" fill="#fa5252" stroke="#fff" stroke-width="1.5"/>' +
      '<text x="20.25" y="{Y}" font-size="{FS}" font-weight="bold" fill="#fff" text-anchor="middle" font-family="Arial,Helvetica,sans-serif">{N}</text>',
    fs: { 1: [18, 26.9], 2: [16.5, 26.7], 3: [12, 25.6] },
  },
  32: {
    env:
      '<rect x="2.5" y="5.5" width="27" height="21" rx="3.5" fill="none" stroke="{S}" stroke-width="2.2"/>' +
      '<path d="M4.7 8.5 L16 16.8 L27.3 8.5" fill="none" stroke="{S}" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>',
    badge:
      '<rect x="10" y="11" width="20.5" height="20" rx="4.5" fill="#fa5252" stroke="#fff" stroke-width="1.5"/>' +
      '<text x="20.25" y="{Y}" font-size="{FS}" font-weight="bold" fill="#fff" text-anchor="middle" font-family="Arial,Helvetica,sans-serif">{N}</text>',
    fs: { 1: [18, 26.9], 2: [16.5, 26.7], 3: [12, 25.6] },
  },
};

/**
 * Produce the SVG source for the envelope at the given pixel size and count.
 * Exported for testing: the SVG generation is a pure function with no DOM
 * dependencies.
 *
 * @param size  - 16 or 32 (px)
 * @param count - unread count; 0 means no badge
 */
export function svgFor(size: 16 | 32, count: number): string {
  const d = DESIGNS[size];
  const dark =
    typeof window !== 'undefined' &&
    window.matchMedia != null &&
    window.matchMedia('(prefers-color-scheme: dark)').matches;
  const stroke = dark ? '#e9ecef' : '#222a3d';
  let body = d.env.split('{S}').join(stroke);

  if (count > 0) {
    const n = count > 99 ? '99+' : String(count);
    const key = Math.min(n.length, 3) as 1 | 2 | 3;
    const spec = d.fs[key];
    body += d.badge
      .replace('{FS}', String(spec[0]))
      .replace('{Y}', String(spec[1]))
      .replace('{N}', n);
  }

  return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">' + body + '</svg>';
}

// Rasterizer function type. Exported for unit tests that inject a mock.
export type RasterizeFn = (
  size: 16 | 32,
  count: number,
) => Promise<{ size: number; url: string } | null>;

function rasterize(size: 16 | 32, count: number): Promise<{ size: number; url: string } | null> {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = () => {
      const c = document.createElement('canvas');
      c.width = c.height = size;
      const ctx = c.getContext('2d');
      if (!ctx) {
        resolve(null);
        return;
      }
      ctx.clearRect(0, 0, size, size);
      ctx.drawImage(img, 0, 0, size, size);
      resolve({ size, url: c.toDataURL('image/png') });
    };
    img.onerror = () => resolve(null);
    img.src = 'data:image/svg+xml,' + encodeURIComponent(svgFor(size, count));
  });
}

// Sequence counter: incremented on every setMailFavicon call. The DOM-write
// phase checks that it has not advanced while rasterization was in flight;
// stale results (seq !== _seq) are silently dropped so the most-recent call
// always wins (re #71).
let _seq = 0;

/**
 * Core implementation of setMailFavicon; accepts a rasterizer so unit tests
 * can inject a synchronous or controlled mock without needing a real canvas.
 */
async function setMailFaviconWith(count: number, rasterizeFn: RasterizeFn): Promise<void> {
  const safeCount = Math.max(0, Math.trunc(count));
  const seq = ++_seq;

  const results = await Promise.all([rasterizeFn(16, safeCount), rasterizeFn(32, safeCount)]);

  // A newer call started while we were rasterizing — our result is stale.
  if (seq !== _seq) return;

  const links = document.querySelectorAll("link[rel~='icon']");
  links.forEach((l) => l.parentNode?.removeChild(l));

  for (const r of results) {
    if (!r) continue;
    const link = document.createElement('link');
    link.rel = 'icon';
    link.type = 'image/png';
    link.sizes = `${r.size}x${r.size}`;
    link.href = r.url;
    document.head.appendChild(link);
  }
}

/**
 * Update the document favicon to show the given unread count as a badge.
 *
 * - count === 0: plain envelope, no badge
 * - count > 99: renders "99+"
 * - Replaces all existing link[rel~='icon'] tags with fresh PNG data-URL entries
 *   for 16px and 32px.
 * - Latest-wins: if called again before the previous rasterization completes,
 *   the stale result is discarded and only the most-recent count is applied.
 */
export async function setMailFavicon(count: number): Promise<void> {
  return setMailFaviconWith(count, rasterize);
}

/**
 * Internals exported only for unit tests. Not part of the public API.
 */
export const _internals_forTest = {
  setMailFaviconWith,
  resetSeq(): void {
    _seq = 0;
  },
};
