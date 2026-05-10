/**
 * Server-emitted internalize placeholder constants — REQ-EXTIMG-BG-INTERNAL-40 / -41.
 *
 * The single source of truth for the prefix that the SPA's sanitiser allows
 * past its scheme allowlist (the renderer would otherwise strip every
 * data: URI) and that the iframe stylesheet uses to size the placeholder
 * to a visible gray box.
 *
 * The Go-side constant is `extimg.PlaceholderDataURI` in
 * `internal/extimg/placeholder.go`. Both sides must keep the prefix
 * literal in lock-step; the test in `sanitize.test.ts` (REQ-INTERNAL-65 /
 * REQ-INTERNAL-66) string-matches the selector against the served
 * stylesheet to guard against drift.
 *
 * The prefix is intentionally narrow (the literal start of the 1x1
 * transparent GIF base64 emitted by the server) so user-supplied bodies
 * cannot smuggle inline images past the external-fetch gate by claiming
 * to be "the placeholder".
 */
export const INTERNALIZE_PLACEHOLDER_PREFIX =
  'data:image/gif;base64,R0lGODlhAQABAIAAAP';

/**
 * Reports whether `src` is the server-emitted internalize placeholder
 * data URI (or a future variant that shares the literal prefix). Used
 * by the sanitiser to bypass the http(s)-only scheme allowlist for
 * this single inbound shape, and by the iframe stylesheet via a
 * matching `[src^="..."]` attribute selector.
 */
export function isInternalizePlaceholder(src: string): boolean {
  return src.startsWith(INTERNALIZE_PLACEHOLDER_PREFIX);
}
