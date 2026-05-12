/**
 * Pure helpers for the per-message tagged-address banner (REQ-MAIL-12c).
 *
 * The banner shows when a delivered message's X-Herold-Recipient header
 * carries a +suffix that has neither a matching managed filter (REQ-TAG-70)
 * nor a matching dismissal (REQ-TAG-60). All identity/filter/dismissal
 * matching is case-insensitive on the local-part per RFC 5233 §3.1 and on
 * the suffix per REQ-TAG-02; the canonical wire form is lower-case.
 *
 * No DOM or store dependency — the file is pure so the visibility logic
 * can be unit-tested with plain values and reused by callers that own
 * their own data fetching.
 */

import type { Identity } from './types';

/**
 * Result of REQ-TAG-01's suffix-extraction algorithm against an
 * X-Herold-Recipient header value.
 *
 * - `null` when the header is absent, empty, or carries no `+`.
 * - `{ baseAddress, suffix }` when a non-empty suffix is present.
 *   `baseAddress` is the original local-part minus the suffix concatenated
 *   with `@domain`, lower-cased. `suffix` is the lower-cased post-`+`
 *   bytes (REQ-TAG-02; additional `+` characters are part of the suffix).
 *
 * Edge cases:
 * - `alice+@example.local` → null (empty suffix, REQ-TAG-03).
 * - `alice@example.local` → null (no `+`).
 * - `alice+test+more@example.local` → `{ baseAddress: 'alice@example.local',
 *   suffix: 'test+more' }`.
 */
export interface ExtractedSuffix {
  baseAddress: string;
  suffix: string;
}

export function extractSuffix(headerValue: string | null | undefined): ExtractedSuffix | null {
  if (!headerValue) return null;
  const trimmed = headerValue.trim();
  if (!trimmed) return null;
  const at = trimmed.indexOf('@');
  if (at <= 0 || at === trimmed.length - 1) return null;
  const local = trimmed.slice(0, at);
  const domain = trimmed.slice(at + 1);
  const plus = local.indexOf('+');
  if (plus <= 0) return null; // no `+`, or starts with `+`
  const baseLocal = local.slice(0, plus);
  const suffix = local.slice(plus + 1);
  if (!suffix) return null; // REQ-TAG-03: trailing `+` is not a tag
  return {
    baseAddress: `${baseLocal}@${domain}`.toLowerCase(),
    suffix: suffix.toLowerCase(),
  };
}

/**
 * Find the Identity whose canonical email matches the extracted base
 * address. Returns null when no identity owns this base address. The
 * caller can pre-filter by verified-only if it wants; REQ-MAIL-12c
 * does not require verified identities for the banner (the banner is a
 * receive-side affordance, REQ-IDENT-60 gates send-side only).
 */
export function findBaseIdentity(
  identities: Iterable<Identity>,
  baseAddress: string,
): Identity | null {
  const target = baseAddress.toLowerCase();
  for (const id of identities) {
    if (id.email.toLowerCase() === target) return id;
  }
  return null;
}

/** Shape of a managed tagged-address filter row, as seen by the banner. */
export interface FilterRecord {
  id: string;
  baseIdentityId: string;
  suffix: string; // lower-cased canonical form (REQ-TAG-02)
}

/** Shape of a dismissal row, as seen by the banner. */
export interface DismissalRecord {
  baseIdentityId: string;
  suffix: string; // lower-cased canonical form (REQ-TAG-02)
}

/**
 * The banner is visible when ALL of the following hold (REQ-MAIL-12c):
 *   1. The capability https://netzhansa.com/jmap/tagged-addresses is
 *      advertised on the JMAP session (caller-supplied flag).
 *   2. X-Herold-Recipient carries a non-empty suffix.
 *   3. The principal owns an Identity whose canonical email matches the
 *      base address (otherwise the message arrived via an alias or
 *      forwarder and the (base_identity, suffix) pair is not addressable
 *      by the suite).
 *   4. No filter row exists for (baseIdentity, suffix).
 *   5. No dismissal row exists for (baseIdentity, suffix).
 *
 * Returns the resolved (baseIdentity, suffix) pair when visible; null
 * otherwise. Suffix in the result is the lower-cased canonical form.
 */
export interface BannerVisibility {
  baseIdentityId: string;
  baseAddress: string;
  suffix: string;
}

export interface BannerInputs {
  capabilityAdvertised: boolean;
  headerValue: string | null | undefined;
  identities: Iterable<Identity>;
  filters: Iterable<FilterRecord>;
  dismissals: Iterable<DismissalRecord>;
}

export function bannerVisibility(inputs: BannerInputs): BannerVisibility | null {
  if (!inputs.capabilityAdvertised) return null;
  const extracted = extractSuffix(inputs.headerValue);
  if (!extracted) return null;
  const identity = findBaseIdentity(inputs.identities, extracted.baseAddress);
  if (!identity) return null;
  for (const f of inputs.filters) {
    if (
      f.baseIdentityId === identity.id &&
      f.suffix.toLowerCase() === extracted.suffix
    ) {
      return null;
    }
  }
  for (const d of inputs.dismissals) {
    if (
      d.baseIdentityId === identity.id &&
      d.suffix.toLowerCase() === extracted.suffix
    ) {
      return null;
    }
  }
  return {
    baseIdentityId: identity.id,
    baseAddress: extracted.baseAddress,
    suffix: extracted.suffix,
  };
}

/**
 * Capitalise the first letter of `s` for the label-name default
 * (REQ-MAIL-12c: "pre-populated with the suffix capitalised"). Leaves
 * the rest of the string verbatim so a suffix like `aws-prod` becomes
 * `Aws-prod` and the user can edit; we do not try to be clever about
 * dashes or camel case.
 */
export function capitalise(s: string): string {
  if (!s) return s;
  return s.charAt(0).toUpperCase() + s.slice(1);
}
