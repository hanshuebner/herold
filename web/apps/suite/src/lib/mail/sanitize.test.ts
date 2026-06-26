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
  it('iframe stylesheet contains the literal selector and a non-zero min-height', () => {
    // Use a benign body; the assertion is on the wrapping <style>, not on the body.
    const out = sanitizeHtml('<p>hi</p>', { loadImages: false });
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
