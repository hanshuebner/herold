/**
 * Tests for the extractDataUriImages pure helper (issue #50).
 *
 * The helper scans an HTML body for base64 data: image URIs embedded in
 * <img src="..."> attributes, decodes them into Uint8Array, assigns each a
 * Content-ID, and returns:
 *   - rewritten HTML with `cid:<cid>` references in place of the data: URIs
 *   - the list of DataUriImage descriptors ready for upload
 *
 * Invariants under test:
 *   - Distinct data: URIs each get their own cid.
 *   - Identical data: URIs (duplicate payloads) share a single cid/image.
 *   - blob:, cid:, and http: src attributes are left untouched.
 *   - An HTML body with no data: URIs is returned unchanged (same reference).
 *   - Malformed / non-base64 data: URIs are left in the HTML without error.
 *   - The decoded bytes match the original base64 payload exactly.
 *   - Synthesised filenames include the correct extension.
 */
import { describe, it, expect } from 'vitest';
import { extractDataUriImages } from './compose.svelte';

// A minimal 1-pixel PNG encoded in base64. Small enough to keep the test
// file readable; genuine binary content so the byte-decode path is exercised.
const PNG_1PX =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg==';

// A minimal 1-pixel GIF (GIF87a, single colour).
const GIF_1PX = 'R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';

function makeDataUri(b64: string, subtype = 'png'): string {
  return `data:image/${subtype};base64,${b64}`;
}

// ── no data: URIs ─────────────────────────────────────────────────────────────

describe('extractDataUriImages — no data: URIs', () => {
  it('returns the original html string unchanged when there are no images', () => {
    const html = '<p>hello world</p>';
    const { html: out, images } = extractDataUriImages(html);
    expect(out).toBe(html);
    expect(images).toHaveLength(0);
  });

  it('leaves blob: src attributes untouched', () => {
    const html = '<img src="blob:https://app.test/abc-123" alt="x">';
    const { html: out, images } = extractDataUriImages(html);
    expect(out).toBe(html);
    expect(images).toHaveLength(0);
  });

  it('leaves cid: src attributes untouched', () => {
    const html = '<img src="cid:inl-1@herold.local" alt="x">';
    const { html: out, images } = extractDataUriImages(html);
    expect(out).toBe(html);
    expect(images).toHaveLength(0);
  });

  it('leaves http: src attributes untouched', () => {
    const html = '<img src="https://example.com/photo.jpg" alt="x">';
    const { html: out, images } = extractDataUriImages(html);
    expect(out).toBe(html);
    expect(images).toHaveLength(0);
  });

  it('returns empty images and unmodified html for an empty string', () => {
    const { html: out, images } = extractDataUriImages('');
    expect(out).toBe('');
    expect(images).toHaveLength(0);
  });
});

// ── single data: image ────────────────────────────────────────────────────────

describe('extractDataUriImages — single data: image', () => {
  it('extracts one PNG and rewrites src to cid:', () => {
    const dataUri = makeDataUri(PNG_1PX);
    const html = `<p>body</p><img src="${dataUri}" alt="x">`;
    const { html: out, images } = extractDataUriImages(html);

    expect(images).toHaveLength(1);
    const img = images[0]!;

    // HTML must contain cid: reference for the generated CID.
    expect(out).toContain(`src="cid:${img.cid}"`);
    // Original data URI must not appear in the output.
    expect(out).not.toContain('data:image/');
    // Mime type and name.
    expect(img.mime).toBe('image/png');
    expect(img.name).toMatch(/inline-image-\d+\.png$/);
  });

  it('correctly decodes the base64 payload to bytes', () => {
    const dataUri = makeDataUri(PNG_1PX);
    const html = `<img src="${dataUri}">`;
    const { images } = extractDataUriImages(html);

    const img = images[0]!;
    // Decode the reference base64 independently and compare.
    const raw = atob(PNG_1PX);
    const expected = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) expected[i] = raw.charCodeAt(i);

    expect(img.bytes).toEqual(expected);
    expect(img.size).toBe(expected.length);
  });

  it('works with single-quoted src attributes', () => {
    const dataUri = makeDataUri(PNG_1PX);
    const html = `<img src='${dataUri}' alt="x">`;
    const { html: out, images } = extractDataUriImages(html);

    expect(images).toHaveLength(1);
    expect(out).toContain(`src='cid:`);
    expect(out).not.toContain('data:image/');
  });

  it('extracts a GIF and uses the correct extension', () => {
    const dataUri = makeDataUri(GIF_1PX, 'gif');
    const html = `<img src="${dataUri}">`;
    const { images } = extractDataUriImages(html);

    expect(images).toHaveLength(1);
    expect(images[0]!.mime).toBe('image/gif');
    expect(images[0]!.name).toMatch(/\.gif$/);
  });

  it('uses the jpeg -> jpg extension mapping', () => {
    // Build a tiny valid-ish JPEG base64 (not structurally valid but base64 decodes fine).
    const fakeJpeg = btoa('\xff\xd8\xff\xd9');
    const dataUri = makeDataUri(fakeJpeg, 'jpeg');
    const html = `<img src="${dataUri}">`;
    const { images } = extractDataUriImages(html);

    expect(images).toHaveLength(1);
    expect(images[0]!.name).toMatch(/\.jpg$/);
  });
});

