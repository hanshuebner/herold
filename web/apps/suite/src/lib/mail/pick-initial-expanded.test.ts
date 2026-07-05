/**
 * Unit tests for pickInitialExpanded (re #135).
 *
 * REQ-UI-20 (corrected): the first unread message is expanded on thread open,
 * not the last. When all messages are read, the last message is expanded.
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
  it('returns null for an empty array', () => {
    expect(pickInitialExpanded([])).toBeNull();
  });

  it('returns the last message id when all messages are read', () => {
    const emails = [makeEmail('e1', true), makeEmail('e2', true), makeEmail('e3', true)];
    expect(pickInitialExpanded(emails)).toBe('e3');
  });

  it('returns the single unread message when only the last is unread (new-message case)', () => {
    const emails = [makeEmail('e1', true), makeEmail('e2', true), makeEmail('e3', false)];
    expect(pickInitialExpanded(emails)).toBe('e3');
  });

  it('returns the FIRST unread message when multiple consecutive messages are unread from the middle (mark-from-here case, re #135)', () => {
    // Messages e3, e4, e5 are unread (marked from e3 onward).
    // Expected: e3 is expanded, not e5.
    const emails = [
      makeEmail('e1', true),
      makeEmail('e2', true),
      makeEmail('e3', false),
      makeEmail('e4', false),
      makeEmail('e5', false),
    ];
    expect(pickInitialExpanded(emails)).toBe('e3');
  });

  it('returns the first message when all messages are unread', () => {
    const emails = [makeEmail('e1', false), makeEmail('e2', false), makeEmail('e3', false)];
    expect(pickInitialExpanded(emails)).toBe('e1');
  });

  it('returns the single message regardless of read state', () => {
    expect(pickInitialExpanded([makeEmail('e1', true)])).toBe('e1');
    expect(pickInitialExpanded([makeEmail('e1', false)])).toBe('e1');
  });
});
