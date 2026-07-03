/**
 * Unit tests for the identity-list chip / sort / default-resolution
 * helpers (REQ-SET-IDENT-02, REQ-SET-IDENT-03, REQ-SET-IDENT-04,
 * REQ-SET-IDENT-08).
 */

import { describe, it, expect } from 'vitest';
import type { Identity } from '../mail/types';
import {
  identityStatus,
  canBeDefault,
  isExternalWithoutSubmission,
  sortIdentities,
  resolveDefault,
  type SubmissionSummary,
} from './identity-status';

function makeIdentity(
  id: string,
  email: string,
  overrides: Partial<Identity> = {},
): Identity {
  return {
    id,
    name: '',
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    ...overrides,
  };
}

describe('identityStatus', () => {
  it('returns verified when verifiedAt is an ISO timestamp', () => {
    const id = makeIdentity('1', 'a@x.test', { verifiedAt: '2026-05-01T00:00:00Z' });
    expect(identityStatus(id)).toBe('verified');
  });

  it('returns verifying when verifiedAt is null and verificationPendingSince is set', () => {
    const id = makeIdentity('2', 'b@x.test', {
      verifiedAt: null,
      verificationPendingSince: '2026-05-09T00:00:00Z',
    });
    expect(identityStatus(id)).toBe('verifying');
  });

  it('returns unverified when verifiedAt is null and verificationPendingSince is null', () => {
    const id = makeIdentity('3', 'c@x.test', {
      verifiedAt: null,
      verificationPendingSince: null,
    });
    expect(identityStatus(id)).toBe('unverified');
  });

  it('returns unverified when verifiedAt is null and verificationPendingSince is absent', () => {
    const id = makeIdentity('4', 'd@x.test', { verifiedAt: null });
    expect(identityStatus(id)).toBe('unverified');
  });

  it('treats a legacy server (both fields absent) as verified', () => {
    // Server has not yet shipped the extension properties at all.
    const id = makeIdentity('5', 'e@x.test');
    expect(identityStatus(id)).toBe('verified');
  });
});

describe('canBeDefault', () => {
  it('is true only for verified identities', () => {
    expect(canBeDefault(makeIdentity('1', 'a@x.test', { verifiedAt: '2026-01-01T00:00:00Z' }))).toBe(true);
    expect(
      canBeDefault(makeIdentity('2', 'b@x.test', { verifiedAt: null, verificationPendingSince: '2026-01-01T00:00:00Z' })),
    ).toBe(false);
    expect(
      canBeDefault(makeIdentity('3', 'c@x.test', { verifiedAt: null })),
    ).toBe(false);
  });
});

describe('isExternalWithoutSubmission', () => {
  const verified = makeIdentity('v', 'v@x.test', { verifiedAt: '2026-01-01T00:00:00Z' });
  const unverified = makeIdentity('u', 'u@x.test', { verifiedAt: null });

  it('treats unverified rows as disabled regardless of submission state', () => {
    expect(isExternalWithoutSubmission(unverified, null)).toBe(true);
    expect(isExternalWithoutSubmission(unverified, { configured: true, state: 'ok' })).toBe(true);
  });

  it('returns false when verified and no submission record exists (local outbound)', () => {
    expect(isExternalWithoutSubmission(verified, null)).toBe(false);
  });

  it('returns false when verified and submission is configured + ok', () => {
    const sub: SubmissionSummary = { configured: true, state: 'ok' };
    expect(isExternalWithoutSubmission(verified, sub)).toBe(false);
  });

  it('returns true when verified but submission is configured + auth-failed', () => {
    const sub: SubmissionSummary = { configured: true, state: 'auth-failed' };
    expect(isExternalWithoutSubmission(verified, sub)).toBe(true);
  });

  it('returns true when verified but submission record exists with configured: false', () => {
    const sub: SubmissionSummary = { configured: false, state: null };
    expect(isExternalWithoutSubmission(verified, sub)).toBe(true);
  });

  it('returns false when domainAuthoritative is true even if configured: false (hosted identity)', () => {
    // This is the bug from #107: a locally-hosted identity has
    // domain_authoritative: true from the server. The hosted identity
    // routes through herold's outbound queue — external SMTP is
    // irrelevant. The row must NOT be disabled.
    const sub: SubmissionSummary = { configured: false, state: null, domainAuthoritative: true };
    expect(isExternalWithoutSubmission(verified, sub)).toBe(false);
  });

  it('returns false when domainAuthoritative is true even if no submission record exists', () => {
    // domainAuthoritative on an existing record and sub===null (no record)
    // both mean "local outbound queue"; both should be allowed.
    const sub: SubmissionSummary = { configured: true, state: null, domainAuthoritative: true };
    expect(isExternalWithoutSubmission(verified, sub)).toBe(false);
  });
});

