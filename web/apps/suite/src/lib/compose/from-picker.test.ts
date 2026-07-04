/**
 * Unit tests for the compose From-picker helpers (REQ-MAIL-12).
 */

import { describe, it, expect } from 'vitest';
import type { Identity } from '../mail/types';
import type { SubmissionSummary } from '../identities/identity-status';
import {
  composeFromSort,
  isFromSelectable,
  composeFromGating,
  shouldShowFromPicker,
} from './from-picker';

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

const noSub = (_id: string): SubmissionSummary | null => null;

describe('composeFromGating', () => {
  it('returns no-identity when called with null', () => {
    expect(composeFromGating(null, null)).toEqual({
      allowed: false,
      reason: 'no-identity',
    });
  });

  it('allows a verified identity with no submission record (local-outbound)', () => {
    const id = makeIdentity('1', 'a@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    expect(composeFromGating(id, null)).toEqual({ allowed: true, reason: null });
  });

  it('allows a verified identity with configured+ok external submission', () => {
    const id = makeIdentity('1', 'a@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const sub: SubmissionSummary = { configured: true, state: 'ok' };
    expect(composeFromGating(id, sub)).toEqual({ allowed: true, reason: null });
  });

  it('blocks an unverified identity', () => {
    const id = makeIdentity('1', 'a@x.test', { verifiedAt: null });
    expect(composeFromGating(id, null)).toEqual({
      allowed: false,
      reason: 'unverified',
    });
  });

  it('blocks a verifying identity (token live, not yet confirmed)', () => {
    const id = makeIdentity('1', 'a@x.test', {
      verifiedAt: null,
      verificationPendingSince: '2026-05-01T00:00:00Z',
    });
    expect(composeFromGating(id, null)).toEqual({
      allowed: false,
      reason: 'verifying',
    });
  });

  it('blocks a verified identity with auth-failed external submission (external-broken)', () => {
    const id = makeIdentity('1', 'a@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const sub: SubmissionSummary = { configured: true, state: 'auth-failed' };
    expect(composeFromGating(id, sub)).toEqual({
      allowed: false,
      reason: 'external-broken',
    });
  });

  it('blocks a verified identity with unreachable external submission (external-broken)', () => {
    const id = makeIdentity('1', 'a@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const sub: SubmissionSummary = { configured: true, state: 'unreachable' };
    expect(composeFromGating(id, sub)).toEqual({
      allowed: false,
      reason: 'external-broken',
    });
  });

  it('blocks a verified identity with unconfigured submission record (external-setup-needed)', () => {
    const id = makeIdentity('1', 'a@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const sub: SubmissionSummary = { configured: false, state: null };
    expect(composeFromGating(id, sub)).toEqual({
      allowed: false,
      reason: 'external-setup-needed',
    });
  });

  it('allows a hosted identity (domainAuthoritative: true) even when configured: false (re #107)', () => {
    // A locally-hosted identity has domain_authoritative: true from the
    // server's GET /api/v1/identities/{id}/submission response. It sends
    // via herold's outbound queue and must not be gated by the
    // external-SMTP check.
    const id = makeIdentity('1', 'a@example.local', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const sub: SubmissionSummary = { configured: false, state: null, domainAuthoritative: true };
    expect(composeFromGating(id, sub)).toEqual({ allowed: true, reason: null });
  });
});

describe('isFromSelectable', () => {
  it('mirrors composeFromGating.allowed', () => {
    const verified = makeIdentity('1', 'a@x.test', {
      verifiedAt: '2026-01-01T00:00:00Z',
    });
    const unverified = makeIdentity('2', 'b@x.test', { verifiedAt: null });
    expect(isFromSelectable(verified, null)).toBe(true);
    expect(isFromSelectable(unverified, null)).toBe(false);
  });
});

describe('composeFromSort', () => {
  // Build a representative spread of identities.
  const def = makeIdentity('1', 'default@x.test', {
    verifiedAt: '2026-01-01T00:00:00Z',
    isDefault: true,
  });
  const verifiedA = makeIdentity('2', 'aaa@x.test', {
    verifiedAt: '2026-02-01T00:00:00Z',
  });
  const verifiedB = makeIdentity('3', 'bbb@x.test', {
    verifiedAt: '2026-02-01T00:00:00Z',
  });
  const verifying1 = makeIdentity('4', 'pending1@x.test', {
    verifiedAt: null,
    verificationPendingSince: '2026-05-01T00:00:00Z',
  });
  const verifying2 = makeIdentity('5', 'pending2@x.test', {
    verifiedAt: null,
    verificationPendingSince: '2026-05-02T00:00:00Z',
  });
  const unverified = makeIdentity('6', 'unverified@x.test', {
    verifiedAt: null,
  });
  const externalBroken = makeIdentity('7', 'ext@x.test', {
    verifiedAt: '2026-03-01T00:00:00Z',
  });
  const subMap: Record<string, SubmissionSummary | null> = {
    '7': { configured: true, state: 'auth-failed' },
  };
  const resolver = (id: string) => subMap[id] ?? null;

  it('places the default identity first', () => {
    const out = composeFromSort([verifiedA, unverified, def, verifying1], resolver);
    expect(out[0]).toBe(def);
  });

  it('sorts other verified identities alphabetically by email after default', () => {
    const out = composeFromSort([verifiedB, verifiedA, def], resolver);
    expect(out.map((i) => i.email)).toEqual([
      'default@x.test',
      'aaa@x.test',
      'bbb@x.test',
    ]);
  });

  it('places verifying identities after verified, id descending', () => {
    const out = composeFromSort([verifying1, verifying2, verifiedA], resolver);
    expect(out.map((i) => i.id)).toEqual(['2', '5', '4']);
  });

  it('places unverified identities after verifying', () => {
    const out = composeFromSort(
      [unverified, verifying1, verifiedA, def],
      resolver,
    );
    expect(out.map((i) => i.id)).toEqual(['1', '2', '4', '6']);
  });

  it('places external-without-submission identities last', () => {
    const out = composeFromSort(
      [externalBroken, unverified, verifying1, verifiedA, def],
      resolver,
    );
    // 1=def, 2=verifiedA, 4=verifying1, 6=unverified, 7=externalBroken
    expect(out.map((i) => i.id)).toEqual(['1', '2', '4', '6', '7']);
  });

  it('does not mutate the input array', () => {
    const input = [unverified, verifiedA, def];
    composeFromSort(input, resolver);
    expect(input.map((i) => i.id)).toEqual(['6', '2', '1']);
  });
});

describe('shouldShowFromPicker', () => {
  const a = makeIdentity('1', 'a@x.test', {
    verifiedAt: '2026-01-01T00:00:00Z',
  });
  const b = makeIdentity('2', 'b@x.test', { verifiedAt: null });

  it('returns false when the list is empty', () => {
    expect(shouldShowFromPicker([], noSub)).toBe(false);
  });

  it('returns false with a single identity', () => {
    expect(shouldShowFromPicker([a], noSub)).toBe(false);
  });

  it('returns true with two identities even when one is unverified', () => {
    expect(shouldShowFromPicker([a, b], noSub)).toBe(true);
  });
});
