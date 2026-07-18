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
 * Finds the htmlBody part (RFC 8621 4.1.4's ordered list) whose inline
 * bodyValue was truncated by the server, if any. `htmlBody` usually
 * carries a single entry; when a multipart/mixed message contributes
 * more than one leaf to `htmlBody` (e.g. a forwarder's own note ahead of
 * the forwarded original, re #258), the note is small and never the one
 * that hits the 1 MiB parser cap, so scanning every entry rather than
 * assuming index 0 is what keeps this detection correct for both shapes.
 * At most one part is expected to be truncated in practice; the first
 * match wins.
 *
 * Two signals are checked in order:
 *  1. bodyValue.isTruncated — the RFC 8621 standard flag, set when the
 *     client explicitly provided maxBodyValueBytes.
 *  2. part.size > DEFAULT_SERVER_CAP — the parser applies a 1 MiB hard
 *     cap internally and may not propagate that to isTruncated. If the
 *     full decoded part size exceeds the cap the inline value is
 *     incomplete.
 */
function findTruncatedHtmlPart(email: Email) {
  for (const part of email.htmlBody ?? []) {
    if (!part.partId) continue;
    const bodyValue = email.bodyValues?.[part.partId];
    if (!bodyValue) continue;
    if (bodyValue.isTruncated || part.size > DEFAULT_SERVER_CAP) return part;
  }
  return null;
}

/**
 * Returns true when any of the email's htmlBody parts has an inline
 * value that was truncated by the server. See `findTruncatedHtmlPart`
 * for the detection rule.
 */
export function htmlBodyIsTruncated(email: Email): boolean {
  return findTruncatedHtmlPart(email) !== null;
}

/**
 * The partId of the truncated htmlBody part, if any. Used by
 * `MessageAccordion` to target the full-body fetch's override at the
 * correct entry in `emailHtmlBody`'s multi-part rendering (re #258),
 * leaving every other htmlBody part's inline value untouched.
 */
export function truncatedHtmlBodyPartId(email: Email): string | null {
  return findTruncatedHtmlPart(email)?.partId ?? null;
}

/**
 * Builds the JMAP download URL for the truncated html body part.
 * Returns null when no part is truncated, the truncated part has no
 * blobId, or the download URL factory returns null (session not yet
 * bootstrapped).
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
  const part = findTruncatedHtmlPart(email);
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
