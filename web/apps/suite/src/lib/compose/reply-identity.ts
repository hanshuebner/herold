/**
 * Reply / Reply-all / Forward identity selection helper.
 *
 * Implements the algorithm specified by REQ-MAIL-12a in
 * `docs/design/web/requirements/02-mail-basics.md`:
 *
 *   1. If the parent carries no `X-Herold-Recipient` header (i.e. it is
 *      genuinely outbound — the user sent it through herold, see
 *      REQ-FLOW-35) AND `parent.from[0].email` matches one of the
 *      user's `Identity` emails (own-sent reply), return that identity
 *      verbatim. This preserves the REQ-MAIL-30a precedence — the
 *      own-sent branch wins over the recipient-match rules below. The
 *      header check matters because every message herold actually
 *      *delivers* (REQ-FLOW-34) carries the header regardless of who
 *      sent it — so a delivered message whose From coincidentally
 *      matches one of the user's other identities (e.g. a self-
 *      addressed test message, or mail from an address the user also
 *      registered as an Identity) is NOT an own-sent reply and must not
 *      hijack the From selection away from the identity it was
 *      actually delivered to (steps 2/3 below).
 *   2. Otherwise, scan the parent's `to` and then `cc` lists in order;
 *      the first hit on a verified Identity wins. This prefers the
 *      address the sender actually used (visible in To/Cc) over the
 *      internal forwarding/delivery target.
 *   3. Otherwise, read the parent's `X-Herold-Recipient` header
 *      (server-injected at fan-out time per REQ-FLOW-34). When the
 *      header value matches a verified Identity, return that identity.
 *      This covers Bcc delivery and list mail where no user identity
 *      appears in To/Cc.
 *   3a. Domain fallback: role/alias addresses (e.g. `info@` / `vorsitz@`
 *       on a domain the account holds an Identity for) route mail into
 *       the mailbox without being registered as a first-class Identity
 *       themselves (see #146/#147/#164). When neither the exact-address
 *       steps above nor the To/Cc scan produced a match, and
 *       `X-Herold-Recipient` is present, its domain (and ONLY its
 *       domain — To/Cc are not consulted) is checked against a verified
 *       Identity's domain; a match wins. `X-Herold-Recipient` is the
 *       authoritative delivery signal, so when it names a domain none
 *       of the user's identities own, the fallback stops there rather
 *       than reaching into To/Cc, where an unrelated third-party
 *       address could otherwise coincidentally share a domain with a
 *       *different* one of the user's identities and hijack the From
 *       to an identity the message was never delivered to. Only when
 *       `X-Herold-Recipient` is absent entirely (older messages) does
 *       the domain fallback consult To then Cc.
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

/** Lower-cased domain (part after `@`) of an email address, or null. */
function domainOf(email: string): string | null {
  const at = email.indexOf('@');
  return at >= 0 ? email.slice(at + 1) : null;
}

/**
 * Build a lower-cased lookup table from identity domain to the first
 * verified identity registered on that domain (encounter order in
 * `identities` wins on a tie). Used by the step-3a domain fallback: a
 * role/alias address that is not itself a registered Identity (e.g.
 * `vorsitz@` routing into a mailbox whose registered Identity is
 * `presse@` on the same domain) still resolves to an identity on the
 * conversation's own domain rather than falling through to an
 * unrelated global default.
 */
function firstVerifiedIdentityByDomain(
  identities: readonly Identity[],
): Map<string, Identity> {
  const map = new Map<string, Identity>();
  for (const id of identities) {
    if (!isVerified(id)) continue;
    const domain = domainOf((id.email ?? '').toLowerCase().trim());
    if (!domain) continue;
    if (!map.has(domain)) map.set(domain, id);
  }
  return map;
}

/**
 * Return the first address in `list` whose domain (case-insensitive)
 * appears in `byDomain`, mapped to its identity. Used for the step-3a
 * domain fallback's To/Cc source.
 */
