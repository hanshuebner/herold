/**
 * Issue #471: the Snoozed virtual folder must list messages soonest-wake-
 * first. The server's Email/query has no snoozedUntil sort property (see
 * sortIdsBySnoozedUntilAscending's docstring in store.svelte.ts), so the
 * client reorders the already-fetched ids by ascending snoozedUntil.
 */

import { describe, it, expect } from 'vitest';
import type { Email } from './types';
import { sortIdsBySnoozedUntilAscending } from './store.svelte';

function makeEmail(id: string, snoozedUntil: string | null): Email {
  return {
    id,
    threadId: `thread-${id}`,
    mailboxIds: { 'mbx-inbox': true },
    keywords: { $snoozed: true },
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    subject: 'Test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil,
  } as unknown as Email;
}

describe('sortIdsBySnoozedUntilAscending (re #471)', () => {
  it('orders ids soonest wake time first', () => {
    const emails = new Map<string, Email>([
      ['late', makeEmail('late', '2026-03-05T09:00:00Z')],
      ['soon', makeEmail('soon', '2026-03-01T09:00:00Z')],
      ['mid', makeEmail('mid', '2026-03-03T09:00:00Z')],
    ]);
    const ids = ['late', 'soon', 'mid'];

    expect(sortIdsBySnoozedUntilAscending(ids, emails)).toEqual(['soon', 'mid', 'late']);
  });

  it('is the opposite direction of the server default (arrival-descending) order', () => {
    // Simulate the receivedAt-descending order the wire query still
    // returns: newest-arrived first, unrelated to wake time.
    const emails = new Map<string, Email>([
      ['newest-arrival-latest-wake', makeEmail('newest-arrival-latest-wake', '2026-03-10T00:00:00Z')],
      ['oldest-arrival-soonest-wake', makeEmail('oldest-arrival-soonest-wake', '2026-03-01T00:00:00Z')],
    ]);
    const arrivalOrderIds = ['newest-arrival-latest-wake', 'oldest-arrival-soonest-wake'];

    expect(sortIdsBySnoozedUntilAscending(arrivalOrderIds, emails)).toEqual([
      'oldest-arrival-soonest-wake',
      'newest-arrival-latest-wake',
    ]);
  });

  it('places ids with no snoozedUntil (or not yet resolved) after every dated id', () => {
    const emails = new Map<string, Email>([
      ['dated', makeEmail('dated', '2026-03-01T00:00:00Z')],
      ['undated', makeEmail('undated', null)],
      // 'unresolved' deliberately absent from the map -- Email/get in flight.
    ]);
    const ids = ['undated', 'unresolved', 'dated'];

    expect(sortIdsBySnoozedUntilAscending(ids, emails)).toEqual(['dated', 'undated', 'unresolved']);
  });

  it('does not mutate the input array', () => {
    const emails = new Map<string, Email>([
      ['b', makeEmail('b', '2026-03-05T00:00:00Z')],
      ['a', makeEmail('a', '2026-03-01T00:00:00Z')],
    ]);
    const ids = ['b', 'a'];
    const sorted = sortIdsBySnoozedUntilAscending(ids, emails);

    expect(ids).toEqual(['b', 'a']);
    expect(sorted).toEqual(['a', 'b']);
  });
});
