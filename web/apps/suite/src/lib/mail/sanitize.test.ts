/**
 * Sanitiser tests — focus on the security-relevant rewrites: cid:
 * inline image resolution, external-image gating, and the inert-tag
 * filter. The full DOMPurify behaviour is exercised by upstream; we
 * test only the herold-specific layer.
 */
import { describe, it, expect } from 'vitest';
import { sanitizeHtml, htmlHasExternalImages } from './sanitize';

function bodyOf(srcdoc: string): string {
  const m = srcdoc.match(/<body>([\s\S]*?)<\/body>/);
  return m?.[1] ?? '';
}

describe('htmlHasExternalImages', () => {
  it('detects http(s) src', () => {
    expect(htmlHasExternalImages('<img src="https://x.test/a.png">')).toBe(true);
    expect(htmlHasExternalImages('<img src="http://x.test/a.png">')).toBe(true);
  });
  it('ignores cid: and data: src', () => {
    expect(htmlHasExternalImages('<img src="cid:foo">')).toBe(false);
    expect(htmlHasExternalImages('<img src="data:image/png;base64,abc">')).toBe(false);
  });
  it('returns false on empty input', () => {
    expect(htmlHasExternalImages('')).toBe(false);
  });
});

describe('sanitizeHtml — cid: image rewrite', () => {
  it('resolves cid via the provided map and sets referrerpolicy', () => {
    const html = '<p><img src="cid:foo123" alt="logo"></p>';
    const out = sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo123: '/jmap/download/abc' },
    });
    const body = bodyOf(out);
    expect(body).toContain('src="/jmap/download/abc"');
    expect(body).toContain('referrerpolicy="no-referrer"');
    expect(body).toContain('loading="lazy"');
  });

  it('blocks cid: when no map entry exists', () => {
    const html = '<p><img src="cid:missing" alt="logo"></p>';
    const out = sanitizeHtml(html, {
      loadImages: false,
      cidMap: {},
    });
    const body = bodyOf(out);
    expect(body).not.toContain('src=');
    expect(body).toContain('data-herold-blocked="cid"');
    expect(body).toContain('alt="logo"');
  });

  it('blocks cid: when no cidMap is provided at all', () => {
    const html = '<img src="cid:foo">';
    const out = sanitizeHtml(html, { loadImages: false });
    const body = bodyOf(out);
    expect(body).toContain('data-herold-blocked="cid"');
  });

  it('strips trailing whitespace from cid value', () => {
    const html = '<img src="cid:abc  " alt="x">';
    const out = sanitizeHtml(html, {
      loadImages: false,
      cidMap: { abc: '/jmap/download/xyz' },
    });
    expect(bodyOf(out)).toContain('src="/jmap/download/xyz"');
  });
});

describe('sanitizeHtml — external image gating', () => {
  it('removes src when loadImages=false', () => {
    const html = '<img src="https://x.test/a.png" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('src=');
    expect(body).toContain('data-herold-blocked="external"');
  });

  it('proxies src when loadImages=true', () => {
    const html = '<img src="https://x.test/a.png" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: true }));
    expect(body).toContain('src="/proxy/image?url=');
    expect(body).toContain(encodeURIComponent('https://x.test/a.png'));
  });

  it('drops non-http(s) image src outright', () => {
    const html = '<img src="javascript:alert(1)" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: true }));
    expect(body).not.toContain('javascript:');
  });
});

describe('sanitizeHtml — anchor rewrite', () => {
  it('forces target=_blank rel=noopener', () => {
    const html = '<a href="https://x.test/">click</a>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('target="_blank"');
    expect(body).toContain('rel="noopener noreferrer"');
  });
});

describe('sanitizeHtml — script/style filters', () => {
  it('drops <script>', () => {
    const html = '<p>hi</p><script>evil()</script>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('<script');
  });
  it('drops on* attributes', () => {
    const html = '<button onclick="evil()">x</button>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    // <button> is also stripped (FORBID_TAGS) but if it weren't, the
    // attribute would still go.
    expect(body).not.toContain('onclick');
  });
});
