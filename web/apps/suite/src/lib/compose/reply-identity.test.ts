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
import { selectReplyIdentity, _internals_forTest } from './reply-identity';
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
  it('returns the matching identity when X-Herold-Recipient names a verified identity', () => {
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

  it('falls through when X-Herold-Recipient names an UNVERIFIED identity; next signal wins', () => {
    // alice+work@ is unverified (verifiedAt: null). The header points
    // at it, but the verification gate forbids selecting it. The
    // algorithm then scans To/Cc; alice@ is verified, so it wins.
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

  it('scans To/Cc in order when X-Herold-Recipient is absent; first hit wins', () => {
    // No X-Herold-Recipient. parent.to lists alice@ first, then
    // alice+work@. The first hit in scan order wins.
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

  it('matches X-Herold-Recipient case-insensitively', () => {
    const parent = makeEmail({
      from: [{ name: null, email: 'external@elsewhere.test' }],
      'header:X-Herold-Recipient:asText': 'Alice+Work@Example.Local',
    });
    const got = selectReplyIdentity(parent, [ALICE, ALICE_WORK], DEFAULT_ID);
    expect(got.email).toBe('alice+work@example.local');
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
