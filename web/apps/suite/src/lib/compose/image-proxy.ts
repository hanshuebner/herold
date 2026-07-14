/**
 * Downscaled in-editor proxy generation for large inline images (issue #243).
 *
 * Pasting, drag-dropping, or picking a large image inserts the original
 * File's full-resolution bitmap as the ProseMirror `<img src>`. The DOM then
 * has to decode and repaint that full-resolution bitmap on every keystroke
 * near it, which is what makes the editor feel unresponsive while a large
 * image sits in the document.
 *
 * `generateImageProxy` renders a downscaled copy for in-editor display only.
 * The full-resolution `File` is untouched by this module: compose.svelte.ts
 * uploads that original File (via `uploadInlineImage`) as the message's
 * inline part, entirely independent of whatever the editor currently shows.
 */

/** Images whose longer side exceeds this are downscaled for editor display. */
export const PROXY_TRIGGER_DIMENSION = 1600;

/** Longer side of the generated proxy, in pixels. */
export const PROXY_TARGET_DIMENSION = 1024;

/** `canvas.toBlob` encode quality for the proxy (0..1). */
export const PROXY_QUALITY = 0.82;

export interface ImageProxy {
  blob: Blob;
  width: number;
  height: number;
}

/**
 * Returns a downscaled proxy Blob for `file` plus its pixel dimensions, or
 * null when no proxy is needed or one could not be generated. Both cases
 * mean the caller should keep using the original file's object URL for
 * display. Skips vector images (`image/svg+xml` has no natural raster
 * resolution to downscale) and anything `createImageBitmap` can't decode.
 */
export async function generateImageProxy(file: File): Promise<ImageProxy | null> {
  if (!file.type.startsWith('image/') || file.type === 'image/svg+xml') return null;
  if (typeof createImageBitmap !== 'function') return null;

  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    // Format createImageBitmap can't decode — keep the original as-is.
    return null;
  }

  try {
    const longSide = Math.max(bitmap.width, bitmap.height);
    if (longSide <= PROXY_TRIGGER_DIMENSION) return null;

    const scale = PROXY_TARGET_DIMENSION / longSide;
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));

    const canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext('2d');
    if (!ctx) return null;
    ctx.drawImage(bitmap, 0, 0, width, height);

    const blob = await canvasToBlob(canvas);
    if (!blob) return null;
    return { blob, width, height };
  } finally {
    bitmap.close?.();
  }
}

/**
 * Encode a canvas to a Blob, preferring WebP (smaller at equivalent
 * quality) and falling back to JPEG for browsers whose `toBlob` declines
 * (or silently no-ops on) the WebP mime type.
 */
function canvasToBlob(canvas: HTMLCanvasElement): Promise<Blob | null> {
  return new Promise((resolve) => {
    canvas.toBlob(
      (blob) => {
        if (blob) {
          resolve(blob);
          return;
        }
        canvas.toBlob(
          (jpegBlob) => resolve(jpegBlob),
          'image/jpeg',
          PROXY_QUALITY,
        );
      },
      'image/webp',
      PROXY_QUALITY,
    );
  });
}
