/**
 * Unit tests for the "Not spam" rule-building helpers (issue #382).
 */

import { describe, it, expect } from 'vitest';
import { senderDomain, buildNeverSpamRule } from './not-spam';

describe('senderDomain', () => {
  it('extracts the domain, lower-cased', () => {
    expect(senderDomain('notify@AccountProtection.Microsoft.Com')).toBe(
      'accountprotection.microsoft.com',
    );
  });

  it('returns "" for an address with no @', () => {
    expect(senderDomain('not-an-address')).toBe('');
  });

  it('returns "" for an address with nothing after @', () => {
    expect(senderDomain('user@')).toBe('');
  });
});

describe('buildNeverSpamRule', () => {
  it('builds a from-condition rule scoped to the exact address', () => {
    const rule = buildNeverSpamRule(
      'address',
      'notify@accountprotection.microsoft.com',
      3,
      'Allow notify@accountprotection.microsoft.com',
    );
    expect(rule).toEqual({
      name: 'Allow notify@accountprotection.microsoft.com',
      enabled: true,
      order: 3,
      conditions: [{ field: 'from', op: 'equals', value: 'notify@accountprotection.microsoft.com' }],
      actions: [{ kind: 'never-spam' }],
    });
  });

  it('builds a from-domain-condition rule scoped to the domain', () => {
    const rule = buildNeverSpamRule(
      'domain',
      'notify@accountprotection.microsoft.com',
      0,
      'Allow accountprotection.microsoft.com',
    );
    expect(rule).toEqual({
      name: 'Allow accountprotection.microsoft.com',
      enabled: true,
      order: 0,
      conditions: [{ field: 'from-domain', op: 'equals', value: 'accountprotection.microsoft.com' }],
      actions: [{ kind: 'never-spam' }],
    });
  });

  it('lower-cases and trims the address scope value', () => {
    const rule = buildNeverSpamRule('address', '  Notify@Example.Com  ', 0, 'x');
    expect(rule?.conditions[0]?.value).toBe('Notify@Example.Com');
  });

  it('returns null for a domain scope when the address has no domain', () => {
    expect(buildNeverSpamRule('domain', 'not-an-address', 0, 'x')).toBeNull();
  });

  it('returns null for an address scope when the address is empty', () => {
    expect(buildNeverSpamRule('address', '   ', 0, 'x')).toBeNull();
  });
});
