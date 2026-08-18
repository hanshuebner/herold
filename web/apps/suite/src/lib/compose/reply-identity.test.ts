/**
 * Tests for `selectReplyIdentity` — the REQ-MAIL-12a algorithm.
 *
 * The helper is pure, so the tests exercise it directly: construct a
 * parent Email with the relevant fields, a small identity set, and a
 * default identity, then assert the matched result.
 *
 * Verification gate compatibility note (REQ-IDENT-10 / REQ-IDENT-60):
 * the server-side `verifiedAt` field is not yet emitted at the time
 * this code lands. `isVerified` treats undefined as verified (legacy
 * compatibility) and `null` as unverified. Tests exercise both states
 * explicitly so the gate is provably wired when the server change
 * arrives.
 */

import { describe, it, expect } from 'vitest';
import { selectReplyIdentity, deliveryAliasForCc, localAliasesForCc, _internals_forTest } from './reply-identity';
import type { Email, Identity } from '../mail/types';

const { isVerified } = _internals_forTest;

// ── Fixtures ─────────────────────────────────────────────────────────

function makeIdentity(
  email: string,
  overrides: Partial<Identity> = {},
): Identity {
  return {
    id: `id-${email}`,
    name: email.split('@')[0]!,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
    ...overrides,
  };
}

function makeEmail(overrides: Partial<Email> = {}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    cc: null,
    subject: null,
    preview: '',
    receivedAt: '2026-05-10T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-1',
    ...overrides,
  };
}

// Common addresses reused across tests.
const ALICE = makeIdentity('alice@example.local');
const ALICE_WORK = makeIdentity('alice+work@example.local');
const BOB = makeIdentity('bob@example.local');
const DEFAULT_ID = ALICE; // most tests treat alice@ as the user's primary

// ── Helper: makeIdentity verifiedAt tri-state ────────────────────────

describe('isVerified (verification gate)', () => {
  it('treats a missing verifiedAt field as verified (legacy server)', () => {
    const id = makeIdentity('x@y.test'); // no verifiedAt key at all
    expect(isVerified(id)).toBe(true);
  });

  it('treats verifiedAt = null as not verified', () => {
    const id = makeIdentity('x@y.test', { verifiedAt: null });
    expect(isVerified(id)).toBe(false);
  });

  it('treats verifiedAt = ISO timestamp as verified', () => {
    const id = makeIdentity('x@y.test', { verifiedAt: '2026-01-01T00:00:00Z' });
    expect(isVerified(id)).toBe(true);
  });

  it('treats verifiedAt = undefined explicitly as verified', () => {
    const id: Identity = { ...makeIdentity('x@y.test'), verifiedAt: undefined };
    expect(isVerified(id)).toBe(true);
  });
});

// ── selectReplyIdentity ──────────────────────────────────────────────

