/**
 * Client-side zip utility for "Download all" (REQ-ATT-40, REQ-ATT-41).
 *
 * Fetches each blob URL using the session cookie (credentials: 'include')
 * and streams them into a zip archive via fflate. The resulting archive
 * is saved with the caller-supplied filename.
 *
 * Each entry is described by a `zipPath` (the path inside the zip) and
 * a `url` (the JMAP download URL resolved against the session). Inline
 * images must already carry the `inline/` prefix in their zipPath so the
 * user can distinguish them from regular attachments (REQ-ATT-40).
 */

import { strToU8, zipSync } from 'fflate';

export interface ZipEntry {
  /** Resolved JMAP download URL for the blob. */
  url: string;
  /** Path inside the zip archive, e.g. "report.pdf" or "inline/photo.jpg". */
  zipPath: string;
}

/**
 * Fetch all blobs in parallel, zip them, and trigger a browser download.
 * Throws if any fetch fails so the caller can surface an error.
 */
export async function zipBlobsAsDownload(
  entries: ZipEntry[],
  filename: string,
): Promise<void> {
  if (entries.length === 0) return;

  // Fetch all blobs in parallel. Credentials must be included so the
  // session cookie attaches for the same-origin JMAP download path.
  const fetched = await Promise.all(
    entries.map(async (entry) => {
      const res = await fetch(entry.url, { credentials: 'include' });
      if (!res.ok) {
        throw new Error(`Download failed for ${entry.zipPath}: HTTP ${res.status}`);
      }
      const buf = await res.arrayBuffer();
      return { zipPath: entry.zipPath, data: new Uint8Array(buf) };
    }),
  );

  // Build the zip. fflate.zipSync expects a Record<path, [Uint8Array, options]>.
  const files: Parameters<typeof zipSync>[0] = {};
  for (const { zipPath, data } of fetched) {
    // Deduplicate paths in case the server returned duplicate filenames
    // (unlikely but possible with no-name attachments).
    let path = zipPath;
    let counter = 1;
    while (Object.prototype.hasOwnProperty.call(files, path)) {
      const ext = zipPath.lastIndexOf('.');
      if (ext > -1) {
        path = `${zipPath.slice(0, ext)}-${counter}${zipPath.slice(ext)}`;
      } else {
        path = `${zipPath}-${counter}`;
      }
      counter++;
    }
    files[path] = [data, { level: 0 }]; // level:0 = store, no compression
  }

  const zipped = zipSync(files);
  // Cast through ArrayBuffer to satisfy strict TypeScript lib checks for
  // Blob constructor (Uint8Array<ArrayBufferLike> vs ArrayBufferView<ArrayBuffer>).
  const blob = new Blob([zipped.buffer as ArrayBuffer], { type: 'application/zip' });
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  } finally {
    // Revoke on the next tick so the browser has time to start the download.
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  }
}

// Re-export for tests that need access to the path-dedup logic without
// going through the full download flow.
export { strToU8 };
