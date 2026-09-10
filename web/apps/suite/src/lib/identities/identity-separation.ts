/**
 * Pure derivations over `Identity.subAccountId` / `Identity.separation`
 * (issue #212, REQ-MAIL-SUB-01/07, REQ-SUBACCT-09/10).
 *
 * Kept free of Svelte / store imports so the gating and progress logic is
 * trivially unit-testable, mirroring `identity-status.ts`.
 */

import type { Identity, IdentitySeparation, SeparationState } from '../mail/types';

const NONE: IdentitySeparation = {
  state: 'none',
  messagesTotal: 0,
  messagesMoved: 0,
  messagesCopied: 0,
};

/**
 * Resolve `identity.separation` with the legacy-server default: a server
 * that predates the sub-account substrate omits the property entirely,
 * which is equivalent to "never separated" (REQ-MAIL-SUB-09 capability
 * gate already hides every separation affordance in that case, but this
 * keeps any direct reader of the field from having to null-check).
 */
export function separationOf(identity: Identity): IdentitySeparation {
  return identity.separation ?? NONE;
}

export function separationState(identity: Identity): SeparationState {
  return separationOf(identity).state;
}

/**
 * Whether `identity` is eligible for the "Separate this identity" action
 * (REQ-MAIL-SUB-01). Only non-default identities that have never been
 * separated are eligible: the principal's own sign-in identity
 * (`mayDelete: false`) cannot be separated, and an identity already
 * migrating or separated has moved off the parent account's
 * `Identity/get` entirely, so the affordance no longer applies to it.
 */
export function canSeparate(identity: Identity): boolean {
  return identity.mayDelete && separationState(identity) === 'none';
}

/**
 * Messages moved + copied so far, for a progress readout while
 * `state === 'migrating'`.
 */
export function separationProgress(identity: Identity): number {
  const s = separationOf(identity);
  return s.messagesMoved + s.messagesCopied;
}