// ── multiple data: images ─────────────────────────────────────────────────────

describe('extractDataUriImages — multiple data: images', () => {
  it('extracts two distinct images and assigns distinct cids', () => {
    const uri1 = makeDataUri(PNG_1PX, 'png');
    const uri2 = makeDataUri(GIF_1PX, 'gif');
    const html = `<p><img src="${uri1}"></p><p><img src="${uri2}"></p>`;
    const { html: out, images } = extractDataUriImages(html);

    expect(images).toHaveLength(2);
    const cid1 = images[0]!.cid;
    const cid2 = images[1]!.cid;
    expect(cid1).not.toBe(cid2);

    expect(out).toContain(`src="cid:${cid1}"`);
    expect(out).toContain(`src="cid:${cid2}"`);
    expect(out).not.toContain('data:image/');
  });
});

// ── duplicate data: images (deduplication) ────────────────────────────────────

describe('extractDataUriImages — duplicate payloads deduplicated', () => {
  it('reuses the same cid for two identical data: URIs', () => {
    const dataUri = makeDataUri(PNG_1PX);
    const html = `<img src="${dataUri}"><img src="${dataUri}">`;
    const { html: out, images } = extractDataUriImages(html);

    // Only one image descriptor for the one unique payload.
    expect(images).toHaveLength(1);
    const { cid } = images[0]!;

    // Both img tags must reference the same cid.
    const matches = Array.from(out.matchAll(new RegExp(`src="cid:${cid.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}"`, 'g')));
    expect(matches).toHaveLength(2);
  });

  it('does not deduplicate different payloads', () => {
    const uri1 = makeDataUri(PNG_1PX, 'png');
    const uri2 = makeDataUri(GIF_1PX, 'gif');
    const html = `<img src="${uri1}"><img src="${uri2}">`;
    const { images } = extractDataUriImages(html);

    expect(images).toHaveLength(2);
    expect(images[0]!.cid).not.toBe(images[1]!.cid);
  });
});

// ── malformed / non-base64 data: URIs ─────────────────────────────────────────

describe('extractDataUriImages — graceful handling of malformed input', () => {
  it('leaves a non-base64 (url-encoded) data: URI untouched', () => {
    // A non-base64 data URI — no ;base64 segment — should not match the regex.
    const html = '<img src="data:image/png,%89PNG%0D%0A" alt="x">';
    const { html: out, images } = extractDataUriImages(html);
    expect(images).toHaveLength(0);
    expect(out).toBe(html);
  });

  it('leaves a data: URI with invalid base64 payload untouched', () => {
    // The regex matches the ;base64 sentinel, but atob throws on "!!!not-base64!!!".
    const html = '<img src="data:image/png;base64,!!!not-base64!!!">';
    const { html: out, images } = extractDataUriImages(html);
    // The regex pattern requires [A-Za-z0-9+/=\s]+ so this will not match
    // because "!" is not in the character class — the tag is left unchanged.
    expect(images).toHaveLength(0);
    expect(out).toBe(html);
  });

  it('does not extract non-image data: URIs (e.g. application/pdf)', () => {
    const b64 = btoa('pdfcontent');
    const html = `<a href="data:application/pdf;base64,${b64}">link</a>`;
    const { html: out, images } = extractDataUriImages(html);
    // The regex only targets src= attributes with data:image/ — not href or other types.
    expect(images).toHaveLength(0);
    expect(out).toBe(html);
  });
});

// ── DataUriImage fields ───────────────────────────────────────────────────────

describe('extractDataUriImages — DataUriImage descriptor fields', () => {
  it('includes the original dataUri on the descriptor for fallback use', () => {
    const dataUri = makeDataUri(PNG_1PX);
    const html = `<img src="${dataUri}">`;
    const { images } = extractDataUriImages(html);

    expect(images[0]!.dataUri).toBe(dataUri);
  });

  it('generates a cid that is RFC 2392-safe (no spaces or special chars)', () => {
    const dataUri = makeDataUri(PNG_1PX);
    const html = `<img src="${dataUri}">`;
    const { images } = extractDataUriImages(html);

    const cid = images[0]!.cid;
    // RFC 2392: cid URLs are <word "@" domain> — only alphanumerics and a few
    // safe chars. The generated CIDs use alphanumeric + hyphens + @.
    expect(cid).toMatch(/^[a-z0-9@.\-]+$/);
  });
});
