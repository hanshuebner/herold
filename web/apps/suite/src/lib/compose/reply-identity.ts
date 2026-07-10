/**
 * Reply / Reply-all / Forward identity selection helper.
 *
 * Implements the algorithm specified by REQ-MAIL-12a in
 * `docs/design/web/requirements/02-mail-basics.md`:
 *
 *   1. If `parent.from[0].email` matches one of the user's `Identity`
 *      emails (own-sent reply), return that identity verbatim. This
 *      preserves the REQ-MAIL-30a precedence — the own-sent branch
 *      wins over the recipient-match rules below (the X-Herold-Recipient
 *      header is empty on outbound anyway, see REQ-FLOW-35).
 *   2. Otherwise, scan the parent's `to` and then `cc` lists in order;
 *      the first hit on a verified Identity wins. This prefers the
 *      address the sender actually used (visible in To/Cc) over the
 *      internal forwarding/delivery target.
 *   3. Otherwise, read the parent's `X-Herold-Recipient` header
 *      (server-injected at fan-out time per REQ-FLOW-34). When the
 *      header value matches a verified Identity, return that identity.
 *      This covers Bcc delivery and list mail where no user identity
 *      appears in To/Cc.
 *   4. Final fallback: the supplied default identity (REQ-MAIL-12).
 *
 * Verification gate: Unverified Identities NEVER win the To/Cc scan
 * (step 2) or the X-Herold-Recipient match (step 3). Step 1 (own-
 * sent) bypasses the verification gate because the user *did send*
 * the message, so the identity is implicitly authorised.
 *
 * Compatibility: until the server starts emitting `verifiedAt` on
 * Identity/get, a missing field is treated as "verified" so the
 * algorithm degrades gracefully against pre-feature backends. Once the
 * server lands the property, the verification gate kicks in
 * automatically without further suite changes.
 *
 * The helper is pure — it takes the parent Email, the identity set,
 * and the default identity, and returns the matched identity. It does
 * NOT mutate state, fetch headers, or read singleton stores; callers
 * are responsible for plumbing parent emails that already carry the
 * `header:X-Herold-Recipient:asText` projection.
 */

import type { Address, Email, Identity } from '../mail/types';

/**
 * True when the identity is "verified" for sending. The verification
 * gate (REQ-IDENT-60) refuses outbound sends from an unverified
 * Identity, so the suite must not preselect one as the reply From.
 *
 * Server compatibility: identity may not yet carry `verifiedAt` on the
 * wire. Treat missing as "verified" so this code path works against
 * pre-feature backends. Once the server emits the property, an
 * explicit `null` here means "user must verify before they can send
 * from this address" and we skip it.
 */
function isVerified(identity: Identity): boolean {
  const v = identity.verifiedAt;
  // Server may emit:
  //   - field absent (older server, pre-feature)  → treat as verified
  //   - explicit null                              → not verified
  //   - ISO timestamp string                       → verified
  if (v === undefined) return true;
  return v != null;
}

/**
 * Build a lower-cased lookup table from identity email to identity.
 * Duplicates (multiple Identity rows with the same email) are
 * collapsed — the first sighting wins, which is acceptable because a
 * second identity for the same address would have identical send
 * behaviour anyway.
 */
function identitiesByEmail(identities: readonly Identity[]): Map<string, Identity> {
  const map = new Map<string, Identity>();
  for (const id of identities) {
    const key = (id.email ?? '').toLowerCase().trim();
    if (!key) continue;
    if (!map.has(key)) map.set(key, id);
  }
  return map;
}

/**
 * Read the parent message's `X-Herold-Recipient` header. The
 * suite requests `header:X-Herold-Recipient:asText` on its Email/get
 * projection (see types.ts EMAIL_BODY_PROPERTIES); the value comes
 * back as a string when present, null/undefined otherwise. The header
 * is generated per-recipient by REQ-FLOW-34 from the inbound RCPT TO
 * envelope and contains exactly one canonical address.
 *
 * Returns the trimmed lower-cased address, or null when the header is
 * absent or empty. Case-insensitive by virtue of the lower-casing —
 * RFC 5321 §2.4 says local-parts are case-sensitive but most MTAs
 * canonicalise to lower-case; the suite follows suit so the comparison
 * is robust against case mismatches between the header value and the
 * Identity email.
 */
