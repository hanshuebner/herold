/**
 * Tests for the Contacts client-side store.
 *
 * Section 1: contacts.filter() with only JMAP Contacts (existing behaviour).
 * Section 2: mergeFilter() — the unified merge helper covering:
 *   - Empty query returns contacts first, then seen entries.
 *   - Dedup by email: contact wins, seen entry suppressed.
 *   - Sort tiers: name-prefix > email-local-part-prefix > substring.
 *   - Within tier: seen entries by lastUsedAt desc, contacts alphabetically.
 *   - Cap at limit.
 *
 * The actual JMAP load() path is integration territory and tested live.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { contacts, _internals_forTest, type ContactSuggestion } from './store.svelte';
import type { SeenAddress } from './seen-addresses.svelte';

const { mergeFilter } = _internals_forTest;

function seed(rows: ContactSuggestion[]): void {
  // Force-set the suggestions by reaching into the singleton; this is
  // safe in tests because the store has no other consumers running.
  (contacts as unknown as { suggestions: ContactSuggestion[] }).suggestions = rows;
}

beforeEach(() => seed([]));

// ── Section 1: contacts.filter (legacy surface) ───────────────────────────

describe('contacts.filter (contacts-only path)', () => {
  const ALICE = { id: '1', name: 'Alice Liddell', email: 'alice@x.test' };
  const BOB = { id: '2', name: 'Bob Smith', email: 'bob@y.test' };
  const CATH = { id: '3', name: 'Catherine Brown', email: 'catherine@z.test' };
  const ALL = [ALICE, BOB, CATH];

  it('returns input slice when query is empty', () => {
    seed(ALL);
    // With no seen entries, filter delegates to mergeFilter which returns contacts.
    expect(contacts.filter('', 10)).toEqual(ALL);
  });

  it('filters by name substring case-insensitively', () => {
    seed(ALL);
    expect(contacts.filter('aLi')).toEqual([ALICE]);
  });

  it('filters by email substring', () => {
    seed(ALL);
    expect(contacts.filter('y.test')).toEqual([BOB]);
  });

  it('caps at limit', () => {
    seed(ALL);
    expect(contacts.filter('', 2)).toEqual([ALICE, BOB]);
  });

  it('returns empty when nothing matches', () => {
    seed(ALL);
    expect(contacts.filter('nobody-here')).toEqual([]);
  });

  it('returns empty when the store has no contacts loaded', () => {
    seed([]);
    expect(contacts.filter('anything')).toEqual([]);
  });
});

// ── Section 2: mergeFilter ────────────────────────────────────────────────

const C_ALICE: ContactSuggestion = { id: 'c1', name: 'Alice Liddell', email: 'alice@x.test' };
const C_BOB: ContactSuggestion = { id: 'c2', name: 'Bob Smith', email: 'bob@y.test' };

const SA_CAROL: SeenAddress = {
  id: 'sa1',
  email: 'carol@z.test',
  displayName: 'Carol',
  firstSeenAt: '2026-01-01T00:00:00Z',
  lastUsedAt: '2026-04-10T12:00:00Z',
  sendCount: 3,
  receivedCount: 1,
};

const SA_DAN: SeenAddress = {
  id: 'sa2',
  email: 'dan@w.test',
  displayName: 'Dan',
  firstSeenAt: '2026-02-01T00:00:00Z',
  lastUsedAt: '2026-04-01T08:00:00Z',
  sendCount: 1,
  receivedCount: 0,
};

// A seen entry whose email matches an existing contact (should be suppressed).
const SA_ALICE_DUP: SeenAddress = {
  id: 'sa3',
  email: 'alice@x.test', // same as C_ALICE
  displayName: 'Alice (seen)',
  firstSeenAt: '2026-03-01T00:00:00Z',
  lastUsedAt: '2026-04-15T10:00:00Z',
  sendCount: 2,
  receivedCount: 0,
};

describe('mergeFilter: empty query', () => {
  it('returns contacts first, then seen entries', () => {
    const result = mergeFilter([C_ALICE, C_BOB], [SA_CAROL, SA_DAN], '', 8);
    expect(result[0]!.id).toBe('c1'); // Alice (contact)
    expect(result[1]!.id).toBe('c2'); // Bob (contact)
    expect(result[2]!.id).toBe('sa:sa1'); // Carol (seen)
    expect(result[3]!.id).toBe('sa:sa2'); // Dan (seen)
  });

  it('deduplicates by email: contact wins, seen entry suppressed', () => {
    const result = mergeFilter([C_ALICE], [SA_ALICE_DUP, SA_CAROL], '', 8);
    // Only one Alice (the contact)
    const alices = result.filter((r) => r.email === 'alice@x.test');
    expect(alices).toHaveLength(1);
    expect(alices[0]!.id).toBe('c1');
  });

  it('caps at limit', () => {
    const result = mergeFilter([C_ALICE, C_BOB], [SA_CAROL, SA_DAN], '', 2);
    expect(result).toHaveLength(2);
  });
});

describe('mergeFilter: with query', () => {
  it('name-prefix match ranks tier 0', () => {
    // "ali" matches start of "Alice Liddell" (tier 0) but is also substring
    // of carol's displayName nowhere — just checking Alice comes first.
    const result = mergeFilter([C_ALICE, C_BOB], [SA_CAROL], 'ali');
    expect(result[0]!.id).toBe('c1');
  });

  it('email-local-part prefix match ranks tier 1', () => {
    // "car" matches start of "carol@z.test" local-part (tier 1).
    // Carol has no name-prefix match. Alice and Bob don't match at all.
    const result = mergeFilter([C_ALICE, C_BOB], [SA_CAROL, SA_DAN], 'car');
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe('sa:sa1');
  });

  it('substring match ranks tier 2', () => {
    // "liddell" is a substring of Alice's name (not a prefix of local-part or name).
    const result = mergeFilter([C_ALICE, C_BOB], [], 'liddell');
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe('c1');
  });

  it('within same tier: seen entries sorted by lastUsedAt desc', () => {
    // Both Carol (lastUsedAt 2026-04-10) and Dan (lastUsedAt 2026-04-01) match
    // "test" as substring. Carol is more recent so should come first within tier 2.
    const result = mergeFilter([], [SA_CAROL, SA_DAN], 'test');
    expect(result[0]!.id).toBe('sa:sa1'); // Carol (more recent)
    expect(result[1]!.id).toBe('sa:sa2'); // Dan
  });

  it('within same tier: contact entries sorted alphabetically', () => {
    // "test" matches both Alice and Bob by email substring. Both are contacts;
    // should appear alphabetically by name: Alice before Bob.
    const result = mergeFilter([C_ALICE, C_BOB], [], 'test');
    expect(result[0]!.id).toBe('c1'); // Alice
    expect(result[1]!.id).toBe('c2'); // Bob
  });

  it('within same tier: contacts before seen entries', () => {
    // Both Alice (contact) and Carol (seen) match at tier 2 ("test").
    // Contacts precede seen entries within the same tier.
    const result = mergeFilter([C_ALICE], [SA_CAROL], 'test');
    expect(result[0]!.id).toBe('c1');
    expect(result[1]!.id).toBe('sa:sa1');
  });

  it('deduplicates when query is provided', () => {
    // SA_ALICE_DUP has same email as C_ALICE; only the contact should appear.
    const result = mergeFilter([C_ALICE], [SA_ALICE_DUP], 'alice');
    const alices = result.filter((r) => r.email === 'alice@x.test');
    expect(alices).toHaveLength(1);
    expect(alices[0]!.id).toBe('c1');
  });

  it('returns empty when nothing matches', () => {
    const result = mergeFilter([C_ALICE, C_BOB], [SA_CAROL, SA_DAN], 'zzz-nomatch');
    expect(result).toHaveLength(0);
  });

  it('caps at limit', () => {
    const result = mergeFilter([C_ALICE, C_BOB], [SA_CAROL, SA_DAN], 'test', 2);
    expect(result).toHaveLength(2);
  });
});
