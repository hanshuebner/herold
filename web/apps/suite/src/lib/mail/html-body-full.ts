/**
 * Helpers for the truncated-HTML-body recovery path (Forgejo #48).
 *
 * Email/get caps body values at mailparse.DefaultMaxTextPartBytes (1 MiB)
 * and signals the cut with EmailBodyValue.isTruncated = true. When
 * truncated, the full decoded part is fetchable via the JMAP download URL
 * for the html body part's blobId — the blob endpoint has no size cap on
 * blob downloads, only on inline bodyValues.
 */

import type { Email } from './types';

/**
 * The server's internal parser caps Part.Text at this byte count
 * (mailparse.DefaultMaxTextPartBytes). Inline body values for parts
 * larger than this are truncated even when the JMAP response carries
 * isTruncated: false (the server only sets that flag when the client
 * explicitly sends maxBodyValueBytes). Part.size always reflects the
 * full decoded byte count, so comparing it against this constant
 * reliably detects the parser-level truncation.
 */
const DEFAULT_SERVER_CAP = 1_048_576; // 1 MiB

/**
 * Returns true when the email's first html body part value is present
 * and was truncated by the server.
 *
 * Two signals are checked in order:
 *  1. bodyValue.isTruncated — the RFC 8621 standard flag, set when the
 *     client explicitly provided maxBodyValueBytes.
 *  2. part.size > DEFAULT_SERVER_CAP — the parser applies a 1 MiB hard
 *     cap internally and may not propagate that to isTruncated. If the
 *     full decoded part size exceeds the cap the inline value is
 *     incomplete.
 */
export function htmlBodyIsTruncated(email: Email): boolean {
  const part = email.htmlBody?.[0];
  if (!part?.partId) return false;
  const bodyValue = email.bodyValues?.[part.partId];
  if (!bodyValue) return false;
  if (bodyValue.isTruncated) return true;
  if (part.size > DEFAULT_SERVER_CAP) return true;
  return false;
}

/**
 * Builds the JMAP download URL for the truncated html body part.
 * Returns null when the body is not truncated, the part has no blobId,
 * or the download URL factory returns null (session not yet bootstrapped).
 *
 * The `downloadUrl` parameter accepts the JmapClient.downloadUrl
 * signature so callers can pass `(args) => jmap.downloadUrl(args)` and
 * this function remains pure and independently testable.
 */
export function htmlBodyFullDownloadUrl(
  email: Email,
  accountId: string,
  downloadUrl: (args: {
    accountId: string;
    blobId: string;
    type: string;
    name: string;
  }) => string | null,
): string | null {
  if (!htmlBodyIsTruncated(email)) return null;
  const part = email.htmlBody?.[0];
  if (!part?.blobId) return null;
  return downloadUrl({
    accountId,
    blobId: part.blobId,
    type: 'text/html',
    name: 'body.html',
  });
}

/**
 * Fetches the full (un-truncated) HTML body text from the JMAP download
 * endpoint. Throws on HTTP error so the caller decides the fallback.
 *
 * Same-origin fetch with session cookies; the suite never sends
 * unauthenticated requests.
 */
export async function fetchFullHtmlBody(url: string): Promise<string> {
  const res = await fetch(url, { credentials: 'include' });
  if (!res.ok) {
    throw new Error(`fetchFullHtmlBody: HTTP ${res.status}`);
  }
  return res.text();
}
