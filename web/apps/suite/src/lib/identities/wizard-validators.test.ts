/**
 * Pure-helper tests for the add-identity wizard validators
 * (REQ-SET-IDENT-30..33, REQ-IDENT-32).
 */

import { describe, it, expect } from 'vitest';
import {
  isValidEmail,
  domainOf,
  isHostedDomain,
  isValidPort,
  isValidCode,
  parseRetryAfter,
} from './wizard-validators';

describe('isValidEmail', () => {
  it('accepts an addr-spec with a hosted domain', () => {
    expect(isValidEmail('alice@example.local')).toBe(true);
  });

  it('accepts an addr-spec with a multi-label external domain', () => {
    expect(isValidEmail('user.name+tag@mail.example.com')).toBe(true);
  });

  it('rejects an empty string', () => {
    expect(isValidEmail('')).toBe(false);
  });

  it('rejects whitespace-only input', () => {
    expect(isValidEmail('   ')).toBe(false);
  });

  it('rejects strings with no @', () => {
    expect(isValidEmail('alice.example.local')).toBe(false);
  });

  it('rejects strings with no domain dot', () => {
    expect(isValidEmail('alice@localhost')).toBe(false);
  });

  it('rejects strings with whitespace inside', () => {
    expect(isValidEmail('alice @example.com')).toBe(false);
    expect(isValidEmail('alice@ example.com')).toBe(false);
    expect(isValidEmail('alice@example .com')).toBe(false);
  });
});

describe('domainOf', () => {
  it('returns the lower-cased domain for a valid address', () => {
    expect(domainOf('Alice@Example.Local')).toBe('example.local');
  });

  it('returns null for an invalid address', () => {
    expect(domainOf('not-an-email')).toBeNull();
    expect(domainOf('')).toBeNull();
  });
});

describe('isHostedDomain', () => {
  const HOSTED = new Set(['example.local', 'company.com']);

  it('returns true when the domain is in the hosted set', () => {
    expect(isHostedDomain('alice@example.local', HOSTED)).toBe(true);
  });

  it('is case-insensitive on the domain part', () => {
    expect(isHostedDomain('alice@EXAMPLE.LOCAL', HOSTED)).toBe(true);
  });

  it('returns false for external domains', () => {
    expect(isHostedDomain('alice@gmail.com', HOSTED)).toBe(false);
  });

  it('returns false for invalid addresses', () => {
    expect(isHostedDomain('not-an-email', HOSTED)).toBe(false);
  });

  it('falls back to false when the hosted set is empty (legacy server)', () => {
    expect(isHostedDomain('alice@example.local', new Set())).toBe(false);
  });
});

describe('isValidPort', () => {
  it('accepts 1, 25, 587, 465, 65535', () => {
    expect(isValidPort(1)).toBe(true);
    expect(isValidPort(25)).toBe(true);
    expect(isValidPort(587)).toBe(true);
    expect(isValidPort(465)).toBe(true);
    expect(isValidPort(65535)).toBe(true);
  });

  it('rejects 0', () => {
    expect(isValidPort(0)).toBe(false);
  });

  it('rejects negative numbers', () => {
    expect(isValidPort(-1)).toBe(false);
  });

  it('rejects values above 65535', () => {
    expect(isValidPort(65536)).toBe(false);
  });

  it('rejects non-integer values', () => {
    expect(isValidPort(587.5)).toBe(false);
  });

  it('rejects NaN / Infinity', () => {
    expect(isValidPort(Number.NaN)).toBe(false);
    expect(isValidPort(Number.POSITIVE_INFINITY)).toBe(false);
  });
});

describe('isValidCode', () => {
  it('accepts 6 decimal digits', () => {
    expect(isValidCode('123456')).toBe(true);
    expect(isValidCode('000000')).toBe(true);
    expect(isValidCode('999999')).toBe(true);
  });

  it('preserves leading zeros', () => {
    expect(isValidCode('007007')).toBe(true);
  });

  it('rejects fewer than 6 digits', () => {
    expect(isValidCode('12345')).toBe(false);
  });

  it('rejects more than 6 digits', () => {
    expect(isValidCode('1234567')).toBe(false);
  });

  it('rejects non-ASCII / non-digit characters', () => {
    expect(isValidCode('12345a')).toBe(false);
    expect(isValidCode('12345 ')).toBe(false);
    expect(isValidCode('12345٧')).toBe(false); // Arabic-Indic digit 7
  });

  it('rejects empty', () => {
    expect(isValidCode('')).toBe(false);
  });
});

describe('parseRetryAfter', () => {
  it('parses integer-seconds form', () => {
    expect(parseRetryAfter('60', 0)).toBe(60);
    expect(parseRetryAfter('0', 0)).toBe(0);
  });

  it('parses HTTP-date form, returning seconds until target', () => {
    const now = Date.parse('2026-05-11T12:00:00Z');
    const target = 'Mon, 11 May 2026 12:00:30 GMT';
    expect(parseRetryAfter(target, now)).toBe(30);
  });

  it('returns 0 when the HTTP-date is already past', () => {
    const now = Date.parse('2026-05-11T12:00:30Z');
    const past = 'Mon, 11 May 2026 12:00:00 GMT';
    expect(parseRetryAfter(past, now)).toBe(0);
  });

  it('returns null on missing / blank header', () => {
    expect(parseRetryAfter(null, 0)).toBeNull();
    expect(parseRetryAfter('', 0)).toBeNull();
    expect(parseRetryAfter('   ', 0)).toBeNull();
  });

  it('returns null on garbage input', () => {
    expect(parseRetryAfter('soon', 0)).toBeNull();
    expect(parseRetryAfter('-5', 0)).toBeNull();
  });
});
