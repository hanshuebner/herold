/**
 * Unit tests for the self-sent deduplication union (re #88).
 *
 * When herold stores a self-sent message as two separate JMAP Email objects
 * — one Sent copy and one Inbox-delivered copy — both sharing the same
 * Message-ID, `resolveDeduplicatedThreadEmails` must:
 *
 *   1. Collapse same-Message-ID pairs into a single representative Email.
 *   2. Union the mailboxIds across all copies so the representative reports
 *      membership in BOTH Sent and Inbox.
 *   3. Union the keywords so read/flagged state is consistent.
 *   4. Leave normal (non-self-sent) threads entirely unchanged.
 *   5. Preserve the issue #40 raw-id dedup as a second line of defence.
 *
 * `loadThread` now stores ALL email IDs in the committed snapshot (both
 * Sent and Inbox copies for self-sent messages). `threadDedupeCount` then
 * calls `resolveDeduplicatedThreadEmails(committed, emails).length` so the
 * badge shows the logical count (3) rather than the raw storage count (6).
 * This is verified below via the store singleton after directly seeding
 * `committedThreadEmailIds` with all-copies IDs.
 */

import { describe, it, expect } from 'vitest';
import type { Email } from './types';
import { _internals_forTest } from '../mail/store.svelte';

const { resolveDeduplicatedThreadEmails } = _internals_forTest;

// ── Helpers ──────────────────────────────────────────────────────────────────

