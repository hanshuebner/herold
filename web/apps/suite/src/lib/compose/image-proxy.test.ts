/**
 * Tests for the downscaled inline-image proxy generator (issue #243).
 *
 * happy-dom does not implement `createImageBitmap` or a real 2D canvas
 * context, so every test stubs both directly. The stubs model the shapes
 * these tests need (bitmap dimensions in, canvas draw calls recorded,
 * `toBlob` resolving with a typed Blob) without depending on an actual
 * image codec.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  generateImageProxy,
  PROXY_QUALITY,
  PROXY_TARGET_DIMENSION,
  PROXY_TRIGGER_DIMENSION,
} from './image-proxy';

function makeFile(name: string, type: string): File {
  return new File(['bytes'], name, { type });
}

function stubCreateImageBitmap(
  impl: (file: File) => Promise<{ width: number; height: number }>,
): ReturnType<typeof vi.fn> {
  const close = vi.fn();
  const fn = vi.fn(async (file: File) => {
    const { width, height } = await impl(file);
    return { width, height, close };
  });
  (globalThis as { createImageBitmap?: unknown }).createImageBitmap = fn;
  return fn;
}

/**
 * Stub `document.createElement('canvas')` with a fake canvas whose
 * `toBlob` resolves with a Blob carrying the requested mime type (or null,
 * for tests that exercise the webp-declines-then-jpeg-fallback path).
 * Non-canvas tags fall through to the real `document.createElement`.
 */
function stubCanvas(options: { webpSucceeds: boolean } = { webpSucceeds: true }) {
  const drawImage = vi.fn();
  const realCreateElement = document.createElement.bind(document);
  const fakeCanvas = {
    width: 0,
    height: 0,
    getContext: vi.fn(() => ({ drawImage })),
    toBlob: vi.fn(
      (cb: (blob: Blob | null) => void, type: string, quality: number) => {
        if (type === 'image/webp' && !options.webpSucceeds) {
          cb(null);
          return;
        }
        cb(new Blob(['proxy-bytes'], { type }));
      },
    ),
  };
  vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
    if (tag === 'canvas') return fakeCanvas as unknown as HTMLCanvasElement;
    return realCreateElement(tag);
  });
  return { drawImage, fakeCanvas };
}

describe('generateImageProxy', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete (globalThis as { createImageBitmap?: unknown }).createImageBitmap;
  });

  it('returns null for a non-image file', async () => {
    const bitmapFn = stubCreateImageBitmap(async () => ({ width: 5000, height: 5000 }));
    const result = await generateImageProxy(makeFile('doc.pdf', 'application/pdf'));
    expect(result).toBeNull();
    expect(bitmapFn).not.toHaveBeenCalled();
  });

  it('returns null for image/svg+xml without decoding (vector, no raster to downscale)', async () => {
    const bitmapFn = stubCreateImageBitmap(async () => ({ width: 5000, height: 5000 }));
    const result = await generateImageProxy(makeFile('icon.svg', 'image/svg+xml'));
    expect(result).toBeNull();
    expect(bitmapFn).not.toHaveBeenCalled();
  });

  it('returns null when the image is at or below the trigger threshold', async () => {
    stubCreateImageBitmap(async () => ({
      width: PROXY_TRIGGER_DIMENSION,
      height: 900,
    }));
    const result = await generateImageProxy(makeFile('photo.jpg', 'image/jpeg'));
    expect(result).toBeNull();
  });

  it('generates a downscaled webp proxy sized to the target dimension when the image exceeds the threshold', async () => {
    stubCreateImageBitmap(async () => ({ width: 4000, height: 3000 }));
    const { drawImage } = stubCanvas({ webpSucceeds: true });

    const result = await generateImageProxy(makeFile('photo.jpg', 'image/jpeg'));

    expect(result).not.toBeNull();
    // 4000x3000, longer side scaled to PROXY_TARGET_DIMENSION preserving aspect.
    const expectedWidth = PROXY_TARGET_DIMENSION;
    const expectedHeight = Math.round(3000 * (PROXY_TARGET_DIMENSION / 4000));
    expect(result?.width).toBe(expectedWidth);
    expect(result?.height).toBe(expectedHeight);
    expect(result?.blob.type).toBe('image/webp');
    expect(drawImage).toHaveBeenCalledWith(
      expect.anything(),
      0,
      0,
      expectedWidth,
      expectedHeight,
    );
  });

  it('closes the decoded bitmap after generating a proxy', async () => {
    const closeSpy = vi.fn();
    const fn = vi.fn(async () => ({ width: 4000, height: 2000, close: closeSpy }));
    (globalThis as { createImageBitmap?: unknown }).createImageBitmap = fn;
    stubCanvas();

    await generateImageProxy(makeFile('photo.jpg', 'image/jpeg'));
    expect(closeSpy).toHaveBeenCalledTimes(1);
  });

  it('falls back to jpeg when the webp encode declines', async () => {
    stubCreateImageBitmap(async () => ({ width: 5000, height: 5000 }));
    stubCanvas({ webpSucceeds: false });

    const result = await generateImageProxy(makeFile('photo.png', 'image/png'));

    expect(result).not.toBeNull();
    expect(result?.blob.type).toBe('image/jpeg');
  });

  it('returns null when createImageBitmap cannot decode the file', async () => {
    (globalThis as { createImageBitmap?: unknown }).createImageBitmap = vi.fn(
      async () => {
        throw new Error('unsupported image format');
      },
    );
    const result = await generateImageProxy(makeFile('weird.avif', 'image/avif'));
    expect(result).toBeNull();
  });

  it('returns null when createImageBitmap is unavailable in this environment', async () => {
    delete (globalThis as { createImageBitmap?: unknown }).createImageBitmap;
    const result = await generateImageProxy(makeFile('photo.jpg', 'image/jpeg'));
    expect(result).toBeNull();
  });

  it('quality constant stays within the valid canvas encode range', () => {
    expect(PROXY_QUALITY).toBeGreaterThan(0);
    expect(PROXY_QUALITY).toBeLessThanOrEqual(1);
  });
});
