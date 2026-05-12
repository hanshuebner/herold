/**
 * Pure helpers for the Settings → Tagged addresses surface
 * (REQ-SET-TAG-01..21).
 *
 * No DOM / store dependency. The form component and the table
 * component import these for validation, sort, and label derivation;
 * unit tests exercise the same helpers directly.
 */

/** The action a TaggedAddressFilter applies on a matching delivery. */
export type FilterAction = 'label' | 'label_archive' | 'label_archive_read';

/** Exhaustive enumeration so the i18n labels never drift from the wire. */
export const FILTER_ACTIONS: readonly FilterAction[] = [
  'label',
  'label_archive',
  'label_archive_read',
] as const;

/**
 * Normalise a user-entered suffix to the canonical wire form
 * (REQ-TAG-02): strip surrounding whitespace, lower-case. The form's
 * commit path invokes this on submit so the user sees their original
 * mixed-case typing while editing.
 */
export function normaliseSuffix(s: string): string {
  return s.trim().toLowerCase();
}

/**
 * Validate a suffix per the server-side gate at
 * internal/protojmap/mail/taggedaddress/types.go validateSuffix
 * (REQ-TAG-13 / REQ-TAG-83):
 *   - non-empty after trim+lowercase
 *   - at most 64 bytes (UTF-8 length so we count code units)
 *   - no whitespace
 *   - no '@' (forbidden — the local-part / domain boundary)
 *   - no ASCII control bytes (<0x20, 0x7f) — the wire rejects them
 *
 * Returns null when valid, otherwise an i18n key referencing the
 * specific failure mode. The caller maps the key to a localised
 * message; the helpers are deliberately UI-agnostic.
 */
export type SuffixError =
  | 'settings.taggedAddresses.error.suffixEmpty'
  | 'settings.taggedAddresses.error.suffixTooLong'
  | 'settings.taggedAddresses.error.suffixWhitespace'
  | 'settings.taggedAddresses.error.suffixForbidden';

export function validateSuffix(rawOrNormalised: string): SuffixError | null {
  const s = normaliseSuffix(rawOrNormalised);
  if (s.length === 0) return 'settings.taggedAddresses.error.suffixEmpty';
  // The server's 64-byte cap is on bytes; for ASCII suffixes this is
  // identical to .length. The form rejects suffixes that exceed the
  // byte budget under UTF-8 too — be conservative and use the encoder.
  const byteLen = new TextEncoder().encode(s).length;
  if (byteLen > 64) return 'settings.taggedAddresses.error.suffixTooLong';
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c <= 0x20 || c === 0x7f) {
      // 0x20 == ' '; the validator already trimmed surrounding spaces,
      // so any space remaining is interior.
      return c === 0x20
        ? 'settings.taggedAddresses.error.suffixWhitespace'
        : 'settings.taggedAddresses.error.suffixForbidden';
    }
    if (s[i] === '@') return 'settings.taggedAddresses.error.suffixForbidden';
  }
  return null;
}

/**
 * Validate the label name per REQ-TAG-32 / REQ-TAG-83:
 *   - non-empty after trim
 *   - at most 255 bytes (the JMAP set handler enforces the same)
 *
 * Returns null when valid, otherwise an i18n key.
 */
export type LabelError =
  | 'settings.taggedAddresses.error.labelEmpty'
  | 'settings.taggedAddresses.error.labelTooLong';

export function validateLabelName(label: string): LabelError | null {
  const trimmed = label.trim();
  if (trimmed.length === 0) return 'settings.taggedAddresses.error.labelEmpty';
  const byteLen = new TextEncoder().encode(trimmed).length;
  if (byteLen > 255) return 'settings.taggedAddresses.error.labelTooLong';
  return null;
}

/**
 * Return the i18n key for the human-readable description of a filter
 * action. Exhaustive switch — adding a new action without updating
 * here is a type error (REQ-TAG-30 currently enumerates exactly
 * three).
 */
export function actionLabelKey(
  action: FilterAction,
):
  | 'settings.taggedAddresses.action.label'
  | 'settings.taggedAddresses.action.labelArchive'
  | 'settings.taggedAddresses.action.labelArchiveRead' {
  switch (action) {
    case 'label':
      return 'settings.taggedAddresses.action.label';
    case 'label_archive':
      return 'settings.taggedAddresses.action.labelArchive';
    case 'label_archive_read':
      return 'settings.taggedAddresses.action.labelArchiveRead';
  }
}

/**
 * Compose the tagged-address chip text: "alice+amazon@example.local"
 * (REQ-SET-TAG-01). When the base address is missing (the identity
 * was deleted) the helper falls back to `+suffix` so the row remains
 * readable.
 */
export function composeTaggedAddress(
  baseEmail: string | null | undefined,
  suffix: string,
): string {
  if (!baseEmail || baseEmail.length === 0) return `+${suffix}`;
  const at = baseEmail.indexOf('@');
  if (at <= 0) return `+${suffix}`;
  return `${baseEmail.slice(0, at)}+${suffix}${baseEmail.slice(at)}`;
}

/**
 * Sort comparator for tagged-address filters (REQ-SET-TAG-02): by base
 * identity email (alphabetical), then by suffix (alphabetical). The
 * caller passes in a mapping from baseIdentityId -> email so the
 * helper does not need a live store reference.
 */
export interface SortableFilter {
  id: string;
  baseIdentityId: string;
  suffix: string;
}

export function sortFilters<T extends SortableFilter>(
  filters: readonly T[],
  emailFor: (baseIdentityId: string) => string | null,
): T[] {
  return [...filters].sort((a, b) => {
    const ea = (emailFor(a.baseIdentityId) ?? '').toLowerCase();
    const eb = (emailFor(b.baseIdentityId) ?? '').toLowerCase();
    if (ea < eb) return -1;
    if (ea > eb) return 1;
    if (a.suffix < b.suffix) return -1;
    if (a.suffix > b.suffix) return 1;
    return 0;
  });
}

/**
 * Sort comparator for dismissals — same shape and ordering as the
 * filters table so the user's eye lands on the same identity in
 * both subsections.
 */
export interface SortableDismissal {
  baseIdentityId: string;
  suffix: string;
}

export function sortDismissals<T extends SortableDismissal>(
  dismissals: readonly T[],
  emailFor: (baseIdentityId: string) => string | null,
): T[] {
  return [...dismissals].sort((a, b) => {
    const ea = (emailFor(a.baseIdentityId) ?? '').toLowerCase();
    const eb = (emailFor(b.baseIdentityId) ?? '').toLowerCase();
    if (ea < eb) return -1;
    if (ea > eb) return 1;
    if (a.suffix < b.suffix) return -1;
    if (a.suffix > b.suffix) return 1;
    return 0;
  });
}

/** Hard caps mirrored from the server (REQ-TAG-86, REQ-TAG-11). */
export const FILTER_CAP = 100;
export const DISMISSAL_CAP = 500;

/** Approaching threshold for the cap notice (REQ-SET-TAG-07). */
export const FILTER_CAP_WARN_REMAINING = 10;
export const DISMISSAL_CAP_WARN_REMAINING = 25;
