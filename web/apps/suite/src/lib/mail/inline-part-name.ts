/**
 * Shared default filename for an unnamed inline body part (issue #409).
 *
 * The cid download URL builder and the inline-image download overlay both
 * need a stand-in filename for a part with no `name` header. Computing that
 * default in one place guarantees the two surfaces agree on the same
 * extension-bearing name — a divergence here is what let Chrome's
 * Content-Disposition preference save an extension-less file even though
 * the overlay itself named the download correctly.
 */

/** Fallback extension when a content type carries no subtype (e.g. `image`). */
const DEFAULT_EXTENSION = 'bin';

/**
 * Derive a file extension from a MIME type, e.g. `image/webp` -> `webp`.
 * Strips any parameters (`text/plain; charset=utf-8` -> `plain`).
 */
export function extensionFromType(type: string | null | undefined): string {
  const subtype = (type ?? '').split(';')[0]?.split('/')[1]?.trim();
  return subtype && subtype.length > 0 ? subtype : DEFAULT_EXTENSION;
}

/**
 * Default name for the Nth (1-based) unnamed inline part of the given
 * content type: `inline-N.<ext>`.
 */
export function defaultInlineName(type: string | null | undefined, idx: number): string {
  return `inline-${idx}.${extensionFromType(type)}`;
}

/** The subset of an EmailBodyPart this module needs, kept dependency-free. */
export interface InlinePartLike {
  cid: string | null;
  blobId: string | null;
  name: string | null;
  type: string;
}

/**
 * Compute the cid -> default-name map for every unnamed part carrying a
 * cid and blobId, in `parts` order. Both the cid download URL builder and
 * the download overlay in `MessageAccordion.svelte` consume this map so
 * they agree on the same extension-bearing name for the same part.
 */
export function buildCidDefaultNames(parts: InlinePartLike[]): Record<string, string> {
  const out: Record<string, string> = {};
  let idx = 0;
  for (const part of parts) {
    if (!part.cid || !part.blobId || part.name) continue;
    out[part.cid] = defaultInlineName(part.type, ++idx);
  }
  return out;
}
