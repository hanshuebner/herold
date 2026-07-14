/**
 * Sanitiser tests — focus on the security-relevant rewrites: cid:
 * inline image resolution, external-image gating, and the inert-tag
 * filter. The full DOMPurify behaviour is exercised by upstream; we
 * test only the herold-specific layer.
 */
import { describe, it, expect } from 'vitest';
import { sanitizeHtml, htmlHasExternalImages } from './sanitize';
import { INTERNALIZE_PLACEHOLDER_PREFIX } from './internalize-placeholder';

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

describe('sanitizeHtml — inline image dimension preservation (issue #47)', () => {
  // ── HTML attribute preservation ────────────────────────────────────────────

  it('preserves existing width and height HTML attributes on cid: images (aspect-ratio reservation)', () => {
    // DOMPurify keeps width/height by default; rewriteImage must not strip them.
    const html = '<img width="600" height="200" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).toContain('width="600"');
    expect(body).toContain('height="200"');
    expect(body).toContain('src="/jmap/download/blob/foo.jpg"');
  });

  it('does not add width/height to a blocked cid: image (no cidMap entry)', () => {
    const html = '<img width="600" height="200" src="cid:missing">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: {},
    }));
    // Blocked images show the alt text, not a reserved-space placeholder.
    expect(body).toContain('data-herold-blocked="cid"');
    expect(body).not.toContain('src=');
  });

  // ── Style-dimension promotion ──────────────────────────────────────────────

  it('promotes style px dimensions to width/height attributes on cid: images when attrs are absent', () => {
    // Marketing HTML often carries dimensions only as inline style; converting
    // them to HTML attributes enables the browser implicit aspect-ratio.
    const html = '<img style="display:block;width:600px;height:200px;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).toContain('width="600"');
    expect(body).toContain('height="200"');
    expect(body).toContain('src="/jmap/download/blob/foo.jpg"');
  });

  it('promotes only the missing attribute when one is already present', () => {
    // If width is already an attribute, only height gets promoted from style.
    const html = '<img width="300" style="width:600px;height:400px;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    // Existing width="300" is preserved; height is promoted from style.
    expect(body).toContain('width="300"');
    expect(body).toContain('height="400"');
  });

  it('promotes only width when style has width but not height', () => {
    const html = '<img style="width:600px;display:block;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).toContain('width="600"');
    // No height in style → no height attribute added (cannot infer).
    expect(body).not.toMatch(/height="\d+"/);
  });

  it('does not promote non-px style dimensions (%, em, rem, vh)', () => {
    // Relative units cannot be converted to meaningful absolute attribute
    // values without knowing the containing block.
    const html = '<img style="width:100%;height:50vh;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).not.toMatch(/width="\d+"/);
    expect(body).not.toMatch(/height="\d+"/);
  });

  it('does not promote max-width as width (boundary guard)', () => {
    // The regex anchors at declaration boundaries so max-width does not
    // shadow width even if it appears first in the style string.
    const html = '<img style="max-width:100%;height:200px;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    // max-width:100% is not px → no width attribute added.
    expect(body).not.toMatch(/width="\d+"/);
    // height:200px is promoted.
    expect(body).toContain('height="200"');
  });

  it('rounds fractional px values to integers', () => {
    const html = '<img style="width:600.5px;height:200.3px;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).toContain('width="601"');
    expect(body).toContain('height="200"');
  });

  it('does not add width/height when style has no dimensions', () => {
    const html = '<img style="display:block;border:0;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).not.toMatch(/width="\d+"/);
    expect(body).not.toMatch(/height="\d+"/);
  });

  it('does not add width/height when no style attribute is present', () => {
    const html = '<img alt="logo" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).not.toMatch(/width="\d+"/);
    expect(body).not.toMatch(/height="\d+"/);
  });

  it('does not promote zero or negative px values', () => {
    const html = '<img style="width:0px;height:-100px;" src="cid:foo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { foo: '/jmap/download/blob/foo.jpg' },
    }));
    expect(body).not.toMatch(/width="\d+"/);
    expect(body).not.toMatch(/height="\d+"/);
  });
});

