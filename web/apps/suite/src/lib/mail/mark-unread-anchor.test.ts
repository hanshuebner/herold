/**
 * Unit tests for pickMarkUnreadAnchor (re #526).
 *
 * The thread toolbar's "Mark unread" button anchors markUnreadFromHere at
 * the first currently-expanded message, falling back to the last message
 * when nothing is expanded.
 */

import { describe, it, expect } from 'vitest';
import { pickMarkUnreadAnchor } from './mark-unread-anchor';
import type { Email } from './types';

function makeEmail(id: string): Email {
  return {
    id,
    threadId: 'tid-1',
    mailboxIds: { inbox: true } as Record<string, true>,
    keywords: {},
    from: [{ name: 'Test', email: 'test@example.test' }],
    to: null,
    cc: null,
    blobId: 'blob-stub',
    subject: 'Subject',
    preview: 'Preview',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
  } as Email;
}

describe('pickMarkUnreadAnchor (re #526)', () => {
  const emails = [makeEmail('e1'), makeEmail('e2'), makeEmail('e3'), makeEmail('e4'), makeEmail('e5')];

  it('anchors at the first expanded message in thread order when only the last two are expanded', () => {
    expect(pickMarkUnreadAnchor(emails, new Set(['e4', 'e5']))).toBe('e4');
  });

  it('anchors at the sole expanded message when only one is expanded', () => {
    expect(pickMarkUnreadAnchor(emails, new Set(['e3']))).toBe('e3');
  });

  it('ignores expanded-set order and uses thread order instead', () => {
    // Even if the Set happens to iterate e5 before e2, thread order wins.
    const s = new Set(['e5', 'e2']);
    expect(pickMarkUnreadAnchor(emails, s)).toBe('e2');
  });

  it('falls back to the last message when nothing is expanded', () => {
    expect(pickMarkUnreadAnchor(emails, new Set())).toBe('e5');
  });

  it('returns null for an empty thread regardless of the expanded set', () => {
    expect(pickMarkUnreadAnchor([], new Set())).toBeNull();
    expect(pickMarkUnreadAnchor([], new Set(['e1']))).toBeNull();
  });

  it('anchors at the single message of a one-message thread whether or not it is expanded', () => {
    const one = [makeEmail('only')];
    expect(pickMarkUnreadAnchor(one, new Set(['only']))).toBe('only');
    expect(pickMarkUnreadAnchor(one, new Set())).toBe('only');
  });
});
