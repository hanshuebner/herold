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

/**
 * True when `email` was actually sent by the signed-in user through
 * herold, as opposed to merely being *from* an address that happens to
 * match one of the user's registered Identities.
 *
 * Every message herold delivers (REQ-FLOW-34, `../../server/requirements/03-mail-flow.md`)
 * carries a server-injected `X-Herold-Recipient` header regardless of
 * who sent it; genuinely outbound messages never carry it (REQ-FLOW-35,
 * stripped unconditionally at submission). So a delivered message whose
 * From coincidentally matches one of the user's other identities — a
 * self-addressed test message, or mail from an address the user also
 * registered as an Identity for outbound use — is not an own-sent
 * message: it must route like any other received mail (reply goes back
 * to the sender), not like a continuation of the user's own
 * conversation (reply goes to the original recipients).
 *
 * Used to gate Reply / Reply-all `To`/`Cc` routing (REQ-MAIL-30a/31) and
 * `selectReplyIdentity`'s own-sent branch (REQ-MAIL-12a). NOT used for
 * the "Du"/self-card visual treatment (`isFromSelf` above), which labels
 * any message the user authored regardless of transport.
 */
export function isOwnSentMessage(
  email: { from?: Array<Address> | null; 'header:X-Herold-Recipient:asText'?: string | null },
  selfEmails: Set<string>,
): boolean {
  if (!isFromSelf(email, selfEmails)) return false;
  const recipient = email['header:X-Herold-Recipient:asText'];
  return recipient == null || recipient.trim() === '';
}