describe('selectReplyIdentity — REQ-MAIL-12a algorithm', () => {
  it('returns the matching identity when To names a verified identity (step 2)', () => {
    // To contains alice+work@ which is a verified identity. X-Herold-Recipient
    // also points there (both agree), but the To scan fires first (step 2).
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice+work@example.local' }],
      'header:X-Herold-Recipient:asText': 'alice+work@example.local',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice+work@example.local');
  });

  it('falls back to the default identity when X-Herold-Recipient is for someone else and no other signal matches', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'bob@other.test' }],
      'header:X-Herold-Recipient:asText': 'bob@other.test',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice@example.local'); // default
  });

  it('skips unverified To entries and picks the next verified one (step 2 verification gate)', () => {
    // To lists alice+work@ (unverified) first, then alice@ (verified).
    // The step-2 To scan skips the unverified entry and returns the
    // next verified hit. X-Herold-Recipient also points to alice+work@
    // but step 3 is never reached.
    const unverifiedWork = makeIdentity('alice+work@example.local', {
      verifiedAt: null,
    });
    const verifiedAlice = makeIdentity('alice@example.local', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [
        { name: null, email: 'alice+work@example.local' },
        { name: null, email: 'alice@example.local' },
      ],
      'header:X-Herold-Recipient:asText': 'alice+work@example.local',
    });
    const got = selectReplyIdentity(
      parent,
      [verifiedAlice, unverifiedWork],
      verifiedAlice,
    );
    expect(got.email).toBe('alice@example.local');
  });

  it('scans To/Cc in order (step 2); first verified hit wins', () => {
    // parent.to lists alice@ first, then alice+work@. The first hit
    // in scan order wins regardless of X-Herold-Recipient.
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [
        { name: null, email: 'alice@example.local' },
        { name: null, email: 'alice+work@example.local' },
      ],
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice@example.local');
  });

  it('own-sent (parent.from matches identity) wins when genuinely outbound (no X-Herold-Recipient)', () => {
    // parent.from is alice@ (the user) and the message carries no
    // X-Herold-Recipient header — a genuinely outbound message the user
    // sent through herold. REQ-MAIL-30a: the own-sent identity wins.
    const parent = makeEmail({
      from: [{ name: null, email: 'alice@example.local' }],
      to: [{ name: null, email: 'someone@elsewhere.test' }],
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice@example.local');
  });

  it('does not treat a message as own-sent when X-Herold-Recipient is present, even if From matches an identity (re #166)', () => {
    // parent.from is alice@ (one of the user's own identities) but the
    // message carries X-Herold-Recipient: alice+work@ — herold injects
    // this header on every message it delivers (REQ-FLOW-34), regardless
    // of who sent it. This is a delivered message whose From coincides
    // with one of the user's registered identities (e.g. a self-addressed
    // test message, or mail from an address the user also uses to send),
    // not a message the user sent through herold. There is no To/Cc match
    // here (step 2 misses), so step 3's X-Herold-Recipient match decides.
    const parent = makeEmail({
      from: [{ name: null, email: 'alice@example.local' }],
      to: [{ name: null, email: 'someone@elsewhere.test' }],
      'header:X-Herold-Recipient:asText': 'alice+work@example.local',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice+work@example.local');
  });

  it('prefers the delivered-to identity over the coincidentally-matching From identity (re #166 concrete repro)', () => {
    // Reproduction from issuecomment-2070: the message was delivered To
    // hans@netzhansa.com (a registered identity) and happens to be From
    // hans.huebner@gmail.com (also a registered identity of the same
    // account, registered for outbound use). Since the message carries
    // X-Herold-Recipient (it was delivered, not sent by this user through
    // herold), step 1 must not fire; step 2's To scan picks the identity
    // the message was actually delivered to.
    const netzhansa = makeIdentity('hans@netzhansa.com');
    const gmail = makeIdentity('hans.huebner@gmail.com');
    const parent = makeEmail({
      from: [{ name: 'Hans Huebner', email: 'hans.huebner@gmail.com' }],
      to: [{ name: null, email: 'hans@netzhansa.com' }],
      'header:X-Herold-Recipient:asText': 'hans@netzhansa.com',
    });
    const got = selectReplyIdentity(parent, [netzhansa, gmail], netzhansa);
    expect(got.email).toBe('hans@netzhansa.com');
  });

  it('returns the default identity when nothing matches', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'unrelated@nowhere.test' }],
      cc: [{ name: null, email: 'also-unrelated@nowhere.test' }],
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice@example.local'); // default
  });

  // ── Extra coverage on edge cases ───────────────────────────────────

  it('To scan (step 2) wins when X-Herold-Recipient is present, even though From also matches an identity', () => {
    // parent.from matches alice@ but X-Herold-Recipient is present, so
    // this is a delivered message, not own-sent — step 1 does not apply
    // (re #166). Step 2's To scan fires instead and picks the first
    // verified hit in order.
    const parent = makeEmail({
      from: [{ name: null, email: 'alice@example.local' }],
      to: [
        { name: null, email: 'alice@example.local' },
        { name: null, email: 'alice+work@example.local' },
      ],
      'header:X-Herold-Recipient:asText': 'alice+work@example.local',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice@example.local');
  });

  it('matches X-Herold-Recipient case-insensitively (step 3, no To/Cc identity)', () => {
    // No identity in To/Cc so step 2 misses; step 3 fires and matches
    // X-Herold-Recipient case-insensitively.
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      'header:X-Herold-Recipient:asText': 'Alice+Work@Example.Local',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice+work@example.local');
  });

  it('prefers To identity over X-Herold-Recipient when they name different identities (forwarded-mail regression)', () => {
    // Regression for #166: a message forwarded by an external MX arrives
    // with X-Herold-Recipient pointing at the delivery identity (A) while
    // To carries the address the original sender used (B). Step 2 (To scan)
    // should fire before step 3 (X-Herold-Recipient), so B wins.
    const identityA = makeIdentity('hans@netzhansa.com');
    const identityB = makeIdentity('hans@huebner.org');
    const parent = makeEmail({
      from: [{ name: null, email: 'stranger@external.test' }],
      to: [{ name: null, email: 'hans@huebner.org' }],
      'header:X-Herold-Recipient:asText': 'hans@netzhansa.com',
    });
    const got = selectReplyIdentity(parent, [identityA, identityB], identityA);
    expect(got.email).toBe('hans@huebner.org');
  });

  it('falls back to X-Herold-Recipient identity when no identity appears in To or Cc (Bcc scenario)', () => {
    // The message was Bcc'd to an identity address: To/Cc carry only
    // third-party addresses, X-Herold-Recipient names the delivery
    // identity. Step 2 (To/Cc scan) misses; step 3 picks it up.
    const identityA = makeIdentity('hans@netzhansa.com');
    const parent = makeEmail({
      from: [{ name: null, email: 'stranger@external.test' }],
      to: [{ name: null, email: 'some-list@mailing.example' }],
      'header:X-Herold-Recipient:asText': 'hans@netzhansa.com',
    });
    const got = selectReplyIdentity(parent, [identityA], ALICE);
    expect(got.email).toBe('hans@netzhansa.com');
  });

  it('scans Cc when To has no match', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'someone@elsewhere.test' }],
      cc: [{ name: null, email: 'alice+work@example.local' }],
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice+work@example.local');
  });

  it('does not pick an unverified identity from the To/Cc scan', () => {
    const unverifiedWork = makeIdentity('alice+work@example.local', {
      verifiedAt: null,
    });
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice+work@example.local' }],
    });
    const got = selectReplyIdentity(parent, [unverifiedWork], DEFAULT_ID);
    // alice+work is unverified → falls through to default (alice@).
    expect(got.email).toBe('alice@example.local');
  });

  it('treats own-sent precedence as identity-presence — verification gate does not block step 1', () => {
    // parent.from is alice+work@ and that identity is unverified.
    // REQ-MAIL-12a step 1 says own-sent wins verbatim regardless of
    // verification — the user has provably sent from that address.
    const unverifiedWork = makeIdentity('alice+work@example.local', {
      verifiedAt: null,
    });
    const parent = makeEmail({
      from: [{ name: null, email: 'alice+work@example.local' }],
      to: [{ name: null, email: 'someone@elsewhere.test' }],
    });
    const got = selectReplyIdentity(
      parent,
      [unverifiedWork, BOB],
      DEFAULT_ID,
    );
    expect(got.email).toBe('alice+work@example.local');
  });

  it('returns default when identities list is empty', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice@example.local' }],
    });
    const got = selectReplyIdentity(parent, [], DEFAULT_ID);
    expect(got).toBe(DEFAULT_ID);
  });

  it('returns default when parent has no addresses at all', () => {
    const parent = makeEmail();
    const got = selectReplyIdentity(parent, [ALICE], DEFAULT_ID);
    expect(got).toBe(DEFAULT_ID);
  });

  // ── Step 3a: domain fallback (re #166, comment-2281 handback) ──────

  it('prefers a same-domain identity over the unrelated default when the delivered-to address is an unregistered role alias', () => {
    // Real headers from issuecomment-2281: delivered to
    // vorsitz@classic-computing.de (X-Herold-Recipient), To is a third
    // party, Cc carries info@classic-computing.de (also not a
    // registered identity). Neither is registered, but the account
    // holds presse@classic-computing.de — same domain, different local
    // part. That identity must win over the account's global default
    // (hans@huebner.org), which shares neither the address nor the
    // domain.
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const classicComputing = makeIdentity('presse@classic-computing.de');
    const parent = makeEmail({
      from: [{ name: 'Walter Bühler Erben', email: 'walter-buehler-erben@posteo.de' }],
      to: [{ name: 'dreams', email: 'dreams@the-retro-heaven.de' }],
      cc: [{ name: null, email: 'info@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, classicComputing], defaultId);
    expect(got.email).toBe('presse@classic-computing.de');
  });

  it('exact X-Herold-Recipient match (step 3) still wins over the domain fallback when the delivered-to address is itself registered', () => {
    // Same shape as the comment-2281 repro, but this time
    // vorsitz@classic-computing.de IS a registered identity — step 3
    // must return it verbatim rather than falling through to step 3a's
    // domain match (which would also find it, but for the wrong reason).
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const vorsitz = makeIdentity('vorsitz@classic-computing.de');
    const parent = makeEmail({
      from: [{ name: 'Walter Bühler Erben', email: 'walter-buehler-erben@posteo.de' }],
      to: [{ name: 'dreams', email: 'dreams@the-retro-heaven.de' }],
      cc: [{ name: null, email: 'info@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, vorsitz], defaultId);
    expect(got.email).toBe('vorsitz@classic-computing.de');
  });

  it('domain fallback via Cc when X-Herold-Recipient is absent', () => {
    // No X-Herold-Recipient at all, but Cc carries a role address on a
    // domain the account holds an identity for.
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const classicComputing = makeIdentity('presse@classic-computing.de');
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'dreams@the-retro-heaven.de' }],
      cc: [{ name: null, email: 'info@classic-computing.de' }],
    });
    const got = selectReplyIdentity(parent, [defaultId, classicComputing], defaultId);
    expect(got.email).toBe('presse@classic-computing.de');
  });

  it('does not apply the domain fallback when no identity shares the delivered-to domain', () => {
    // No identity anywhere shares classic-computing.de — the domain
    // fallback must not fire and the account default wins, as before.
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const other = makeIdentity('other@netzhansa.com');
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'dreams@the-retro-heaven.de' }],
      cc: [{ name: null, email: 'info@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, other], defaultId);
    expect(got.email).toBe('hans@huebner.org');
  });

  it('domain fallback skips an unverified same-domain identity', () => {
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const unverified = makeIdentity('presse@classic-computing.de', { verifiedAt: null });
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'dreams@the-retro-heaven.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, unverified], defaultId);
    expect(got.email).toBe('hans@huebner.org');
  });

  it('does not let a coincidental To/Cc domain match hijack the From when X-Herold-Recipient names an unrelated domain (fix-verifier deviation #1)', () => {
    // Multi-domain account: identities on huebner.org (default) and
    // the-retro-heaven.de (a decoy that happens to share a domain with
    // the To address). The message was genuinely delivered to
    // vorsitz@classic-computing.de (X-Herold-Recipient) — a domain
    // neither identity owns. Because X-Herold-Recipient IS present and
    // is the authoritative delivery signal, its failure to match must
    // NOT fall through to a coincidental To/Cc domain hit on the
    // unrelated the-retro-heaven.de identity; the account default wins.
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const decoy = makeIdentity('someone@the-retro-heaven.de');
    const parent = makeEmail({
      from: [{ name: 'Walter Bühler Erben', email: 'walter-buehler-erben@posteo.de' }],
      to: [{ name: 'dreams', email: 'dreams@the-retro-heaven.de' }],
      cc: [{ name: null, email: 'info@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, decoy], defaultId);
    expect(got.email).toBe('hans@huebner.org');
  });

  it('prefers the Delivered-To identity over the account default on a cross-domain alias forward (re #254)', () => {
    // #254 repro: the message was delivered via a cross-domain alias
    // forward (info@classic-computing.org -> vorsitz@classic-computing.de).
    // X-Herold-Recipient carries the literal RCPT TO herold accepted
    // (info@classic-computing.org) -- a domain no identity owns, so
    // steps 3/3a cannot resolve it. Delivered-To, an upstream-MTA header
    // untouched by herold, names the registered identity the mail was
    // actually delivered to and must win over the account default.
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const vorsitz = makeIdentity('vorsitz@classic-computing.de');
    const parent = makeEmail({
      from: [{ name: 't.orth', email: 't.orth@gmx.de' }],
      to: [{ name: null, email: 'info@classic-computing.org' }],
      'header:X-Herold-Recipient:asText': 'info@classic-computing.org',
      'header:X-Original-To:asText': 'info@classic-computing.org',
      'header:Delivered-To:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, vorsitz], defaultId);
    expect(got.email).toBe('vorsitz@classic-computing.de');
  });

  it('falls back to Delivered-To domain match when neither Delivered-To nor X-Herold-Recipient exactly match a registered identity', () => {
    // Delivered-To names an unregistered role alias on a domain the
    // account holds a different identity for (mirrors #166's step-3a
    // role-alias shape, but reached via step 3b since X-Herold-Recipient
    // is on an unrelated domain).
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const presse = makeIdentity('presse@classic-computing.de');
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'info@classic-computing.org' }],
      'header:X-Herold-Recipient:asText': 'info@classic-computing.org',
      'header:Delivered-To:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, presse], defaultId);
    expect(got.email).toBe('presse@classic-computing.de');
  });

  it('falls back to X-Original-To when Delivered-To is absent (step 3b, second source)', () => {
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const vorsitz = makeIdentity('vorsitz@classic-computing.de');
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'info@classic-computing.org' }],
      'header:X-Herold-Recipient:asText': 'info@classic-computing.org',
      'header:X-Original-To:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, vorsitz], defaultId);
    expect(got.email).toBe('vorsitz@classic-computing.de');
  });

  it('does not consult Delivered-To when X-Herold-Recipient already resolved via the domain fallback (step 3a still wins)', () => {
    // X-Herold-Recipient's domain matches presse@classic-computing.de
    // via step 3a; Delivered-To names a different, decoy identity that
    // must not override the already-resolved step-3a match.
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const presse = makeIdentity('presse@classic-computing.de');
    const decoy = makeIdentity('decoy@other.example');
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'dreams@the-retro-heaven.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
      'header:Delivered-To:asText': 'decoy@other.example',
    });
    const got = selectReplyIdentity(parent, [defaultId, presse, decoy], defaultId);
    expect(got.email).toBe('presse@classic-computing.de');
  });

  it('skips an unverified identity on the Delivered-To exact match (step 3b verification gate)', () => {
    const defaultId = makeIdentity('hans@huebner.org', { isDefault: true });
    const unverified = makeIdentity('vorsitz@classic-computing.de', { verifiedAt: null });
    const parent = makeEmail({
      from: [{ name: null, email: 'someone@elsewhere.test' }],
      to: [{ name: null, email: 'info@classic-computing.org' }],
      'header:X-Herold-Recipient:asText': 'info@classic-computing.org',
      'header:Delivered-To:asText': 'vorsitz@classic-computing.de',
    });
    const got = selectReplyIdentity(parent, [defaultId, unverified], defaultId);
    expect(got.email).toBe('hans@huebner.org');
  });
});

// ── REQ-MAIL-12a acceptance / contract test ─────────────────────────────
//
// This is #166's THIRD point-fix on selectReplyIdentity (e6234574 →
// 658c945b → 5930528f/this commit), each correcting one symptom with its
// own narrowly-scoped test. Per the project's cap-fix-on-fix rule, a
// third symptom on one flow owes a single pinned end-to-end test of the
// WHOLE step-ordering contract, not another symptom-only test — so a
// fourth divergence on this flow is caught here instead of requiring a
// fourth round-trip through the issue queue.
//
// One row per REQ-MAIL-12a step (1, 2, 3, 3a, 4) plus the three real-world
// repro cases this issue accumulated (issuecomment-2070, issuecomment-2238,
// issuecomment-2281) and the deviation the fix-verifier caught on the first
// pass at 3a (a coincidental To/Cc domain match must not override an
// authoritative-but-unmatched X-Herold-Recipient domain).
describe('selectReplyIdentity — REQ-MAIL-12a acceptance/contract test (pins the full step order, cap-fix-on-fix depth 3)', () => {
  const HANS_HUEBNER = makeIdentity('hans@huebner.org', { isDefault: true });
  const HANS_NETZHANSA = makeIdentity('hans@netzhansa.com');
  const HANS_GMAIL = makeIdentity('hans.huebner@gmail.com');
  const CLASSIC_PRESSE = makeIdentity('presse@classic-computing.de');
  const CLASSIC_VORSITZ = makeIdentity('vorsitz@classic-computing.de');
  const RETRO_DECOY = makeIdentity('someone@the-retro-heaven.de');

  type Case = {
    name: string;
    parent: Partial<Email>;
    identities: Identity[];
    defaultIdentity: Identity;
    expectedEmail: string;
  };

  const cases: Case[] = [
    {
      name: 'step 1 — own-sent: From matches an identity and no X-Herold-Recipient header (genuinely outbound)',
      parent: {
        from: [{ name: null, email: 'alice@example.local' }],
        to: [{ name: null, email: 'someone@elsewhere.test' }],
      },
      identities: [ALICE, ALICE_WORK],
      defaultIdentity: DEFAULT_ID,
      expectedEmail: 'alice@example.local',
    },
    {
      name: 'step 2 — To/Cc exact match wins over a present-but-different X-Herold-Recipient identity',
      parent: {
        from: [{ name: null, email: 'external@elsewhere.test' }],
        to: [{ name: null, email: 'alice+work@example.local' }],
        'header:X-Herold-Recipient:asText': 'alice@example.local',
      },
      identities: [ALICE, ALICE_WORK],
      defaultIdentity: DEFAULT_ID,
      expectedEmail: 'alice+work@example.local',
    },
    {
      name: 'step 3 — X-Herold-Recipient exact match wins when To/Cc have no identity (Bcc/list mail)',
      parent: {
        from: [{ name: null, email: 'stranger@external.test' }],
        to: [{ name: null, email: 'some-list@mailing.example' }],
        'header:X-Herold-Recipient:asText': 'hans@netzhansa.com',
      },
      identities: [HANS_NETZHANSA],
      defaultIdentity: HANS_HUEBNER,
      expectedEmail: 'hans@netzhansa.com',
    },
    {
      name: 'step 3a — domain fallback wins when X-Herold-Recipient matches no identity exactly but shares a domain with one',
      parent: {
        from: [{ name: null, email: 'walter-buehler-erben@posteo.de' }],
        to: [{ name: null, email: 'dreams@the-retro-heaven.de' }],
        cc: [{ name: null, email: 'info@classic-computing.de' }],
        'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
      },
      identities: [HANS_HUEBNER, CLASSIC_PRESSE],
      defaultIdentity: HANS_HUEBNER,
      expectedEmail: 'presse@classic-computing.de',
    },
    {
      name: 'step 4 — terminal fallback to the default identity when nothing matches at any step',
      parent: {
        from: [{ name: null, email: 'external@elsewhere.test' }],
        to: [{ name: null, email: 'unrelated@nowhere.test' }],
        cc: [{ name: null, email: 'also-unrelated@nowhere.test' }],
      },
      identities: [ALICE, ALICE_WORK],
      defaultIdentity: DEFAULT_ID,
      expectedEmail: 'alice@example.local',
    },
    {
      name: 'repro: issuecomment-2070 — delivered-to identity wins over a coincidentally-matching From identity',
      parent: {
        from: [{ name: 'Hans Huebner', email: 'hans.huebner@gmail.com' }],
        to: [{ name: null, email: 'hans@netzhansa.com' }],
        'header:X-Herold-Recipient:asText': 'hans@netzhansa.com',
      },
      identities: [HANS_NETZHANSA, HANS_GMAIL],
      defaultIdentity: HANS_NETZHANSA,
      expectedEmail: 'hans@netzhansa.com',
    },
    {
      name: 'repro: issuecomment-2238 — own-sent does not fire when X-Herold-Recipient is present, even if From matches an identity',
      parent: {
        from: [{ name: null, email: 'alice@example.local' }],
        to: [{ name: null, email: 'someone@elsewhere.test' }],
        'header:X-Herold-Recipient:asText': 'alice+work@example.local',
      },
      identities: [ALICE, ALICE_WORK],
      defaultIdentity: DEFAULT_ID,
      expectedEmail: 'alice+work@example.local',
    },
    {
      name: 'repro: issuecomment-2281 — delivered-to role/alias address (not a registered identity) resolves via same-domain fallback, not the unrelated global default',
      parent: {
        from: [{ name: 'Walter Bühler Erben', email: 'walter-buehler-erben@posteo.de' }],
        to: [{ name: 'dreams', email: 'dreams@the-retro-heaven.de' }],
        cc: [{ name: null, email: 'info@classic-computing.de' }],
        'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
      },
      identities: [HANS_HUEBNER, CLASSIC_PRESSE],
      defaultIdentity: HANS_HUEBNER,
      expectedEmail: 'presse@classic-computing.de',
    },
    {
      name: 'fix-verifier deviation #1 — an unmatched X-Herold-Recipient domain must not fall through to a coincidental To/Cc domain hit on an unrelated identity',
      parent: {
        from: [{ name: 'Walter Bühler Erben', email: 'walter-buehler-erben@posteo.de' }],
        to: [{ name: 'dreams', email: 'dreams@the-retro-heaven.de' }],
        cc: [{ name: null, email: 'info@classic-computing.de' }],
        'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
      },
      identities: [HANS_HUEBNER, RETRO_DECOY],
      defaultIdentity: HANS_HUEBNER,
      expectedEmail: 'hans@huebner.org',
    },
    {
      name: 'step 3b — Delivered-To wins when X-Herold-Recipient names a domain no identity owns (re #254)',
      parent: {
        from: [{ name: 't.orth', email: 't.orth@gmx.de' }],
        to: [{ name: null, email: 'info@classic-computing.org' }],
        'header:X-Herold-Recipient:asText': 'info@classic-computing.org',
        'header:X-Original-To:asText': 'info@classic-computing.org',
        'header:Delivered-To:asText': 'vorsitz@classic-computing.de',
      },
      identities: [HANS_HUEBNER, CLASSIC_VORSITZ],
      defaultIdentity: HANS_HUEBNER,
      expectedEmail: 'vorsitz@classic-computing.de',
    },
    {
      name: 'repro: issue #254 — cross-domain alias forward (info@classic-computing.org -> vorsitz@classic-computing.de) resolves via step 3b, not the unrelated global default',
      parent: {
        from: [{ name: 'Walter Bühler Erben', email: 'walter-buehler-erben@posteo.de' }],
        to: [{ name: null, email: 'info@classic-computing.org' }],
        'header:X-Herold-Recipient:asText': 'info@classic-computing.org',
        'header:X-Original-To:asText': 'info@classic-computing.org',
        'header:Delivered-To:asText': 'vorsitz@classic-computing.de',
      },
      identities: [HANS_HUEBNER, CLASSIC_VORSITZ],
      defaultIdentity: HANS_HUEBNER,
      expectedEmail: 'vorsitz@classic-computing.de',
    },
  ];

  it.each(cases)('$name', ({ parent, identities, defaultIdentity, expectedEmail }) => {
    const got = selectReplyIdentity(makeEmail(parent), identities, defaultIdentity);
    expect(got.email).toBe(expectedEmail);
  });
});

// ── localAliasesForCc ───────────────────────────────────────────────────

describe('localAliasesForCc', () => {
  // Fixture: alice@example.local is a registered identity;
  // alias@example.local is NOT (same domain).
  const VORSITZ = makeIdentity('vorsitz@classic-computing.de');

  it('returns the upstream-alias address from visible To when X-Herold-Recipient is a registered identity (primary fix)', () => {
    // Scenario: external MX resolved info@classic-computing.de →
    // vorsitz@classic-computing.de before herold saw the envelope.
    // X-Herold-Recipient = vorsitz@ (registered) — deliveryAliasForCc
    // correctly no-ops, but info@ must still be added to reply Cc.
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'info@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'vorsitz@classic-computing.de',
    });
    const aliases = localAliasesForCc(parent, [VORSITZ]);
    expect(aliases).toEqual(['info@classic-computing.de']);
  });

  it('returns the X-Herold-Recipient address when it is not a registered identity (herold-native alias path)', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alias@example.local' }],
      'header:X-Herold-Recipient:asText': 'alias@example.local',
    });
    const aliases = localAliasesForCc(parent, [ALICE, BOB]);
    expect(aliases).toEqual(['alias@example.local']);
  });

  it('returns empty when X-Herold-Recipient is a registered identity and To has no same-domain non-identity addresses', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice@example.local' }],
      'header:X-Herold-Recipient:asText': 'alice@example.local',
    });
    const aliases = localAliasesForCc(parent, [ALICE, BOB]);
    expect(aliases).toEqual([]);
  });

  it('returns empty when X-Herold-Recipient is absent and all To addresses are registered identities', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice@example.local' }],
    });
    const aliases = localAliasesForCc(parent, [ALICE]);
    expect(aliases).toEqual([]);
  });

  it('returns empty when X-Herold-Recipient is absent and To addresses are on an external domain', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'someone@external.test' }],
    });
    const aliases = localAliasesForCc(parent, [ALICE]);
    expect(aliases).toEqual([]);
  });

  it('matches To address domain case-insensitively against identity domains', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'Alias@Example.Local' }],
    });
    const aliases = localAliasesForCc(parent, [ALICE]);
    // Alice owns example.local; Alias@ is on that domain and not an identity.
    expect(aliases).toEqual(['alias@example.local']);
  });

  it('deduplicates when X-Herold-Recipient matches a To address (both sources overlap)', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alias@example.local' }],
      'header:X-Herold-Recipient:asText': 'alias@example.local',
    });
    // alias@ appears in both source 1 (X-Herold-Recipient) and source 2
    // (visible To on identity domain). Must not appear twice.
    const aliases = localAliasesForCc(parent, [ALICE]);
    expect(aliases).toEqual(['alias@example.local']);
  });

  it('collects multiple aliases from To and Cc', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'list@example.local' }],
      cc: [{ name: null, email: 'team@example.local' }],
    });
    const aliases = localAliasesForCc(parent, [ALICE]);
    expect(aliases).toEqual(['list@example.local', 'team@example.local']);
  });

  it('skips parent.to scan when ownMessage=true (own-sent reply: To goes to reply To, not Cc)', () => {
    // The user sent a message to info@classic-computing.de; on own-sent
    // reply-all, parent.to becomes the new reply's To. localAliasesForCc
    // must NOT add those addresses to Cc as well.
    const parent = makeEmail({
      from: [{ name: null, email: 'vorsitz@classic-computing.de' }],
      to: [{ name: null, email: 'info@classic-computing.de' }],
      cc: [{ name: null, email: 'team@classic-computing.de' }],
    });
    const aliases = localAliasesForCc(parent, [VORSITZ], /* ownMessage */ true);
    // parent.to is skipped; parent.cc alias is included.
    expect(aliases).toEqual(['team@classic-computing.de']);
  });

  it('still includes X-Herold-Recipient non-identity when ownMessage=true', () => {
    // Even for own-sent messages, X-Herold-Recipient (source 1) is checked.
    // In practice X-Herold-Recipient is absent on outbound (REQ-FLOW-35),
    // so this case tests the logic in isolation.
    const parent = makeEmail({
      from: [{ name: null, email: 'alice@example.local' }],
      to: [{ name: null, email: 'alice@example.local' }],
      'header:X-Herold-Recipient:asText': 'alias@example.local',
    });
    const aliases = localAliasesForCc(parent, [ALICE], /* ownMessage */ true);
    // parent.to is skipped (ownMessage); X-Herold-Recipient alias included.
    expect(aliases).toEqual(['alias@example.local']);
  });

  it('includes the delivery address (source 1) even when an UNVERIFIED Identity is registered for it (re #280)', () => {
    // Regression: a role address (info@classic-computing.de) that the user
    // started adding as a first-class Identity but never verified must not
    // be treated as "already covered" -- it is not one of the account's
    // configured identities (#146's own acceptance-criterion language), so
    // the delivery address must still be added to the reply Cc.
    const unverifiedInfo = makeIdentity('info@classic-computing.de', { verifiedAt: null });
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'vorsitz@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'info@classic-computing.de',
    });
    const aliases = localAliasesForCc(parent, [VORSITZ, unverifiedInfo]);
    expect(aliases).toEqual(['info@classic-computing.de']);
  });

  it('includes the delivery address (source 2, visible To scan) even when an UNVERIFIED Identity is registered for it (re #280)', () => {
    // Same regression via the visible-To/Cc domain-scan path (no
    // X-Herold-Recipient at all -- e.g. a message that predates REQ-FLOW-34
    // stamping). The address is directly visible in To and must still be
    // added to Cc despite the unverified Identity row.
    const unverifiedInfo = makeIdentity('info@classic-computing.de', { verifiedAt: null });
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'info@classic-computing.de' }],
    });
    const aliases = localAliasesForCc(parent, [VORSITZ, unverifiedInfo]);
    expect(aliases).toEqual(['info@classic-computing.de']);
  });

  it('still excludes the delivery address when the matching Identity is verified', () => {
    // Control: a VERIFIED Identity for the delivery address is genuinely
    // "configured" and correctly suppresses the Cc addition.
    const verifiedInfo = makeIdentity('info@classic-computing.de', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'info@classic-computing.de' }],
      'header:X-Herold-Recipient:asText': 'info@classic-computing.de',
    });
    const aliases = localAliasesForCc(parent, [VORSITZ, verifiedInfo]);
    expect(aliases).toEqual([]);
  });
});

