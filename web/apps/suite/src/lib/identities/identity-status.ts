/**
 * Pure derivations over an `Identity` row for the Settings identity list
 * (REQ-SET-IDENT-01..08).
 *
 * Kept free of Svelte / store imports so the chip and sort logic is
 * trivially unit-testable. The list page consumes these helpers and
 * renders the corresponding chip / disabled affordance.
 */

import type { Identity } from '../mail/types';

/**
 * Three-state verification status surfaced as a row chip per
 * REQ-SET-IDENT-02:
 *
 *   - 'verified'    — `verifiedAt` is a non-null ISO timestamp.
 *   - 'verifying'   — `verifiedAt` is null AND `verificationPendingSince`
 *                     is a non-null ISO timestamp (server says a token
 *                     is live).
 *   - 'unverified'  — neither field marks verification (both null or
 *                     `verifiedAt` is null and `verificationPendingSince`
 *                     is null/absent).
 *
 * For legacy servers that have not yet shipped the extension properties
 * at all (both fields `undefined`), the identity is treated as
 * 'verified' — see `Identity.verifiedAt` doc-comment for the rationale
 * (preserves existing behaviour until the server starts emitting the
 * field).
 */
export type IdentityStatus = 'verified' | 'verifying' | 'unverified';

export function identityStatus(id: Identity): IdentityStatus {
  // Verified: timestamp present (truthy ISO string).
  if (id.verifiedAt) return 'verified';
  // Legacy server: neither field emitted → verified-by-default
  // (matches the reply-identity compat path in
  // lib/compose/reply-identity.ts).
  if (id.verifiedAt === undefined && id.verificationPendingSince === undefined) {
    return 'verified';
  }
  // Token live → verifying.
  if (id.verificationPendingSince) return 'verifying';
  // verifiedAt explicitly null and no live token → unverified.
  return 'unverified';
}

/**
 * Per-identity submission summary resolved from the external-submission
 * store. Passed as `sub` to the helpers below; null means "no record in
 * the store" (identity sends via the local outbound queue).
 *
 * The server's GET /api/v1/identities/{id}/submission response carries
 * `domain_authoritative: true` when the identity's domain is hosted on
 * this herold instance (re #74). Locally-hosted identities route through
 * herold's outbound queue and never need external SMTP, so
 * `domainAuthoritative: true` exempts the row from any external-SMTP gate
 * regardless of whether a submission record exists or is configured.
 */
export interface SubmissionSummary {
  /** Whether a submission record exists with `configured: true`. */
  configured: boolean;
  /** The server's last-known probe state, or null if absent. */
  state: 'ok' | 'auth-failed' | 'unreachable' | null;
  /**
   * True when the identity's email domain is authoritative on this server.
   * When true the identity routes through herold's local outbound queue and
   * is exempt from the external-SMTP gate regardless of `configured`.
   * Absent (undefined) is treated as false — callers that do not propagate
   * the field preserve existing behaviour.
   */
  domainAuthoritative?: boolean;
}

/**
 * Six-state identity classification for the settings list and compose picker
 * (REQ-SET-IDENT-08, re #98, re #107, re #115).
 *
 *   'authoritative'  — domain is hosted on this herold; identity routes via
 *                      the local outbound queue. Never disabled in the UI.
 *   'external-ok'    — verified external identity with a working configured
 *                      submission. Fully usable.
 *   'setup-needed'   — verified external whose submission record exists but
 *                      has configured: false (user started the wizard, did
 *                      not finish). Row is interactive; only the
 *                      set-as-default control is disabled until SMTP is set up.
 *   'broken'         — verified external with configured: true but probe
 *                      failed (auth-failed / unreachable). Distinct
 *                      attention/error state; default radio disabled.
 *   'verifying'      — verification token live but not yet confirmed.
 *   'unverified'     — verifiedAt is null and no live token.
 */
export type ExternalIdentityState =
  | 'authoritative'
  | 'external-ok'
  | 'setup-needed'
  | 'broken'
  | 'verifying'
  | 'unverified';