describe('sanitizeHtml — cid: image aspect-ratio from server-supplied dimensions (issue #47)', () => {
  it('injects aspect-ratio into the style when cidDimensions provides W and H', () => {
    // Afilio pattern: author sets width attribute and height:auto; server
    // supplies intrinsic pixel size so the browser can reserve the height.
    const html = '<img src="cid:img1" width="520" style="height: auto; max-width: 100%;">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: 1040, height: 1600 } },
    }));
    expect(body).toContain('aspect-ratio: 1040 / 1600');
    expect(body).toContain('src="/jmap/download/img1.png"');
  });

  it('preserves the author width attribute unchanged when aspect-ratio is injected', () => {
    const html = '<img src="cid:img1" width="520" style="height: auto;">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: 1040, height: 1600 } },
    }));
    // Author's display width must not be overwritten by the intrinsic width.
    expect(body).toContain('width="520"');
    expect(body).toContain('aspect-ratio: 1040 / 1600');
  });

  it('does not duplicate aspect-ratio when one is already present in the style', () => {
    // If the author (or a previous pass) already set an aspect-ratio, the
    // sanitiser must not add a second one.
    const html = '<img src="cid:img1" style="aspect-ratio: 1 / 2; width: 200px;">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: 400, height: 600 } },
    }));
    const occurrences = (body.match(/aspect-ratio/g) ?? []).length;
    expect(occurrences).toBe(1);
  });

  it('does not add aspect-ratio when the cid has no cidDimensions entry', () => {
    const html = '<img src="cid:img1">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: {}, // img1 absent from the dimensions map
    }));
    expect(body).not.toContain('aspect-ratio');
  });

  it('does not add aspect-ratio when cidDimensions is omitted entirely', () => {
    const html = '<img src="cid:img1">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      // no cidDimensions property
    }));
    expect(body).not.toContain('aspect-ratio');
  });

  it('security: NaN width is not injected into style', () => {
    // NaN is a valid JS number type; the runtime guard must reject it.
    const html = '<img src="cid:img1" style="display:block;">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: NaN, height: 600 } },
    }));
    expect(body).not.toContain('aspect-ratio');
  });

  it('security: Infinity width is not injected into style', () => {
    const html = '<img src="cid:img1" style="display:block;">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: Infinity, height: 600 } },
    }));
    expect(body).not.toContain('aspect-ratio');
  });

  it('security: non-positive width is not injected into style', () => {
    const html = '<img src="cid:img1" style="display:block;">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: -100, height: 600 } },
    }));
    expect(body).not.toContain('aspect-ratio');
  });

  it('appends aspect-ratio correctly when existing style ends without semicolon', () => {
    const html = '<img src="cid:img1" style="display:block">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: 800, height: 600 } },
    }));
    expect(body).toContain('aspect-ratio: 800 / 600');
  });

  it('injects aspect-ratio on an image with no pre-existing style attribute', () => {
    const html = '<img src="cid:img1" alt="logo">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: { img1: '/jmap/download/img1.png' },
      cidDimensions: { img1: { width: 200, height: 150 } },
    }));
    expect(body).toContain('aspect-ratio: 200 / 150');
  });

  it('does not add aspect-ratio to a blocked cid: image (no cidMap entry)', () => {
    const html = '<img src="cid:missing" width="520">';
    const body = bodyOf(sanitizeHtml(html, {
      loadImages: false,
      cidMap: {},
      cidDimensions: { missing: { width: 1040, height: 1600 } },
    }));
    // Blocked images must not carry layout-reservation styling.
    expect(body).not.toContain('aspect-ratio');
    expect(body).toContain('data-herold-blocked="cid"');
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

describe('sanitizeHtml — internalize placeholder pass-through (REQ-EXTIMG-BG-INTERNAL-40 / -65)', () => {
  it('passes the server-emitted placeholder data URI through unchanged', () => {
    const placeholder =
      'data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==';
    const html = `<img src="${placeholder}" alt="x">`;
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain(`src="${placeholder}"`);
    // No referrerpolicy / loading attrs are added: there is nothing
    // to fetch for an inline data URI.
    expect(body).not.toContain('referrerpolicy');
    expect(body).not.toContain('loading="lazy"');
    // The block-images banner machinery must not flag the placeholder
    // as a blocked external image.
    expect(body).not.toContain('data-herold-blocked');
  });

  it('passes the server-emitted placeholder through even when loadImages=true', () => {
    const placeholder =
      'data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==';
    const html = `<img src="${placeholder}" alt="x">`;
    const body = bodyOf(sanitizeHtml(html, { loadImages: true }));
    expect(body).toContain(`src="${placeholder}"`);
    // loadImages=true must NOT route the placeholder through /proxy/image —
    // the placeholder is local content, not an external resource to fetch.
    expect(body).not.toContain('/proxy/image');
  });
});

describe('sanitizeHtml — inline raster data: URIs', () => {
  it('passes a base64 PNG data URI through unchanged (issue: broken inline logos)', () => {
    const html = '<img src="data:image/png;base64,iVBORw0KGgo=" alt="logo">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('src="data:image/png;base64,iVBORw0KGgo="');
    expect(body).not.toContain('data-herold-blocked');
  });

  it('passes JPEG / GIF / WebP / AVIF data URIs through', () => {
    for (const mime of ['image/jpeg', 'image/gif', 'image/webp', 'image/avif']) {
      const html = `<img src="data:${mime};base64,AAAA" alt="x">`;
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).toContain(`src="data:${mime};base64,AAAA"`);
    }
  });

  it('treats data: URIs even when loadImages=false (they are inline, not external)', () => {
    const html = '<img src="data:image/png;base64,AAAA" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('src="data:image/png;base64,AAAA"');
    expect(body).not.toContain('data-herold-blocked');
  });

  it('does NOT proxy a data: URI when loadImages=true', () => {
    const html = '<img src="data:image/png;base64,AAAA" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: true }));
    expect(body).not.toContain('/proxy/image');
    expect(body).toContain('src="data:image/png;base64,AAAA"');
  });

  it('blocks data:image/svg+xml (SVG can contain scripts and external refs)', () => {
    const html = '<img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('data:image/svg');
    expect(body).not.toContain('src=');
  });

  it('blocks non-image data: URIs', () => {
    const html = '<img src="data:text/html;base64,PHNjcmlwdD4=" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('data:text/html');
    expect(body).not.toContain('src=');
  });

  it('blocks crafted boundary tricks like image/png-evil', () => {
    // Without the [;,] boundary anchor in the regex, "image/png-evil" could
    // match the "image/png" prefix; the boundary forces a real mediatype end.
    const html = '<img src="data:image/png-evil;base64,AAAA" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('data:image/png-evil');
    expect(body).not.toContain('src=');
  });

  it('accepts data: URIs with mediatype parameters before ;base64', () => {
    const html = '<img src="data:image/png;charset=utf-8;base64,AAAA" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('src="data:image/png;charset=utf-8;base64,AAAA"');
  });

  it('accepts URL-encoded (non-base64) data: URIs', () => {
    const html = '<img src="data:image/gif,GIF89a%01%00" alt="x">';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('src="data:image/gif,GIF89a%01%00"');
  });
});

describe('sanitizeHtml — internalize placeholder visible box (REQ-EXTIMG-BG-INTERNAL-41 / -66)', () => {
  it('iframe stylesheet contains the literal selector and a non-zero min-height when the message is still pending', () => {
    // Use a benign body; the assertion is on the wrapping <style>, not on the body.
    const out = sanitizeHtml('<p>hi</p>', { loadImages: false, internalizePending: true });
    expect(out).toContain(
      `img[src^="${INTERNALIZE_PLACEHOLDER_PREFIX}"]`,
    );
    // String-match the rule's min-height; explicitly assert the value
    // is not the CSS no-op `0`. Splitting the assertion into "selector
    // present" + "min-height is non-zero" guards against a future
    // refactor that drops the rule body but keeps the selector.
    const styleMatch = out.match(
      /img\[src\^="data:image\/gif;base64,R0lGODlhAQABAIAAAP"\][^}]*\{([^}]*)\}/,
    );
    expect(styleMatch).not.toBeNull();
    const ruleBody = styleMatch?.[1] ?? '';
    expect(ruleBody).toMatch(/min-height\s*:\s*([1-9][0-9.]*)\s*(em|px|rem|%)/);
  });

  it('omits the placeholder-sizing rule when the message is not pending (issue #160)', () => {
    // When internalizePending is absent (the common already-internalized
    // case) or explicitly false, the stylesheet must not carry the
    // prefix-match rule at all -- otherwise a genuine already-internalized
    // tracking pixel whose bytes happen to share the placeholder's prefix
    // gets forced into an oversized gray block instead of rendering at its
    // authored size.
    const outDefault = sanitizeHtml('<p>hi</p>', { loadImages: false });
    expect(outDefault).not.toContain(
      `img[src^="${INTERNALIZE_PLACEHOLDER_PREFIX}"]`,
    );
    const outExplicitFalse = sanitizeHtml('<p>hi</p>', {
      loadImages: false,
      internalizePending: false,
    });
    expect(outExplicitFalse).not.toContain(
      `img[src^="${INTERNALIZE_PLACEHOLDER_PREFIX}"]`,
    );
  });

  it('renders a genuine already-internalized tracking pixel at its authored size, not forced full-width (issue #160)', () => {
    // A minimal transparent-pixel GIF byte-for-byte identical to
    // extimg.PlaceholderDataURI's prefix, but here appearing in a message
    // whose internalize pass has already completed (internalizePending
    // false/absent). The nested-table ESP layout is the shape reported in
    // #160 (Greenhouse/Mailgun marketing email).
    const pixel =
      'data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==';
    const html = `
      <table><tr><td>
        <img src="${pixel}" width="1" height="1" alt="">
      </td></tr></table>
    `;
    const out = sanitizeHtml(html, { loadImages: false });
    // The image itself passes through (inline raster data URIs are always
    // allowed) with its authored 1x1 dimensions intact...
    expect(out).toContain(`src="${pixel}"`);
    expect(out).toContain('width="1" height="1"');
    // ...and the stylesheet carries no rule that would override that size.
    expect(out).not.toContain(
      `img[src^="${INTERNALIZE_PLACEHOLDER_PREFIX}"]`,
    );
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

describe('sanitizeHtml — linkify plain-text URLs (issue #103)', () => {
  it('wraps bare https URLs in anchors', () => {
    const body = bodyOf(sanitizeHtml('<p>see https://example.test/path for more</p>', { loadImages: false }));
    expect(body).toContain('<a href="https://example.test/path"');
    expect(body).toContain('target="_blank"');
    expect(body).toContain('rel="noopener noreferrer"');
    expect(body).toContain('>https://example.test/path</a>');
  });

  it('promotes bare www. URLs to https://', () => {
    const body = bodyOf(sanitizeHtml('<p>visit www.example.test/page</p>', { loadImages: false }));
    expect(body).toContain('<a href="https://www.example.test/page"');
    expect(body).toContain('>www.example.test/page</a>');
  });

  it('strips trailing sentence punctuation from the link', () => {
    const body = bodyOf(sanitizeHtml('<p>See https://example.test/path.</p>', { loadImages: false }));
    expect(body).toContain('<a href="https://example.test/path"');
    expect(body).toContain('>https://example.test/path</a>.');
  });

  it('keeps a closing paren that balances an opening one inside the URL', () => {
    const body = bodyOf(sanitizeHtml('<p>See https://en.wikipedia.org/wiki/Foo_(disambiguation) for more</p>', { loadImages: false }));
    expect(body).toContain('<a href="https://en.wikipedia.org/wiki/Foo_(disambiguation)"');
  });

  it('does not double-link a URL already inside an <a>', () => {
    const body = bodyOf(sanitizeHtml('<a href="https://x.test/">https://x.test/</a>', { loadImages: false }));
    expect((body.match(/<a /g) ?? []).length).toBe(1);
  });

  it('does not linkify inside <code> or <pre>', () => {
    const body = bodyOf(sanitizeHtml('<p><code>see https://example.test/code</code> <pre>https://example.test/pre</pre></p>', { loadImages: false }));
    expect(body).not.toContain('<a href="https://example.test/code"');
    expect(body).not.toContain('<a href="https://example.test/pre"');
  });

  it('linkifies mailto: but not bare addresses', () => {
    const body = bodyOf(sanitizeHtml('<p>contact mailto:hi@example.test or hi@example.test</p>', { loadImages: false }));
    expect(body).toContain('<a href="mailto:hi@example.test"');
    // The bare second address must not get its own anchor; the only <a>
    // in the body is the explicit mailto: above.
    expect((body.match(/<a /g) ?? []).length).toBe(1);
  });
});

describe('sanitizeHtml — quoted-history collapse', () => {
  it('wraps a top-level <blockquote> in <details>', () => {
    const html =
      '<p>My reply.</p>' +
      '<blockquote>Original message body.</blockquote>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('<details class="herold-quoted">');
    expect(body).toContain('<summary aria-label="Show trimmed content">');
    // The visual label is provided entirely by CSS ::before; the summary
    // element itself must be empty to avoid "···Show trimmed content" doubling.
    expect(body).not.toContain('>Show trimmed content<');
    expect(body).toContain('<blockquote>Original message body.</blockquote>');
    expect(body.indexOf('My reply.')).toBeLessThan(body.indexOf('<details'));
  });

  it('wraps gmail_quote class divs', () => {
    const html =
      '<p>My reply.</p>' +
      '<div class="gmail_quote_attribution">On Mon...</div>' +
      '<div class="gmail_quote">Original.</div>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('<details class="herold-quoted">');
    expect(body).toContain('Original.');
  });

  it('does not wrap when there is no quoted region', () => {
    const html = '<p>Just my reply.</p>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('<details');
  });

  it('bottom-posted fresh reply causes the quoted region to remain expanded (re #32, re #49)', () => {
    // Apple Mail bottom-posting: the user types their reply after the
    // quoted block. With issue #49 the fix goes further than #32: when
    // fresh content follows the blockquote the quote is not trailing and
    // must not be wrapped in <details> at all — leaving it fully expanded.
    const html =
      '<blockquote>' +
        '<p>On Mon Alice wrote: original message text</p>' +
      '</blockquote>' +
      '<div>' +
        '<p>My bottom-posted reply that must remain visible</p>' +
      '</div>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    // No <details> wrapping — the quoted region is not trailing.
    expect(body).not.toContain('<details');
    // Both the quoted history and the reply are directly visible.
    expect(body).toContain('On Mon Alice wrote');
    expect(body).toContain('My bottom-posted reply that must remain visible');
  });

  it('gmail attribution + quoted div are both swept into details (sweep not broken by re #32 fix)', () => {
    // Gmail: attribution div (class matching gmail_quote*) comes before the
    // gmail_quote div. Both must end up inside <details>; the fix must not
    // break this existing sweep behaviour.
    const html =
      '<p>My reply.</p>' +
      '<div class="gmail_quote_attribution">On Mon, Alice wrote:</div>' +
      '<div class="gmail_quote">Original quoted text</div>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    const detailsStart = body.indexOf('<details class="herold-quoted">');
    const detailsEnd = body.indexOf('</details>');
    expect(detailsStart).not.toBe(-1);
    // Attribution and quote content must both be inside <details>.
    const attrPos = body.indexOf('On Mon, Alice wrote:');
    const quotePos = body.indexOf('Original quoted text');
    expect(attrPos).toBeGreaterThan(detailsStart);
    expect(attrPos).toBeLessThan(detailsEnd);
    expect(quotePos).toBeGreaterThan(detailsStart);
    expect(quotePos).toBeLessThan(detailsEnd);
    // The fresh reply precedes <details>.
    expect(body.indexOf('My reply.')).toBeLessThan(detailsStart);
  });

  it('empty br-only siblings of the blockquote are swept into details', () => {
    // A trailing <br> after the blockquote is a separator, not fresh content.
    const html = '<blockquote><p>Quoted</p></blockquote><br>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    const detailsEnd = body.indexOf('</details>');
    // The <br> must appear before </details>.
    expect(body.indexOf('<br')).toBeLessThan(detailsEnd);
  });

  // issue #49 — trailing-only collapse rule
  it('#49 bottom-posted: <blockquote> followed by a fresh <p> is not collapsed', () => {
    const html =
      '<blockquote>Quoted history.</blockquote>' +
      '<p>Reply that follows the quote.</p>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('<details');
    expect(body).toContain('Quoted history.');
    expect(body).toContain('Reply that follows the quote.');
  });

  it('#49 interleaved: fresh content between two quoted blocks prevents any collapsing', () => {
    const html =
      '<p>First reply paragraph.</p>' +
      '<blockquote>First quoted block.</blockquote>' +
      '<p>Second reply paragraph.</p>' +
      '<blockquote>Second quoted block.</blockquote>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('<details');
    expect(body).toContain('First quoted block.');
    expect(body).toContain('Second quoted block.');
  });

  it('#49 quote-only body with no surrounding content collapses', () => {
    const html = '<blockquote>Only quoted text, no reply at all.</blockquote>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('<details class="herold-quoted">');
    expect(body).toContain('Only quoted text, no reply at all.');
  });

  it('#49 trailing whitespace-only text node after quote does not prevent collapsing', () => {
    // Whitespace text nodes are not fresh content — the region is still
    // trailing and the blockquote should be collapsed.
    const html = '<p>My reply.</p><blockquote>Original.</blockquote>   ';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('<details class="herold-quoted">');
  });

  it('#49 top-posted reply before a trailing quote still collapses the quote', () => {
    // Standard top-post: fresh reply is BEFORE the quote, not after.
    // The quote is trailing — collapse it.
    const html =
      '<p>My reply.</p>' +
      '<blockquote>Original message that was replied to.</blockquote>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('<details class="herold-quoted">');
    expect(body).toContain('Original message that was replied to.');
    expect(body.indexOf('My reply.')).toBeLessThan(body.indexOf('<details'));
  });

  // re #234: fold the citation-introducer paragraph and a trailing
  // signature-only tail into the collapsed region.
  describe('citation-introducer + trailing-signature folding (re #234)', () => {
    it('folds compose.svelte.ts\'s own "On {date}, {sender} wrote:" introducer into details', () => {
      // Exact shape emitted by compose.svelte.ts's formatReplyQuote: the
      // introducer <p> is the blockquote's immediate previous sibling.
      const html =
        '<p>My reply.</p>' +
        '<p>On Mon, 13 Jul 2026, Alice &lt;a@x.test&gt; wrote:</p>' +
        '<blockquote>Original message.</blockquote>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      const detailsEnd = body.indexOf('</details>');
      expect(detailsStart).not.toBe(-1);
      const introPos = body.indexOf('wrote:');
      expect(introPos).toBeGreaterThan(detailsStart);
      expect(introPos).toBeLessThan(detailsEnd);
      // The fresh reply stays outside and before <details>.
      expect(body.indexOf('My reply.')).toBeLessThan(detailsStart);
    });

    it('folds a German name-first "<Name> schrieb am ... um ...:" introducer', () => {
      const html =
        '<p>Danke!</p>' +
        '<p>Hans Hübner (Vorstandsvorsitzender VzEkC e.V.) schrieb am 13.07.26 um 16:53:</p>' +
        '<blockquote>Original.</blockquote>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      expect(detailsStart).not.toBe(-1);
      expect(body.indexOf('schrieb am 13.07.26')).toBeGreaterThan(detailsStart);
      expect(body.indexOf('Danke!')).toBeLessThan(detailsStart);
    });

    it('folds an "On ... wrote:" introducer separated from the blockquote by inter-tag whitespace (real mail clients, not compose.svelte.ts)', () => {
      // compose.svelte.ts's own formatReplyQuote concatenates tags with
      // zero whitespace between them ("<p>...</p><blockquote>"). Real
      // clients -- Thunderbird and essentially every other mail client --
      // serialize HTML with a newline (and often indentation) between
      // block-level tags, which the DOM parser turns into a whitespace-
      // only TEXT NODE sitting between the introducer <p> and the
      // <blockquote>. The introducer must still fold.
      const html =
        '<p>My reply.</p>\n' +
        '<p>On Mon, 13 Jul 2026, Florian Test &lt;florian@example.local&gt; wrote:</p>\n' +
        '  <blockquote>Original message.</blockquote>\n';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      const detailsEnd = body.indexOf('</details>');
      expect(detailsStart).not.toBe(-1);
      const introPos = body.indexOf('wrote:');
      expect(introPos).toBeGreaterThan(detailsStart);
      expect(introPos).toBeLessThan(detailsEnd);
      expect(body.indexOf('My reply.')).toBeLessThan(detailsStart);
    });

    it('folds a German name-first introducer separated from the blockquote by inter-tag whitespace', () => {
      // The actual reported message shape: Thunderbird's German locale
      // output, serialized with a newline between the introducer <p> and
      // the <blockquote>.
      const html =
        '<p>Danke für die Einladung.</p>\n' +
        '<p>Hans Hübner (Vorstandsvorsitzender VzEkC e.V.) schrieb am 13.07.26 um 16:53:</p>\n' +
        '<blockquote>Original.</blockquote>\n';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      const detailsEnd = body.indexOf('</details>');
      expect(detailsStart).not.toBe(-1);
      const introPos = body.indexOf('schrieb am 13.07.26');
      expect(introPos).toBeGreaterThan(detailsStart);
      expect(introPos).toBeLessThan(detailsEnd);
      expect(body.indexOf('Danke für die Einladung.')).toBeLessThan(detailsStart);
    });

    it('does not fold a genuine paragraph separated from the blockquote by inter-tag whitespace', () => {
      // Whitespace-skipping must not make the detector greedier: the
      // immediate non-empty previous sibling is still ordinary prose, not
      // an attribution line, even once the intervening whitespace text
      // node is skipped.
      const html =
        '<p>My reply.</p>\n' +
        '<p>Here is some context I want you to read first.</p>\n' +
        '<blockquote>Original message.</blockquote>\n';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      expect(detailsStart).not.toBe(-1);
      expect(body.indexOf('Here is some context')).toBeLessThan(detailsStart);
    });

    it('does not walk past a genuine paragraph to find an earlier attribution line', () => {
      // The backward skip must stop at the FIRST non-empty sibling and
      // test only that one -- it must not skip over real prose looking
      // for an attribution line further back.
      const html =
        '<p>On Mon, 13 Jul 2026, Alice &lt;a@x.test&gt; wrote:</p>\n' +
        '<p>Here is some unrelated context.</p>\n' +
        '<blockquote>Original message.</blockquote>\n';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      expect(detailsStart).not.toBe(-1);
      // Neither paragraph folds: the immediate previous sibling ("Here is
      // some unrelated context.") is not an attribution line, so the
      // backward sweep stops there without ever inspecting the earlier
      // "wrote:" line.
      expect(body.indexOf('Here is some unrelated context.')).toBeLessThan(detailsStart);
      expect(body.indexOf('wrote:')).toBeLessThan(detailsStart);
    });

    it('does not fold a genuine paragraph that merely precedes the quote', () => {
      // Heuristic discipline: ordinary text immediately before a blockquote
      // must stay outside the collapsed region -- only recognized
      // auto-generated introducer shapes fold.
      const html =
        '<p>My reply.</p>' +
        '<p>Here is some context I want you to read first.</p>' +
        '<blockquote>Original message.</blockquote>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      expect(detailsStart).not.toBe(-1);
      // The genuine paragraph stays outside <details>, before it.
      expect(body.indexOf('Here is some context')).toBeLessThan(detailsStart);
    });

    it('folds a trailing signature-only tail with the quote (compose.svelte.ts shape)', () => {
      // Exact shape emitted by compose.svelte.ts's appendSignature: two
      // empty <p> separators, then <p>-- </p>, then the signature paragraphs.
      const html =
        '<p>My reply.</p>' +
        '<blockquote>Original.</blockquote>' +
        '<p></p><p></p><p>-- </p><p>Alice</p><p>Vorstand VzEkC e.V.</p>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      const detailsEnd = body.indexOf('</details>');
      expect(detailsStart).not.toBe(-1);
      const sigPos = body.indexOf('Vorstand VzEkC e.V.');
      expect(sigPos).toBeGreaterThan(detailsStart);
      expect(sigPos).toBeLessThan(detailsEnd);
      expect(body.indexOf('My reply.')).toBeLessThan(detailsStart);
    });

    it('folds both the introducer and the trailing signature together (full Thunderbird-reply shape)', () => {
      const html =
        '<p>Super, bis dann!</p>' +
        '<p>On Mo., 13. Juli 2026, 16:55, Florian Stassen &lt;florian@classic-computing.de&gt; wrote:</p>' +
        '<blockquote>Original quoted text.</blockquote>' +
        '<p></p><p>-- </p><p>Alice</p>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      const detailsEnd = body.indexOf('</details>');
      expect(detailsStart).not.toBe(-1);
      expect(body.indexOf('wrote:')).toBeGreaterThan(detailsStart);
      expect(body.indexOf('wrote:')).toBeLessThan(detailsEnd);
      const sigPos = body.indexOf('>Alice<');
      expect(sigPos).toBeGreaterThan(detailsStart);
      expect(sigPos).toBeLessThan(detailsEnd);
      expect(body.indexOf('Super, bis dann!')).toBeLessThan(detailsStart);
    });

    it('does not collapse when real content follows the signature delimiter paragraph area unexpectedly', () => {
      // Guard regression: real fresh content (not a signature, not
      // quote-or-empty) after the blockquote still blocks the collapse
      // entirely, exactly as before re #234.
      const html =
        '<blockquote>Original.</blockquote>' +
        '<p>One more thing I forgot to mention.</p>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).not.toContain('<details');
      expect(body).toContain('One more thing I forgot to mention.');
    });
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

describe('sanitizeHtml — lone half of color/background-color pair (issue #231)', () => {
  it('strips a lone background-color so it does not collide with the iframe theme text color', () => {
    const html = '<span style="font-size:12.8px;background-color:rgb(26,17,17)">text</span>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('background-color');
    // The unrelated declaration is preserved. The style attribute is
    // reserialized through the CSS parser when a declaration is
    // stripped (see sanitizeInlineColorPairs), so assert on the value
    // rather than on exact original spacing.
    expect(body).toMatch(/font-size:\s*12\.8px/);
  });

  it('strips a lone color so it does not collide with the iframe theme background', () => {
    const html = '<span style="color:rgb(240,240,240)">text</span>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('color:rgb(240,240,240)');
  });

  it('keeps a fully-specified color/background-color pair intact', () => {
    const html = '<span style="color:#ffffff;background-color:#000000">text</span>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('color:#ffffff');
    expect(body).toContain('background-color:#000000');
  });

  it('removes the style attribute entirely when it held only the lone half', () => {
    const html = '<span style="background-color:rgb(26,17,17)">text</span>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('style=');
  });

  it('does not confuse background-color with color when only background-color is set', () => {
    // Regression guard: a naive substring check for "color:" would also
    // match "background-color:" and incorrectly report both halves present.
    const html = '<p style="background-color: rgb(26,17,17);">para</p>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).not.toContain('background-color');
  });

  it('leaves elements with no color declarations untouched', () => {
    const html = '<p style="font-weight:bold">text</p>';
    const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
    expect(body).toContain('font-weight:bold');
  });

  describe('the `background` shorthand', () => {
    it('strips a lone `background:` shorthand carrying only a color', () => {
      const html = '<span style="background:#1a1111">text</span>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).not.toContain('background');
      expect(body).not.toContain('style=');
    });

    it('strips a lone `background:` shorthand with !important', () => {
      const html = '<span style="background: #1a1111 !important">text</span>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).not.toContain('background');
    });

    it('keeps a `background:` shorthand carrying a color when `color` is also declared (complete pair)', () => {
      const html = '<span style="background:url(https://x.test/bg.png) #fff;color:#000">text</span>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).toContain('background:url(https://x.test/bg.png) #fff');
      expect(body).toContain('color:#000');
    });

    it('does not strip a `background:` shorthand that carries no color at all', () => {
      // background-image-only shorthand: no collision is possible because
      // no background color is declared, so the pair rule must not fire.
      const html = '<div style="background:url(https://x.test/bg.png) no-repeat">text</div>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).toContain('background:url(https://x.test/bg.png) no-repeat');
    });

    it('strips a lone `background:` shorthand mixing an image and a color when no `color` is declared', () => {
      const html = '<div style="background:url(https://x.test/bg.png) #1a1111 no-repeat">text</div>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      // The whole shorthand is dropped (documented trade-off), not just
      // the color token, so no residual color risk remains.
      expect(body).not.toContain('background');
    });

    it('recognizes a named-color background shorthand as a lone half', () => {
      const html = '<span style="background:navy">text</span>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      expect(body).not.toContain('background');
    });

    it('does not confuse `background-color:` or `background-image:` longhands with the `background` shorthand', () => {
      const html = '<span style="background-color:#1a1111">a</span>' +
        '<div style="background-image:url(https://x.test/bg.png)">b</div>';
      const body = bodyOf(sanitizeHtml(html, { loadImages: false }));
      // The background-color span: stripped (lone half).
      expect(body).not.toContain('background-color');
      // The background-image div: no color at all, left untouched.
      expect(body).toContain('background-image:url(https://x.test/bg.png)');
    });
  });
});

describe('sanitizeHtml — iframe body sizing (issue #158)', () => {
  it('establishes a block formatting context on <body> so a trailing element\'s default UA margin (e.g. a <p>) does not collapse through and disappear from body.scrollHeight, clipping the last line', () => {
    const out = sanitizeHtml('<p>Liebe Gruesse,<br>Hans</p>', { loadImages: false });
    // The template declares `html, body { margin: 0; padding: 0; }` first,
    // then a standalone `body { ... }` rule carrying the font/colour/layout
    // declarations -- match that second occurrence specifically.
    const bodyRules = out.match(/(?:^|\n)\s*body\s*\{[^}]*\}/g) ?? [];
    expect(bodyRules.some((rule) => /display:\s*flow-root/.test(rule))).toBe(true);
  });
});