describe('sortIdentities', () => {
  const def = makeIdentity('1', 'default@x.test', {
    verifiedAt: '2026-01-01T00:00:00Z',
    isDefault: true,
  });
  const verA = makeIdentity('2', 'a@x.test', { verifiedAt: '2026-02-01T00:00:00Z' });
  const verB = makeIdentity('3', 'b@x.test', { verifiedAt: '2026-02-01T00:00:00Z' });
  const pending1 = makeIdentity('4', 'p1@x.test', {
    verifiedAt: null,
    verificationPendingSince: '2026-05-01T00:00:00Z',
  });
  const pending2 = makeIdentity('5', 'p2@x.test', {
    verifiedAt: null,
    verificationPendingSince: '2026-05-02T00:00:00Z',
  });
  const unv = makeIdentity('6', 'u@x.test', { verifiedAt: null });

  it('does not special-case the default identity — it sorts within the verified group by email', () => {
    // default@x.test sorts after a@x.test and b@x.test alphabetically
    // even though it carries isDefault: true.
    const out = sortIdentities([verB, verA, def]);
    expect(out.map((i) => i.email)).toEqual(['a@x.test', 'b@x.test', 'default@x.test']);
  });

  it('keeps the default identity list position stable regardless of isDefault', () => {
    const withDefault = sortIdentities([verA, verB, def]);
    const notDefault = sortIdentities([
      verA,
      verB,
      makeIdentity('1', 'default@x.test', { verifiedAt: '2026-01-01T00:00:00Z' }),
    ]);
    expect(withDefault.map((i) => i.email)).toEqual(notDefault.map((i) => i.email));
  });

  it('places verified identities alphabetically by email', () => {
    const out = sortIdentities([verB, verA]);
    expect(out.map((i) => i.email)).toEqual(['a@x.test', 'b@x.test']);
  });

  it('places pending identities after verified, sorted id descending', () => {
    const out = sortIdentities([pending1, pending2, verA]);
    // verA verified first; then pending2 (id=5) before pending1 (id=4) by descending id.
    expect(out.map((i) => i.id)).toEqual(['2', '5', '4']);
  });

  it('places unverified identities last', () => {
    const out = sortIdentities([unv, verA, pending1, def]);
    // verified group (def 'default@x.test', verA 'a@x.test') sorts by
    // email -> a@ then default@; then pending; then unverified.
    expect(out.map((i) => i.id)).toEqual(['2', '1', '4', '6']);
  });

  it('does not mutate the input array', () => {
    const input = [unv, verA, def];
    sortIdentities(input);
    expect(input.map((i) => i.id)).toEqual(['6', '2', '1']);
  });
});

describe('resolveDefault', () => {
  it('returns the identity with isDefault: true when present', () => {
    const def = makeIdentity('1', 'd@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
      isDefault: true,
    });
    const other = makeIdentity('2', 'o@x.test', { verifiedAt: '2026-01-01T00:00:00Z' });
    expect(resolveDefault([other, def])).toBe(def);
  });

  it('falls back to the first verified identity in stable order', () => {
    const a = makeIdentity('1', 'a@x.test', { verifiedAt: '2026-01-01T00:00:00Z' });
    const b = makeIdentity('2', 'b@x.test', { verifiedAt: '2026-01-01T00:00:00Z' });
    expect(resolveDefault([b, a])).toBe(a); // alphabetical by email
  });

  it('returns null when no identity is verified', () => {
    const unv = makeIdentity('1', 'u@x.test', { verifiedAt: null });
    expect(resolveDefault([unv])).toBeNull();
  });
});
