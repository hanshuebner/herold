/**
 * Unit tests for pickInitialExpanded (re #135).
 *
 * REQ-UI-20 (corrected): all unread messages are expanded on thread open.
 * When all messages are read, only the last message is expanded.
 */

import { describe, it, expect } from 'vitest';
import { pickInitialExpanded } from './pick-initial-expanded';
import type { Email } from './types';

function makeEmail(id: string, seen: boolean): Email {
  return {
    id,
    threadId: 'tid-1',
    mailboxIds: { inbox: true } as Record<string, true>,
    keywords: seen ? ({ $seen: true } as Record<string, true | undefined>) : {},
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

describe('pickInitialExpanded (re #135)', () => {
  it('returns empty array for an empty emails list', () => {
    expect(pickInitialExpanded([])).toEqual([]);
  });

  it('returns only the last message id when all messages are read', () => {
    const emails = [makeEmail('e1', true), makeEmail('e2', true), makeEmail('e3', true)];
    expect(pickInitialExpanded(emails)).toEqual(['e3']);
  });

  it('returns the single unread message id when only the last message is unread (new-message case)', () => {
    const emails = [makeEmail('e1', true), makeEmail('e2', true), makeEmail('e3', false)];
    expect(pickInitialExpanded(emails)).toEqual(['e3']);
  });

  it('returns ALL unread message ids when multiple consecutive messages are unread from the middle (mark-from-here case, re #135)', () => {
    // Messages e3, e4, e5 are unread (marked from e3 onward).
    // Expected: all three are expanded, not just e3 or e5.
    const emails = [
      makeEmail('e1', true),
      makeEmail('e2', true),
      makeEmail('e3', false),
      makeEmail('e4', false),
      makeEmail('e5', false),
    ];
    expect(pickInitialExpanded(emails)).toEqual(['e3', 'e4', 'e5']);
  });

  it('returns all message ids when all messages are unread', () => {
    const emails = [makeEmail('e1', false), makeEmail('e2', false), makeEmail('e3', false)];
    expect(pickInitialExpanded(emails)).toEqual(['e1', 'e2', 'e3']);
  });

  it('returns the single message id regardless of read state', () => {
    expect(pickInitialExpanded([makeEmail('e1', true)])).toEqual(['e1']);
    expect(pickInitialExpanded([makeEmail('e1', false)])).toEqual(['e1']);
  });

  it('returns only the unread messages when they are scattered among read messages', () => {
    // e1 read, e2 unread, e3 read, e4 unread — both unread are expanded.
    const emails = [
      makeEmail('e1', true),
      makeEmail('e2', false),
      makeEmail('e3', true),
      makeEmail('e4', false),
    ];
    expect(pickInitialExpanded(emails)).toEqual(['e2', 'e4']);
  });
});
