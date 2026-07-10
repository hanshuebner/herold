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

  it('own-sent (parent.from matches identity) wins over X-Herold-Recipient (REQ-MAIL-30a precedence)', () => {
    // parent.from is alice@ (the user). X-Herold-Recipient is
    // alice+work@. REQ-MAIL-30a says the own-sent identity wins; the
    // X-Herold-Recipient match must not override.
    const parent = makeEmail({
      from: [{ name: null, email: 'alice@example.local' }],
      to: [{ name: null, email: 'someone@elsewhere.test' }],
      'header:X-Herold-Recipient:asText': 'alice+work@example.local',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice@example.local');
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

  it('owns precedence even when the user is also in To and X-Herold-Recipient points elsewhere', () => {
    // Defensive: own-sent is detected purely from parent.from; the
    // user appearing in To (Bcc-to-self pattern) doesn't change that.
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