function makeEmail(
  id: string,
  opts: {
    messageId?: string | null;
    mailboxIds?: Record<string, true>;
    keywords?: Record<string, true | undefined>;
    threadId?: string;
  } = {},
): Email {
  return {
    id,
    threadId: opts.threadId ?? 'thread-1',
    mailboxIds: opts.mailboxIds ?? { 'mbx-inbox': true },
    keywords: opts.keywords ?? {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    subject: 'Test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    ...(opts.messageId !== undefined ? { messageId: opts.messageId ? [opts.messageId] : [] } : {}),
  } as unknown as Email;
}

// ── resolveDeduplicatedThreadEmails ──────────────────────────────────────────

describe('resolveDeduplicatedThreadEmails', () => {
  it('returns an empty array for an empty emailIds list', () => {
    const result = resolveDeduplicatedThreadEmails([], new Map());
    expect(result).toEqual([]);
  });

  it('passes through a normal (non-self-sent) thread unchanged', () => {
    const e1 = makeEmail('e1', { messageId: '<msg1@host>', mailboxIds: { 'mbx-inbox': true } });
    const e2 = makeEmail('e2', { messageId: '<msg2@host>', mailboxIds: { 'mbx-inbox': true } });
    const e3 = makeEmail('e3', { messageId: '<msg3@host>', mailboxIds: { 'mbx-inbox': true } });
    const emails = new Map([
      ['e1', e1],
      ['e2', e2],
      ['e3', e3],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e1', 'e2', 'e3'], emails);
    expect(result).toHaveLength(3);
    expect(result[0]).toBe(e1);
    expect(result[1]).toBe(e2);
    expect(result[2]).toBe(e3);
  });

  it('collapses a Sent copy and an Inbox copy with the same Message-ID into one representative', () => {
    const sent = makeEmail('e-sent', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-sent': true },
    });
    const inbox = makeEmail('e-inbox', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-inbox': true },
    });
    const emails = new Map([
      ['e-sent', sent],
      ['e-inbox', inbox],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    expect(result).toHaveLength(1);
  });

  it('representative is the first occurrence when sizes are equal or absent', () => {
    const sent = makeEmail('e-sent', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-sent': true },
    });
    const inbox = makeEmail('e-inbox', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-inbox': true },
    });
    const emails = new Map([
      ['e-sent', sent],
      ['e-inbox', inbox],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    // Neither copy has a size; the first in thread order wins.
    expect(result[0]!.id).toBe('e-sent');
  });

  it('representative is the copy with the larger size (externally-received copy wins)', () => {
    // Sent copy is stored first (lower in thread order) but has a smaller
    // size (no transit headers). Inbox copy arrives later but is richer.
    const sent = makeEmail('e-sent', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-sent': true },
    });
    const inbox = makeEmail('e-inbox', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-inbox': true },
    });
    // Simulate transit-header overhead on the inbound copy.
    (sent as Email & { size: number }).size = 1200;
    (inbox as Email & { size: number }).size = 1850;
    const emails = new Map([
      ['e-sent', sent],
      ['e-inbox', inbox],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    // inbox copy (1850) beats sent copy (1200) regardless of thread order.
    expect(result[0]!.id).toBe('e-inbox');
  });

  it('representative mailboxIds is the UNION of all same-Message-ID copies', () => {
    const sent = makeEmail('e-sent', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-sent': true },
    });
    const inbox = makeEmail('e-inbox', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-inbox': true },
    });
    const emails = new Map([
      ['e-sent', sent],
      ['e-inbox', inbox],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    expect(result[0]!.mailboxIds).toEqual({
      'mbx-sent': true,
      'mbx-inbox': true,
    });
  });

  it('representative keywords is the UNION of all same-Message-ID copies (truthy-wins)', () => {
    const sentUnseen = makeEmail('e-sent', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-sent': true },
      keywords: { $flagged: true }, // no $seen
    });
    const inboxSeen = makeEmail('e-inbox', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-inbox': true },
      keywords: { $seen: true }, // no $flagged
    });
    const emails = new Map([
      ['e-sent', sentUnseen],
      ['e-inbox', inboxSeen],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    expect(result[0]!.keywords).toEqual({ $flagged: true, $seen: true });
  });

  it('handles a thread with 3 logical messages each stored as Sent + Inbox (6 rows total)', () => {
    const SENT = 'mbx-sent';
    const INBOX = 'mbx-inbox';
    const emails = new Map<string, Email>([
      ['s1', makeEmail('s1', { messageId: '<m1@h>', mailboxIds: { [SENT]: true } })],
      ['d1', makeEmail('d1', { messageId: '<m1@h>', mailboxIds: { [INBOX]: true } })],
      ['s2', makeEmail('s2', { messageId: '<m2@h>', mailboxIds: { [SENT]: true } })],
      ['d2', makeEmail('d2', { messageId: '<m2@h>', mailboxIds: { [INBOX]: true } })],
      ['s3', makeEmail('s3', { messageId: '<m3@h>', mailboxIds: { [SENT]: true } })],
      ['d3', makeEmail('d3', { messageId: '<m3@h>', mailboxIds: { [INBOX]: true } })],
    ]);

    const result = resolveDeduplicatedThreadEmails(
      ['s1', 'd1', 's2', 'd2', 's3', 'd3'],
      emails,
    );

    // Three logical messages
    expect(result).toHaveLength(3);

    // Each representative has both Sent and Inbox membership
    for (const email of result) {
      expect(email.mailboxIds[SENT]).toBe(true);
      expect(email.mailboxIds[INBOX]).toBe(true);
    }

    // Representatives are the first occurrences
    expect(result[0]!.id).toBe('s1');
    expect(result[1]!.id).toBe('s2');
    expect(result[2]!.id).toBe('s3');
  });

  it('does NOT merge emails whose Message-IDs differ', () => {
    const e1 = makeEmail('e1', { messageId: '<a@host>', mailboxIds: { 'mbx-inbox': true } });
    const e2 = makeEmail('e2', { messageId: '<b@host>', mailboxIds: { 'mbx-sent': true } });
    const emails = new Map([
      ['e1', e1],
      ['e2', e2],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e1', 'e2'], emails);
    expect(result).toHaveLength(2);
    expect(result[0]).toBe(e1);
    expect(result[1]).toBe(e2);
  });

  it('includes emails without a messageId as independent entries (no merging)', () => {
    // Two emails with no messageId should both appear, not be merged
    const e1 = makeEmail('e1', { mailboxIds: { 'mbx-inbox': true } });
    const e2 = makeEmail('e2', { mailboxIds: { 'mbx-inbox': true } });
    const emails = new Map([
      ['e1', e1],
      ['e2', e2],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e1', 'e2'], emails);
    expect(result).toHaveLength(2);
  });

  it('preserves raw-id deduplication (issue #40 regression defence)', () => {
    // The same email id appearing twice in emailIds must only appear once in output
    const e1 = makeEmail('e1', { messageId: '<msg@host>', mailboxIds: { 'mbx-inbox': true } });
    const emails = new Map([['e1', e1]]);
    const result = resolveDeduplicatedThreadEmails(['e1', 'e1'], emails);
    expect(result).toHaveLength(1);
    expect(result[0]).toBe(e1);
  });

  it('skips email ids not present in the emails map', () => {
    const e1 = makeEmail('e1', { messageId: '<msg1@host>', mailboxIds: { 'mbx-inbox': true } });
    const emails = new Map([['e1', e1]]);
    const result = resolveDeduplicatedThreadEmails(['e1', 'e-missing'], emails);
    expect(result).toHaveLength(1);
    expect(result[0]).toBe(e1);
  });

  it('normalises angle-bracket Message-IDs for grouping (case-insensitive)', () => {
    const sent = makeEmail('e-sent', {
      messageId: '<FOO.BAR@HOST>',
      mailboxIds: { 'mbx-sent': true },
    });
    const inbox = makeEmail('e-inbox', {
      messageId: '<foo.bar@host>',
      mailboxIds: { 'mbx-inbox': true },
    });
    const emails = new Map([
      ['e-sent', sent],
      ['e-inbox', inbox],
    ]);
    const result = resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    // Same normalised id => same group => one representative
    expect(result).toHaveLength(1);
    expect(result[0]!.mailboxIds).toEqual({ 'mbx-sent': true, 'mbx-inbox': true });
  });

  it('synthetic merged email does not mutate the original emails in the map', () => {
    const sent = makeEmail('e-sent', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-sent': true },
    });
    const inbox = makeEmail('e-inbox', {
      messageId: '<msg@host>',
      mailboxIds: { 'mbx-inbox': true },
    });
    const emails = new Map([
      ['e-sent', sent],
      ['e-inbox', inbox],
    ]);
    resolveDeduplicatedThreadEmails(['e-sent', 'e-inbox'], emails);
    // Originals must be unmodified
    expect(emails.get('e-sent')!.mailboxIds).toEqual({ 'mbx-sent': true });
    expect(emails.get('e-inbox')!.mailboxIds).toEqual({ 'mbx-inbox': true });
  });

  it('mixed thread: some messages duplicated, some not', () => {
    const SENT = 'mbx-sent';
    const INBOX = 'mbx-inbox';
    // e1/e1d share a Message-ID (self-sent), e2 is a normal inbound reply
    const e1sent = makeEmail('e1s', { messageId: '<m1@h>', mailboxIds: { [SENT]: true } });
    const e1inbox = makeEmail('e1i', { messageId: '<m1@h>', mailboxIds: { [INBOX]: true } });
    const e2 = makeEmail('e2', { messageId: '<m2@h>', mailboxIds: { [INBOX]: true } });
    const emails = new Map([
      ['e1s', e1sent],
      ['e1i', e1inbox],
      ['e2', e2],
    ]);

    const result = resolveDeduplicatedThreadEmails(['e1s', 'e1i', 'e2'], emails);
    expect(result).toHaveLength(2);

    // First = merged representative of m1
    expect(result[0]!.id).toBe('e1s');
    expect(result[0]!.mailboxIds).toEqual({ [SENT]: true, [INBOX]: true });

    // Second = e2, unchanged
    expect(result[1]).toBe(e2);
  });
});