// ── deliveryAliasForCc ──────────────────────────────────────────────────

describe('deliveryAliasForCc', () => {
  it('returns the delivery address when X-Herold-Recipient does not match any identity', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alias@example.local' }],
      'header:X-Herold-Recipient:asText': 'alias@example.local',
    });
    const alias = deliveryAliasForCc(parent, [ALICE, BOB]);
    expect(alias).toBe('alias@example.local');
  });

  it('returns null when X-Herold-Recipient matches a registered identity', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice@example.local' }],
      'header:X-Herold-Recipient:asText': 'alice@example.local',
    });
    const alias = deliveryAliasForCc(parent, [ALICE, BOB]);
    expect(alias).toBeNull();
  });

  it('returns null when X-Herold-Recipient is absent (outbound, REQ-FLOW-35)', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      to: [{ name: null, email: 'alice@example.local' }],
    });
    const alias = deliveryAliasForCc(parent, [ALICE, BOB]);
    expect(alias).toBeNull();
  });

  it('matches X-Herold-Recipient case-insensitively against registered identities', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      'header:X-Herold-Recipient:asText': 'Alice@Example.Local',
    });
    // Upper-cased header matches alice@example.local — no alias CC needed.
    const alias = deliveryAliasForCc(parent, [ALICE]);
    expect(alias).toBeNull();
  });

  it('returns the normalised (lower-cased) alias when no identity matches', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      'header:X-Herold-Recipient:asText': 'Alias@Example.Local',
    });
    const alias = deliveryAliasForCc(parent, [ALICE, BOB]);
    // readHeraldRecipient lower-cases and trims the value.
    expect(alias).toBe('alias@example.local');
  });
});