/**
 * Classify an identity into one of six states for the settings list and
 * compose picker. The caller passes `sub` from the submission store, or
 * null when no record exists (the "local outbound queue" case). Pure and
 * Svelte-free.
 */
export function externalIdentityState(
  id: Identity,
  sub: SubmissionSummary | null,
): ExternalIdentityState {
  const vStatus = identityStatus(id);
  if (vStatus === 'verifying') return 'verifying';
  if (vStatus === 'unverified') return 'unverified';
  // Verified identity.
  if (sub === null) return 'authoritative';
  if (sub.domainAuthoritative) return 'authoritative';
  if (!sub.configured) return 'setup-needed';
  if (sub.state === 'ok') return 'external-ok';
  // configured=true with a non-ok probe state (auth-failed / unreachable /
  // null — treat as broken in all non-ok cases).
  return 'broken';
}

/**
 * Whether this identity may be selected as the default From identity
 * (REQ-SET-IDENT-04). Verified identities with a working submission path
 * (authoritative queue or configured+ok external) are eligible. Unverified,
 * verifying, setup-needed, and broken identities are not; the caller should
 * disable the radio control.
 *
 * Pass `sub` when the submission store has loaded data for this identity.
 * When `sub` is omitted or undefined the check falls back to verification
 * state only (preserves existing behaviour for callers that lack submission
 * context).
 */
export function canBeDefault(id: Identity, sub?: SubmissionSummary | null): boolean {
  if (identityStatus(id) !== 'verified') return false;
  if (sub === undefined) return true;
  const state = externalIdentityState(id, sub);
  return state === 'authoritative' || state === 'external-ok';
}

/**
 * Whether the row should render in the disabled visual treatment
 * (REQ-SET-IDENT-08). Returns true for any state in which the identity
 * cannot send: unverified, verifying, setup-needed, and broken.
 *
 * Retained for sort/gating callers that treat all non-sendable states as
 * a single bucket. For rendering that distinguishes setup-needed from
 * broken, use `externalIdentityState` directly.
 */
export function isExternalWithoutSubmission(
  id: Identity,
  sub: SubmissionSummary | null,
): boolean {
  const state = externalIdentityState(id, sub);
  return state !== 'authoritative' && state !== 'external-ok';
}

/**
 * Stable sort for the identity list per REQ-SET-IDENT-03:
 *   (1) verified identities alphabetically by email,
 *   (2) pending identities by createdAt descending — the suite has no
 *       createdAt field on Identity yet, so we fall back to id (which
 *       is the server-issued allocator order, monotonically increasing)
 *       descending so newer pending rows surface first,
 *   (3) unverified identities last, alphabetically by email so the
 *       order is stable.
 *
 * The default identity is NOT special-cased: it sorts within the
 * verified group by email like any other verified identity. Which row
 * is the default is conveyed by the static "Standard" badge, not by
 * list position — so changing the default never reorders the list.
 *
 * Returns a new array; does not mutate the input.
 */
export function sortIdentities(identities: Identity[]): Identity[] {
  const rank = (id: Identity): number => {
    const s = identityStatus(id);
    if (s === 'verified') return 0;
    if (s === 'verifying') return 1;
    return 2;
  };
  return [...identities].sort((a, b) => {
    const ra = rank(a);
    const rb = rank(b);
    if (ra !== rb) return ra - rb;
    if (ra === 1) {
      // Pending: id descending (proxy for createdAt descending).
      return b.id.localeCompare(a.id);
    }
    return a.email.localeCompare(b.email);
  });
}

/**
 * Resolve the "current default" identity per REQ-SET-IDENT-04.
 * Prefers an Identity with an explicit `isDefault: true`; falls back to
 * the first verified identity (compat with the legacy "first identity
 * is the default" convention per REQ-SET-02). Returns null when the
 * list contains no verified identity at all.
 */
export function resolveDefault(identities: Identity[]): Identity | null {
  for (const id of identities) {
    if (id.isDefault) return id;
  }
  // Stable order so the fallback is deterministic across re-renders.
  const sorted = sortIdentities(identities);
  for (const id of sorted) {
    if (identityStatus(id) === 'verified') return id;
  }
  return null;
}
