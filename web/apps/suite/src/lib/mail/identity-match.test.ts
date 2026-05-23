/**
 * Unit tests for the identity-match helpers.
 *
 * These are pure functions with no Svelte reactivity; plain vitest is
 * sufficient — no @testing-library/svelte needed.
 */

import { describe, it, expect } from 'vitest';
import { buildSelfEmailSet, isFromSelf } from './identity-match';
import type { Identity } from './types';

// ── buildSelfEmailSet ─────────────────────────────────────────────────────────

function makeIdentity(email: string, name = 'Test User'): Identity {
  return {
    id: 'i1',
    name,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
  };
}

describe('buildSelfEmailSet', () => {
  it('returns an empty set for an empty iterable', () => {
    expect(buildSelfEmailSet([])).toEqual(new Set());
  });

  it('lowercases addresses', () => {
    const set = buildSelfEmailSet([makeIdentity('Alice@Example.COM')]);
    expect(set.has('alice@example.com')).toBe(true);
    expect(set.has('Alice@Example.COM')).toBe(false);
  });

  it('trims whitespace around the email', () => {
    const set = buildSelfEmailSet([makeIdentity('  alice@example.com  ')]);
    expect(set.has('alice@example.com')).toBe(true);
  });

  it('collects all identities', () => {
    const identities = [
      makeIdentity('alice@example.com'),
      makeIdentity('alias@example.org'),
    ];
    const set = buildSelfEmailSet(identities);
    expect(set.size).toBe(2);
    expect(set.has('alice@example.com')).toBe(true);
    expect(set.has('alias@example.org')).toBe(true);
  });

  it('skips identities whose email normalises to empty string', () => {
    const set = buildSelfEmailSet([makeIdentity('   ')]);
    expect(set.size).toBe(0);
  });
});

// ── isFromSelf ────────────────────────────────────────────────────────────────

describe('isFromSelf', () => {
  const self = new Set(['me@example.test', 'alias@example.org']);

  it('returns true when the first From address is in the self set', () => {
    expect(isFromSelf({ from: [{ name: 'Me', email: 'me@example.test' }] }, self)).toBe(true);
  });

  it('is case-insensitive (uppercase from address)', () => {
    expect(isFromSelf({ from: [{ name: 'Me', email: 'ME@EXAMPLE.TEST' }] }, self)).toBe(true);
  });

  it('trims whitespace before matching', () => {
    expect(isFromSelf({ from: [{ name: null, email: ' me@example.test ' }] }, self)).toBe(true);
  });

  it('returns false when the first From is not in the self set', () => {
    expect(isFromSelf({ from: [{ name: 'Other', email: 'other@example.test' }] }, self)).toBe(false);
  });

  it('returns false when from is null', () => {
    expect(isFromSelf({ from: null }, self)).toBe(false);
  });

  it('returns false when from is undefined', () => {
    expect(isFromSelf({}, self)).toBe(false);
  });

  it('returns false when the from array is empty', () => {
    expect(isFromSelf({ from: [] }, self)).toBe(false);
  });

  it('returns false when selfEmails is empty', () => {
    expect(isFromSelf({ from: [{ name: null, email: 'me@example.test' }] }, new Set())).toBe(false);
  });

  it('uses only the first From address (multiple From entries)', () => {
    // RFC 5322 allows multiple From entries; we check only the first.
    const multi = {
      from: [
        { name: null, email: 'other@example.test' },
        { name: null, email: 'me@example.test' },
      ],
    };
    // First From is not self; second is — result must be false.
    expect(isFromSelf(multi, self)).toBe(false);
  });

  it('returns false when the From email is an empty string', () => {
    expect(isFromSelf({ from: [{ name: 'Name Only', email: '' }] }, self)).toBe(false);
  });
});
