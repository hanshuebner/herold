/**
 * Tests for the paste-image handling introduced for issue #242.
 *
 * Compose paste previously fell through to ProseMirror's default clipboard
 * pipeline, which prefers the `text/html` flavor over any accompanying raw
 * image bytes. A source like Google Photos puts both a `text/html` flavor
 * (an `<img src="https://photos.fife.usercontent.google.com/...">`) and the
 * raw image bytes on the clipboard at once; the foreign URL then produced a
 * broken image blocked by the browser's same-site policy.
 *
 * Covered here as pure-helper unit tests (the DOM/EditorView wiring itself
 * is exercised live in the browser — see the issue comment):
 *   - extractPastedImageFiles: raw image bytes win over any HTML flavor.
 *   - isAllowedPastedImageSrc / stripForeignImagesFromPastedHtml: a
 *     foreign-origin <img src> with no accompanying raw bytes is dropped
 *     rather than inserted as a broken image; herold-origin (blob:/cid:/
 *     data:/same-origin) images survive untouched.
 */
import { describe, it, expect } from 'vitest';
import {
  extractPastedImageFiles,
  isAllowedPastedImageSrc,
  stripForeignImagesFromPastedHtml,
} from './editor';

function makeImageFile(name = 'photo.png', type = 'image/png'): File {
  return new File([new Uint8Array([1, 2, 3, 4])], name, { type });
}

// ── extractPastedImageFiles ─────────────────────────────────────────────

describe('extractPastedImageFiles', () => {
  it('returns an empty array for a null DataTransfer', () => {
    expect(extractPastedImageFiles(null)).toEqual([]);
  });

  it('extracts an image file from DataTransfer.files (Google Photos raw bytes)', () => {
    const file = makeImageFile();
    const dt = {
      files: [file] as unknown as FileList,
      items: [] as unknown as DataTransferItemList,
    } as DataTransfer;
    const files = extractPastedImageFiles(dt);
    expect(files).toHaveLength(1);
    expect(files[0]).toBe(file);
  });

  it('ignores non-image files in DataTransfer.files', () => {
    const pdf = new File(['x'], 'doc.pdf', { type: 'application/pdf' });
    const dt = {
      files: [pdf] as unknown as FileList,
      items: [] as unknown as DataTransferItemList,
    } as DataTransfer;
    expect(extractPastedImageFiles(dt)).toEqual([]);
  });

  it('falls back to DataTransfer.items when files is empty (Safari-style clipboard)', () => {
    const file = makeImageFile('clip.png');
    const item = {
      kind: 'file',
      type: 'image/png',
      getAsFile: () => file,
    } as unknown as DataTransferItem;
    const dt = {
      files: [] as unknown as FileList,
      items: [item] as unknown as DataTransferItemList,
    } as DataTransfer;
    const files = extractPastedImageFiles(dt);
    expect(files).toHaveLength(1);
    expect(files[0]).toBe(file);
  });

  it('ignores non-file, non-image items', () => {
    const textItem = {
      kind: 'string',
      type: 'text/html',
      getAsFile: () => null,
    } as unknown as DataTransferItem;
    const dt = {
      files: [] as unknown as FileList,
      items: [textItem] as unknown as DataTransferItemList,
    } as DataTransfer;
    expect(extractPastedImageFiles(dt)).toEqual([]);
  });
});

// ── isAllowedPastedImageSrc ─────────────────────────────────────────────

describe('isAllowedPastedImageSrc', () => {
  it('rejects an empty src', () => {
    expect(isAllowedPastedImageSrc('')).toBe(false);
  });

  it('allows cid: references (the compose schema inline-image scheme)', () => {
    expect(isAllowedPastedImageSrc('cid:inl-1@herold.local')).toBe(true);
  });

  it('allows data: URIs (self-contained, no cross-site fetch)', () => {
    expect(isAllowedPastedImageSrc('data:image/png;base64,iVBORw0KGgo=')).toBe(true);
  });

  it('allows a same-origin blob: URL', () => {
    expect(isAllowedPastedImageSrc(`blob:${window.location.origin}/abc-123`)).toBe(true);
  });

  it('allows a same-origin path', () => {
    expect(isAllowedPastedImageSrc(`${window.location.origin}/api/blob/1`)).toBe(true);
  });

  it('rejects a foreign https: URL (the Google Photos case)', () => {
    expect(
      isAllowedPastedImageSrc(
        'https://photos.fife.usercontent.google.com/pw/AP1GczOesxT_tyJ8B2IWT1PB7eLMj9yxq801jsSQ6LBoa8Cs9OH4QQrkCgFolA=w2970-h2236-s-no-gm',
      ),
    ).toBe(false);
  });

});

// ── stripForeignImagesFromPastedHtml ────────────────────────────────────

describe('stripForeignImagesFromPastedHtml', () => {
  it('returns html unchanged when it has no <img at all', () => {
    const html = '<p>hello</p>';
    expect(stripForeignImagesFromPastedHtml(html)).toBe(html);
  });

  it('drops a foreign-origin <img> (Google Photos HTML flavor, no raw bytes)', () => {
    const html =
      '<p>look</p><img src="https://photos.fife.usercontent.google.com/pw/abc=w2970-h2236">';
    const out = stripForeignImagesFromPastedHtml(html);
    expect(out).not.toContain('<img');
    expect(out).not.toContain('photos.fife.usercontent.google.com');
    expect(out).toContain('<p>look</p>');
  });

  it('keeps a cid: image untouched', () => {
    const html = '<img src="cid:inl-1@herold.local" alt="x">';
    const out = stripForeignImagesFromPastedHtml(html);
    expect(out).toContain('cid:inl-1@herold.local');
  });

  it('keeps a same-origin blob: image untouched', () => {
    const html = `<img src="blob:${window.location.origin}/xyz">`;
    const out = stripForeignImagesFromPastedHtml(html);
    expect(out).toContain('blob:');
  });

  it('drops only the foreign image among a mix of allowed and foreign images', () => {
    const html =
      `<img src="cid:keep@herold.local"><img src="https://evil.example.com/x.png">`;
    const out = stripForeignImagesFromPastedHtml(html);
    expect(out).toContain('cid:keep@herold.local');
    expect(out).not.toContain('evil.example.com');
  });
});
