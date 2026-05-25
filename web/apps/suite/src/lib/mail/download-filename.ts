/**
 * Sanitise an Email subject into a safe filename for the "Download .eml"
 * kebab action (REQ-MAIL-141). The output is the suggested name passed to
 * the browser's `<a download>` attribute, NOT a server path — the browser
 * sanitises further per platform, but we hand it a clean candidate.
 *
 * Rules:
 *   - Strip path-separator and Windows-forbidden chars, plus control
 *     bytes (`/ \ : * ? " < > |` + `\x00..\x1f`). Replaced with `_`.
 *   - Collapse runs of whitespace to a single space; trim.
 *   - Truncate to 80 characters BEFORE appending the extension.
 *   - When the trimmed subject is empty, fall back to
 *     `message-<short>.eml` where `<short>` is the first 8 characters of
 *     the blob id (or the email id if the blob id is absent).
 *   - Always end in `.eml`.
 */
export function emlDownloadFilename(args: {
  subject: string | null | undefined;
  blobId?: string | null;
  emailId: string;
}): string {
  const cleaned = sanitiseSubject(args.subject);
  // Treat a subject that reduces to only underscores / whitespace as empty
  // (e.g. ":: <<>>") — fall back to the blob-id stub so we don't hand the
  // user a file called "__ ____.eml".
  if (cleaned && cleaned.replace(/[_\s]/g, '').length > 0) {
    return `${cleaned}.eml`;
  }
  const id = (args.blobId ?? args.emailId).slice(0, 8);
  return `message-${id || 'unknown'}.eml`;
}

function sanitiseSubject(raw: string | null | undefined): string {
  if (!raw) return '';
  // Replace path-separator and Windows-forbidden chars, plus control
  // bytes, with underscore. Spaces and hyphens are fine in filenames.
  // eslint-disable-next-line no-control-regex
  const safe = raw.replace(/[\\/:*?"<>|\x00-\x1f]/g, '_');
  // Collapse internal whitespace to a single space, trim ends.
  const collapsed = safe.replace(/\s+/g, ' ').trim();
  if (!collapsed) return '';
  // Truncate to 80 chars.
  return collapsed.length > 80 ? collapsed.slice(0, 80).trimEnd() : collapsed;
}
