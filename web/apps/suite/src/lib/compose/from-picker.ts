/**
 * Pure derivations for the compose-window From dropdown (REQ-MAIL-12).
 *
 * Kept Svelte / store-free so the sort, the disabled-row predicate, and
 * the send-gating helper are trivially unit-testable. The
 * `FromPicker.svelte` component consumes these helpers and renders the
 * dropdown; the compose store consumes `composeFromGating` to drive the
 * Send button.
 *
 * Three concepts:
 *
 *   1. `composeFromSort` — order the principal's identities for display.
 *      Verified-default first, then other verified, then verifying,
 *      then unverified, then external-without-submission (last because
 *      they are unusable even though verified).
 *
 *   2. `isFromSelectable` — true when the user is allowed to send mail
 *      from the identity. Verified-and-locally-routed, OR verified-and-
 *      external-submission-configured-and-ok. Unverified, verifying,
 *      and external-without-submission identities all return false.
 *
 *   3. `composeFromGating` — given the selected identity + its
 *      submission summary, return a `{ allowed, reason }` object so the
 *      Send button can disable itself with a localized reason string.
 *      The reason is a stable key the caller translates.
 */

import type { Identity } from '../mail/types';
import {
  identityStatus,
  isExternalWithoutSubmission,
  externalIdentityState,
  type SubmissionSummary,
} from '../identities/identity-status';

/**
 * Reason key surfaced by composeFromGating when the selected identity is
 * not allowed to send. The caller maps these to localized strings.
 *
 *   - 'unverified'               — verifiedAt is null, no live token.
 *   - 'verifying'                — verification token live but not yet
 *                                  confirmed.
 *   - 'external-setup-needed'    — identity is verified external but
 *                                  submission has not been configured yet
 *                                  (configured: false).
 *   - 'external-broken'          — identity is verified external with
 *                                  submission configured but probe failed
 *                                  (auth-failed / unreachable).
 *   - 'no-identity'              — no identity is selected at all
 *                                  (rare; would be a store-warmup race).
 */
export type FromGatingReason =
  | 'unverified'
  | 'verifying'
  | 'external-setup-needed'
  | 'external-broken'
  | 'no-identity';

export interface FromGating {
  /** True when the Send button may be enabled on this identity. */
  allowed: boolean;
  /** Stable key explaining the disable; null when allowed=true. */
  reason: FromGatingReason | null;
}

/**
 * Compute the gating verdict for a candidate From identity.
 *
 * `submission` is the SubmissionSummary the caller resolved for the
 * identity (typically via `submissionStore.forIdentity(id).data`); pass
 * `null` when the per-identity submission record is absent (the local-
 * outbound case) or when the external-submission capability is not
 * advertised at all. When the capability is absent, an external broken
 * identity cannot be detected — but neither can the user actually have
 * one, since they couldn't configure submission in the first place.
 *
 * The verdict is the single source of truth for the Send button's
 * enabled state.
 */
export function composeFromGating(
  identity: Identity | null,
  submission: SubmissionSummary | null,
): FromGating {
  if (!identity) {
    return { allowed: false, reason: 'no-identity' };
  }
  const status = identityStatus(identity);
  if (status === 'unverified') {
    return { allowed: false, reason: 'unverified' };
  }
  if (status === 'verifying') {
    return { allowed: false, reason: 'verifying' };
  }
  // Verified — use the six-state classifier to distinguish authoritative /
  // external-ok (both allowed) from setup-needed and broken (both blocked
  // but with distinct reasons for the UI).
  const extState = externalIdentityState(identity, submission);
  if (extState === 'authoritative' || extState === 'external-ok') {
    return { allowed: true, reason: null };
  }
  if (extState === 'broken') {
    return { allowed: false, reason: 'external-broken' };
  }
  return { allowed: false, reason: 'external-setup-needed' };
}

/**
 * True when the user may pick this identity in the From dropdown.
 * Same predicate as `composeFromGating(...).allowed`, expressed as a
 * boolean for the click-handler short-circuit.
 */
export function isFromSelectable(
  identity: Identity,
  submission: SubmissionSummary | null,
): boolean {
  return composeFromGating(identity, submission).allowed;
}

/**
 * Sort the principal's identities for the From dropdown
 * (REQ-MAIL-12 ordering):
 *
 *   1. The default identity (isDefault=true) — first.
 *   2. Other verified identities, locally-routed (sub=null) or with a
 *      working external submission. Sorted alphabetically by email.
 *   3. Verifying identities (token live, not yet confirmed). Sorted by
 *      id descending so the most recently added bubbles up.
 *   4. Unverified identities (verifiedAt=null and no live token).
 *      Alphabetical by email.
 *   5. External-without-submission identities (verified but broken /
 *      unconfigured submission). Last because they need the most work
 *      from the user before they're usable. Alphabetical by email.
 *
 * `submissionResolver` returns the per-identity submission summary;
 * the caller wires it to `submissionStore.forIdentity(id).data`. The
 * sort is stable — same input always yields same output for tests.
 *
 * Returns a new array; does not mutate the input.
 */
export function composeFromSort(
  identities: readonly Identity[],
  submissionResolver: (id: string) => SubmissionSummary | null,
): Identity[] {
  const rank = (id: Identity): number => {
    if (id.isDefault) return 0;
    const status = identityStatus(id);
    const sub = submissionResolver(id.id);
    if (status === 'verified') {
      // Verified + working = bucket 1. Verified + external-broken = bucket 4.
      return isExternalWithoutSubmission(id, sub) ? 4 : 1;
    }
    if (status === 'verifying') return 2;
    return 3; // unverified
  };
  return [...identities].sort((a, b) => {
    const ra = rank(a);
    const rb = rank(b);
    if (ra !== rb) return ra - rb;
    if (ra === 2) {
      // Verifying: id descending (proxy for createdAt descending) so newer
      // pending rows appear first. Matches identityStatus.sortIdentities.
      return b.id.localeCompare(a.id);
    }
    return a.email.localeCompare(b.email);
  });
}

/**
 * Whether the picker should render at all (REQ-MAIL-12 visibility
 * gate): at least one identity is present. The picker is always shown
 * when the user has any identity so they can confirm and change the
 * From address during a reply — even a single-identity user benefits
 * from being able to see which identity is pre-selected.
 *
 * Unverified identities and external-without-submission identities
 * appear in the picker as disabled rows; the picker is still shown so
 * the affordance to Verify them from compose is not hidden.
 *
 * `submissionResolver` is the same callback shape as composeFromSort.
 */
export function shouldShowFromPicker(
  identities: readonly Identity[],
  _submissionResolver: (id: string) => SubmissionSummary | null,
): boolean {
  // One identity or more — show the picker unconditionally. The
  // static-text fallback in ComposeWindow only appears when there
  // are no identities at all (loading / warmup race).
  return identities.length >= 1;
}
