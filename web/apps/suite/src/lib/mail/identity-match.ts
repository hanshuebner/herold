/**
 * Shared helpers for detecting whether a message was authored by the
 * signed-in user. Used by both MessageAccordion (visual self-card
 * treatment) and the compose store (reply / reply-all address selection).
 */

import type { Address, Identity } from './types';

/**
 * Build a Set of lowercased, trimmed email addresses from an iterable of
 * Identity objects. The set is used for O(1) membership tests.
 */
export function buildSelfEmailSet(identities: Iterable<Identity>): Set<string> {
  const out = new Set<string>();
  for (const id of identities) {
    const normalized = id.email.trim().toLowerCase();
    if (normalized) out.add(normalized);
  }
  return out;
}

/**
 * True when the first From address of `email` appears in `selfEmails`.
 * Case-insensitive; whitespace around the address is normalised.
 * Returns false when From is absent, empty, or null.
 */
export function isFromSelf(
  email: { from?: Array<Address> | null },
  selfEmails: Set<string>,
): boolean {
  const raw = email.from?.[0]?.email ?? '';
  const lc = raw.trim().toLowerCase();
  return lc !== '' && selfEmails.has(lc);
}
