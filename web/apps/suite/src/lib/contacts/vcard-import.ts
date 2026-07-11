/**
 * Pure helpers for the vCard import / export JMAP driver.
 *
 * Types for Contact/import and Contact/export, plus pure functions for
 * building request args and categorising response results.
 *
 * REQ-CONT-80..83 (docs/design/web/requirements/27-contacts.md).
 */

/** Per-card outcome from Contact/import. */
export interface ImportCardResult {
  /** Zero-based position of the card in the uploaded .vcf. */
  index: number;
  uid?: string;
  result: 'created' | 'failed';
  /** JMAP contact id, present when result === 'created'. */
  id?: string;
  /** Server-supplied reason, present when result === 'failed'. */
  reason?: string;
  /**
   * Advisory: existing contact ids that look like duplicates of this card
   * (matched by uid or primary email). Present on either outcome.
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
 * Categorise import results into created / failed / withDuplicates sub-lists.
 * withDuplicates may overlap with created (duplicate candidates are advisory on
 * either outcome).
 */
export function parseImportSummary(results: ImportCardResult[]): {
  created: ImportCardResult[];
  failed: ImportCardResult[];
  withDuplicates: ImportCardResult[];
} {
  const created = results.filter((r) => r.result === 'created');
  const failed = results.filter((r) => r.result === 'failed');
  const withDuplicates = results.filter(
    (r) => (r.duplicateCandidates?.length ?? 0) > 0,
  );
  return { created, failed, withDuplicates };
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
