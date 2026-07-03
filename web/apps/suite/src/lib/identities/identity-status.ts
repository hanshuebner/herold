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
 * Whether this identity may be selected as the default From identity
 * (REQ-SET-IDENT-04). Only verified identities are selectable; the
 * radio is disabled on unverified / pending rows.
 */
export function canBeDefault(id: Identity): boolean {
  return identityStatus(id) === 'verified';
}

/**
 * Whether the row should render in the disabled visual treatment.
 * REQ-SET-IDENT-08 ties this to "external-without-submission" — an
 * external Identity (one whose owning domain is not hosted on this
 * herold) that has no working external submission configured. The
 * caller threads `externalSubmissionConfigured` from the per-identity
 * submission store; the helper combines it with the verification state
 * so a fresh unverified Identity does not gain a working radio just
 * because submission setup is pending.
 *
 * The server's GET /api/v1/identities/{id}/submission response carries
 * `domain_authoritative: true` when the identity's domain is hosted on
 * this herold instance (re #74). Locally-hosted identities route through
 * herold's outbound queue and never need external SMTP, so
 * `domainAuthoritative: true` exempts the row from the disabled treatment
 * regardless of whether a submission record exists or is configured.
 *
 * Verified identities with no submission record at all (sub === null) are
 * also NOT considered external — absence of a record means "send via the
 * local outbound queue".
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

export function isExternalWithoutSubmission(
  id: Identity,
  sub: SubmissionSummary | null,
): boolean {
  // Unverified rows are always disabled — the user cannot use the
  // identity at all yet, so the row is greyed independent of any
  // submission state.
  if (identityStatus(id) !== 'verified') return true;
  // No submission record at all → the identity sends via the local
  // outbound queue; not external; not disabled.
  if (sub === null) return false;
  // Domain is authoritative on this server → sends via the local outbound
  // queue; external SMTP is irrelevant regardless of `configured`.
  if (sub.domainAuthoritative) return false;
  // Configured + ok → working external; row is active.
  if (sub.configured && sub.state === 'ok') return false;
  // Configured + alert state → external but broken; treat as disabled.
  // Unconfigured submission record is "external-without-submission"
  // (the row exists because the user started but did not finish the
  // wizard) — REQ-SET-IDENT-08.
  return true;
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