function readHeraldRecipient(parent: Email): string | null {
  const raw = parent['header:X-Herold-Recipient:asText'];
  if (raw == null) return null;
  const trimmed = raw.trim().toLowerCase();
  return trimmed.length > 0 ? trimmed : null;
}

/**
 * Walk an address list in order and return the first address whose
 * lower-cased email matches a verified identity.
 */
function firstVerifiedMatch(
  list: Address[] | null | undefined,
  byEmail: Map<string, Identity>,
): Identity | null {
  if (!list) return null;
  for (const addr of list) {
    const key = (addr.email ?? '').toLowerCase().trim();
    if (!key) continue;
    const id = byEmail.get(key);
    if (id && isVerified(id)) return id;
  }
  return null;
}

/**
 * Select the From identity for a reply / reply-all / forward against
 * `parent`. See the file header for the spec reference (REQ-MAIL-12a).
 *
 * The four-step algorithm is implemented inline so the precedence is
 * easy to audit:
 *
 *   1. Own-sent: parent.from[0].email matches an identity (any
 *      verification state — the user already sent the message, so
 *      they're authorised).
 *   2. parent.to / parent.cc scan → first verified identity.
 *   3. X-Herold-Recipient header → verified identity.
 *   4. Fallback to `defaultIdentity`.
 *
 * Callers (compose's openReply / openReplyAll / openForward) pass the
 * full identity list and the user's default identity (typically
 * `mail.primaryIdentity`). The returned identity is what the compose
 * pre-selects in its From field; the user is free to override.
 */
export function selectReplyIdentity(
  parent: Email,
  identities: readonly Identity[],
  defaultIdentity: Identity,
): Identity {
  const byEmail = identitiesByEmail(identities);

  // Step 1 — own-sent. REQ-MAIL-30a takes precedence over the
  // recipient-match rule below: when the user authored the parent,
  // continue the conversation from the same identity regardless of
  // any X-Herold-Recipient header (which is anyway absent on outbound
  // per REQ-FLOW-35).
  const fromEmail = (parent.from?.[0]?.email ?? '').toLowerCase().trim();
  if (fromEmail) {
    const own = byEmail.get(fromEmail);
    if (own) return own;
  }

  // Step 2 — scan To then Cc; first verified hit wins. Prefers the
  // address the sender actually used (visible in To/Cc) over the
  // internal forwarding/delivery target (X-Herold-Recipient, step 3).
  const toHit = firstVerifiedMatch(parent.to ?? null, byEmail);
  if (toHit) return toHit;
  const ccHit = firstVerifiedMatch(parent.cc ?? null, byEmail);
  if (ccHit) return ccHit;

  // Step 3 — X-Herold-Recipient header (verified gate applies). Covers
  // Bcc delivery and list mail where no user identity appears in To/Cc.
  const recipient = readHeraldRecipient(parent);
  if (recipient) {
    const matched = byEmail.get(recipient);
    if (matched && isVerified(matched)) return matched;
  }

  // Step 4 — terminal fallback.
  return defaultIdentity;
}

/**
 * Return the X-Herold-Recipient address from `parent` when it is present
 * and does NOT match any registered Identity. That address is an alias or
 * forwarding target the user does not have as a first-class identity;
 * callers (openReply / openReplyAll in compose.svelte.ts) should add it to
 * the reply's CC so the thread retains the routing address the original
 * message reached (addresses the scenario in REQ-FLOW-34 where the RCPT TO
 * envelope target differs from the user's registered identities).
 *
 * Returns null when:
 *   - the header is absent or empty — including outbound messages, which
 *     carry no X-Herold-Recipient per REQ-FLOW-35.
 *   - the delivery address matches a registered Identity by email (case-
 *     insensitive, same normalisation as `selectReplyIdentity`).
 */
export function deliveryAliasForCc(
  parent: Email,
  identities: readonly Identity[],
): string | null {
  const recipient = readHeraldRecipient(parent);
  if (!recipient) return null;
  const byEmail = identitiesByEmail(identities);
  if (byEmail.has(recipient)) return null;
  return recipient;
}

/**
 * Test seam: re-export the predicates so the test file can exercise
 * the verified gate without re-implementing it. Not part of the
 * stable public surface.
 */
export const _internals_forTest = {
  isVerified,
  identitiesByEmail,
  readHeraldRecipient,
  firstVerifiedMatch,
};
