/**
 * Tests for the Contacts client-side store. We exercise the
 * filter() public surface; the actual JMAP load is integration
 * territory and tested live.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { contacts, type ContactSuggestion } from './store.svelte';

function seed(rows: ContactSuggestion[]): void {
  // Force-set the suggestions by reaching into the singleton; this is
  // safe in tests because the store has no other consumers running.
  // (The real load() path is exercised in integration tests.)
  (contacts as unknown as { suggestions: ContactSuggestion[] }).suggestions =
    rows;
}

beforeEach(() => seed([]));

describe('contacts.filter', () => {
  const ALICE = { id: '1', name: 'Alice Liddell', email: 'alice@x.test' };
  const BOB = { id: '2', name: 'Bob Smith', email: 'bob@y.test' };
  const CATH = { id: '3', name: 'Catherine Brown', email: 'catherine@z.test' };
  const ALL = [ALICE, BOB, CATH];

  it('returns input slice when query is empty', () => {
    seed(ALL);
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
