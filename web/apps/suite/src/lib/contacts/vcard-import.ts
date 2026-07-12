/**
 * Pure helpers for the vCard import / export JMAP driver.
 *
 * Types for Contact/import and Contact/export, plus pure functions for
 * building request args and categorising response results.
 *
 * REQ-CONT-80..83 (docs/design/web/requirements/27-contacts.md).
 */

/**
 * One field-level difference between an imported card and the existing
 * contact it matched (ImportCardResult.result === 'conflict').
 */
export interface ImportFieldDiff {
  /** JSContact top-level property name that differs (e.g. "name", "emails"). */
  field: string;
  /** Current value on the stored contact, rendered as compact JSON. */
  existing?: string;
  /** Value the imported card would set, rendered as compact JSON. */
  incoming?: string;
}

/** Per-card outcome from Contact/import. */
export interface ImportCardResult {
  /** Zero-based position of the card in the uploaded .vcf. */
  index: number;
  uid?: string;
  /**
   * - 'created': no existing contact matched; a new contact was created.
   * - 'skipped': the card exactly matches an existing contact (matched by
   *   uid, else primary email); no new contact was created.
   * - 'conflict': the card matches an existing contact but its data
   *   differs; no new contact was created and the existing contact was
   *   not overwritten.
   * - 'failed': the card could not be parsed or created.
   */
  result: 'created' | 'skipped' | 'conflict' | 'failed';
  /** JMAP contact id, present when result === 'created'. */
  id?: string;
  /**
   * JMAP id of the pre-existing contact this card was matched against
   * (result === 'skipped' or 'conflict').
   */
  matchedId?: string;
  /**
   * Matched contact's display name (result === 'skipped' or 'conflict'),
   * so the UI can identify the row by name rather than card index.
   */
  matchedName?: string;
  /** Field-level differences vs. the matched contact (result === 'conflict'). */
  diff?: ImportFieldDiff[];
  /** Server-supplied reason, present when result === 'failed'. */
  reason?: string;
  /**
   * Advisory: existing contact ids that look like duplicates of this card
   * (matched by uid or primary email), reported only when the match was
   * ambiguous (zero or more than one candidate) and the card was
   * therefore created rather than skipped/conflicted.
   */
  duplicateCandidates?: string[];
}

/** Full Contact/import response args. */
export interface ImportResponse {
  accountId: string;
  newState: string;
  results: ImportCardResult[];
}

/** Advisory: a property that could not be represented in the exported vCard. */
export interface UnrepresentableProperty {
  contactId: string;
  type: string;
  detail: string;
}

/** Full Contact/export response args. */
export interface ExportResponse {
  accountId: string;
  blobId: string;
  type: string;
  size: number;
  unrepresentable?: UnrepresentableProperty[];
}

/** Request args for Contact/import. */
export interface ImportArgs {
  accountId: string;
  blobId: string;
  addressBookId?: string;
}

/** Request args for Contact/export. */
export interface ExportArgs {
  accountId: string;
  ids?: string[];
  addressBookId?: string;
  fetchPhotos?: boolean;
}

/** Build the Contact/import request args from the blob upload result. */
export function buildImportArgs(
  accountId: string,
  blobId: string,
  addressBookId?: string,
): ImportArgs {
  const args: ImportArgs = { accountId, blobId };
  if (addressBookId) args.addressBookId = addressBookId;
  return args;
}

/** Build the Contact/export request args for various scopes. */
export function buildExportArgs(
  accountId: string,
  options: {
    ids?: string[];
    addressBookId?: string;
    fetchPhotos?: boolean;
  } = {},
): ExportArgs {
  const args: ExportArgs = { accountId };
  if (options.ids !== undefined) args.ids = options.ids;
  if (options.addressBookId !== undefined) args.addressBookId = options.addressBookId;
  if (options.fetchPhotos !== undefined) args.fetchPhotos = options.fetchPhotos;
  return args;
}

/**
 * Categorise import results into created / skipped / conflicts / failed /
 * withDuplicates sub-lists. withDuplicates may overlap with created
 * (ambiguous duplicate candidates are advisory on an otherwise-created
 * card); skipped and conflicts are each their own outcome and do not
 * overlap with created or with each other (REQ-CONT-82, re #206).
 */
export function parseImportSummary(results: ImportCardResult[]): {
  created: ImportCardResult[];
  skipped: ImportCardResult[];
  conflicts: ImportCardResult[];
  failed: ImportCardResult[];
  withDuplicates: ImportCardResult[];
} {
  const created = results.filter((r) => r.result === 'created');
  const skipped = results.filter((r) => r.result === 'skipped');
  const conflicts = results.filter((r) => r.result === 'conflict');
  const failed = results.filter((r) => r.result === 'failed');
  const withDuplicates = results.filter(
    (r) => (r.duplicateCandidates?.length ?? 0) > 0,
  );
  return { created, skipped, conflicts, failed, withDuplicates };
}

/**
 * Trigger a browser file download for the given URL and suggested filename.
 * Uses a transient anchor element so cookies attach (same-origin).
 */
export function triggerDownload(url: string, filename: string): void {
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
}