function firstDomainMatch(
  list: Address[] | null | undefined,
  byDomain: Map<string, Identity>,
): Identity | null {
  if (!list) return null;
  for (const addr of list) {
    const key = (addr.email ?? '').toLowerCase().trim();
    if (!key) continue;
    const domain = domainOf(key);
    if (!domain) continue;
    const id = byDomain.get(domain);
    if (id) return id;
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
 *   1. Own-sent: no X-Herold-Recipient header (genuinely outbound, see
 *      REQ-FLOW-35) AND parent.from[0].email matches an identity (any
 *      verification state — the user already sent the message, so
 *      they're authorised).
 *   2. parent.to / parent.cc scan → first verified identity.
 *   3. X-Herold-Recipient header → verified identity.
 *   3a. Domain fallback: when X-Herold-Recipient is present, ONLY its
 *       domain is matched against a verified identity's domain (To/Cc
 *       are not consulted — an unmatched delivery domain is
 *       authoritative and must not fall through to a coincidental
 *       To/Cc domain hit). When X-Herold-Recipient is absent entirely,
 *       parent.to then parent.cc are matched by domain instead.
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

  // Read once — used to gate step 1 and, when step 1 doesn't apply, to
  // match step 3.
  const recipient = readHeraldRecipient(parent);

  // Step 1 — own-sent. Only applies to messages the user genuinely
  // sent through herold (no X-Herold-Recipient header). Every message
  // herold delivers carries the header regardless of who sent it
  // (REQ-FLOW-34), so a delivered message whose From coincidentally
  // matches one of the user's other identities must fall through to
  // the recipient-match rules below rather than hijacking the From
  // selection.
  if (recipient == null) {
    const fromEmail = (parent.from?.[0]?.email ?? '').toLowerCase().trim();
    if (fromEmail) {
      const own = byEmail.get(fromEmail);
      if (own) return own;
    }
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
  if (recipient) {
    const matched = byEmail.get(recipient);
    if (matched && isVerified(matched)) return matched;
  }

  // Step 3a — domain fallback. The delivered-to address may be a
  // role/alias address that routes into the mailbox without being a
  // registered Identity itself (#146/#147/#164). When its domain
  // matches a verified identity's domain, that identity is still a
  // better From than the account's unrelated global default.
  //
  // X-Herold-Recipient is the authoritative delivery signal, so it is
  // the ONLY source consulted when present: if its domain matches no
  // identity, the message was genuinely delivered to a domain the
  // account has no identity for, and the fallback must not reach into
  // To/Cc — an unrelated third-party address in To/Cc could otherwise
  // coincidentally share a domain with a *different* one of the
  // user's identities (e.g. a multi-domain account CC'd by a
  // correspondent whose own address happens to sit on one of the
  // user's other domains) and hijack the From to an identity the
  // message was never delivered to. To/Cc domain matching is only
  // consulted when there is no X-Herold-Recipient at all (older
  // messages that predate the header, or messages where the server
  // never stamped it).
  const byDomain = firstVerifiedIdentityByDomain(identities);
  if (byDomain.size > 0) {
    if (recipient) {
      const domain = domainOf(recipient);
      const domainHit = domain ? byDomain.get(domain) : undefined;
      if (domainHit) return domainHit;
      // Recipient known but its domain matches nothing — the delivery
      // domain is authoritative and unrelated; do not fall through to
      // To/Cc domain guessing.
    } else {
      const toDomainHit = firstDomainMatch(parent.to ?? null, byDomain);
      if (toDomainHit) return toDomainHit;
      const ccDomainHit = firstDomainMatch(parent.cc ?? null, byDomain);
      if (ccDomainHit) return ccDomainHit;
    }
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
 *
 * @deprecated Use `localAliasesForCc` which also covers the
 * upstream-expanded-alias case where the external MX resolves the alias
 * before herold sees the envelope, so X-Herold-Recipient ends up as the
 * resolved mailbox (a registered Identity), not the original alias.
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
 * Return all "local alias" addresses that should be added to the reply Cc.
 *
 * Two sources are checked in encounter order with global deduplication:
 *
 *   1. X-Herold-Recipient (REQ-FLOW-34): when present and not matching a
 *      registered Identity, this address is an alias herold itself saw in
 *      the SMTP envelope. It is included so the reply retains the alias.
 *
 *   2. Visible To/Cc addresses: any address in the parent's To or Cc header
 *      whose domain appears in at least one registered Identity email and
 *      which is not itself a registered Identity. This covers the
 *      upstream-expanded-alias case where an external MX resolves a local
 *      alias before herold sees the envelope; X-Herold-Recipient then
 *      carries the resolved mailbox (a registered Identity) and source 1
 *      correctly no-ops, but the original alias survives in the visible
 *      To/Cc header and must be preserved in the reply Cc.
 *
 * `ownMessage` must be true when the caller is replying to one of the
 * user's own sent messages. In that case `parent.to` becomes the reply's
 * To field, so those addresses must not be scanned for Cc additions.
 * `parent.cc` is always scanned regardless of `ownMessage`. Outbound
 * messages carry no X-Herold-Recipient per REQ-FLOW-35, so source 1 is
 * automatically a no-op for own-sent messages.
 *
 * Returns lower-cased, deduplicated addresses in encounter order.
 * Returns an empty array when there are no qualifying addresses.
 */
export function localAliasesForCc(
  parent: Email,
  identities: readonly Identity[],
  ownMessage?: boolean,
): string[] {
  const byEmail = identitiesByEmail(identities);
  const result: string[] = [];
  const seen = new Set<string>();

  // Source 1 — X-Herold-Recipient. Same logic as deliveryAliasForCc.
  const recipient = readHeraldRecipient(parent);
  if (recipient && !byEmail.has(recipient)) {
    seen.add(recipient);
    result.push(recipient);
  }

  // Source 2 — visible To/Cc scan against identity domains.
  if (byEmail.size > 0) {
    // Build the set of domains owned by registered identities. Only
    // addresses on these domains can be "local aliases"; external-domain
    // To/Cc recipients are never candidates.
    const identityDomains = new Set<string>();
    for (const email of byEmail.keys()) {
      const at = email.indexOf('@');
      if (at >= 0) identityDomains.add(email.slice(at + 1));
    }
    // When replying to an own-sent message, parent.to addresses will
    // become the reply's To field — skip them here to avoid duplicating
    // those addresses in Cc. parent.cc is always scanned.
    const listsToScan: (Address[] | null | undefined)[] = ownMessage
      ? [parent.cc]
      : [parent.to, parent.cc];
    for (const list of listsToScan) {
      if (!list) continue;
      for (const addr of list) {
        const lc = (addr.email ?? '').toLowerCase().trim();
        if (!lc || seen.has(lc)) continue;
        seen.add(lc);
        if (byEmail.has(lc)) continue; // registered identity — skip
        const at = lc.indexOf('@');
        if (at < 0) continue;
        if (identityDomains.has(lc.slice(at + 1))) {
          result.push(lc);
        }
      }
    }
  }

  return result;
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
  firstVerifiedIdentityByDomain,
  firstDomainMatch,
};
