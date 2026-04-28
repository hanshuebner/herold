/**
 * Tests for the pure helpers behind the move-to-mailbox picker.
 * The Svelte-runes-aware singleton itself is exercised by the
 * component test; these specs cover the candidate / filter logic
 * that decides what shows up in the list.
 */
import { describe, it, expect } from 'vitest';
import { _internals_forTest } from './move-picker.svelte';
import type { Mailbox } from './types';

const { computeMoveCandidates, filterMailboxesByName } = _internals_forTest;

function mb(partial: Partial<Mailbox> & { id: string; name: string }): Mailbox {
  return {
    role: null,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...partial,
  };
}

describe('computeMoveCandidates', () => {
  const inbox = mb({ id: 'mb-inbox', name: 'Inbox', role: 'inbox' });
  const archive = mb({ id: 'mb-archive', name: 'Archive', role: 'archive' });
  const sent = mb({ id: 'mb-sent', name: 'Sent', role: 'sent' });
  const drafts = mb({ id: 'mb-drafts', name: 'Drafts', role: 'drafts' });
  const trash = mb({ id: 'mb-trash', name: 'Trash', role: 'trash' });
  const work = mb({ id: 'mb-work', name: 'Work' });
  const personal = mb({ id: 'mb-personal', name: 'Personal' });
  const all = [inbox, archive, sent, drafts, trash, work, personal];

  it('puts roled mailboxes first in the fixed order, then user mailboxes alphabetically', () => {
    const got = computeMoveCandidates(all, new Set());
    expect(got.map((m) => m.id)).toEqual([
      'mb-inbox',
      'mb-archive',
      'mb-sent',
      'mb-drafts',
      'mb-trash',
      'mb-personal',
      'mb-work',
    ]);
  });

  it('excludes mailboxes the email already lives in', () => {
    const got = computeMoveCandidates(all, new Set(['mb-inbox', 'mb-work']));
    expect(got.map((m) => m.id)).toEqual([
      'mb-archive',
      'mb-sent',
      'mb-drafts',
      'mb-trash',
      'mb-personal',
    ]);
  });

  it('sorts user mailboxes alphabetically when no roles are present', () => {
    const got = computeMoveCandidates([work, personal], new Set());
    expect(got.map((m) => m.id)).toEqual(['mb-personal', 'mb-work']);
  });

  it('returns an empty list when every mailbox is already a current one', () => {
    const got = computeMoveCandidates(all, new Set(all.map((m) => m.id)));
    expect(got).toEqual([]);
  });

  it('places mailboxes with unknown roles after the known ones', () => {
    const odd = mb({ id: 'mb-odd', name: 'Odd', role: 'archive-but-stranger' });
    const got = computeMoveCandidates([inbox, odd], new Set());
    // 'archive-but-stranger' is not in ROLE_ORDER, so role index = -1 →
    // sorted after the known role inbox.
    expect(got.map((m) => m.id)).toEqual(['mb-inbox', 'mb-odd']);
  });
});

describe('filterMailboxesByName', () => {
  const a = mb({ id: 'a', name: 'Inbox' });
  const b = mb({ id: 'b', name: 'Inquiries' });
  const c = mb({ id: 'c', name: 'Work' });
  const items = [a, b, c];

  it('returns the input unchanged for an empty filter', () => {
    expect(filterMailboxesByName(items, '')).toEqual(items);
  });

  it('returns the input unchanged for whitespace-only filter', () => {
    expect(filterMailboxesByName(items, '   ')).toEqual(items);
  });

  it('matches case-insensitive substrings', () => {
    expect(filterMailboxesByName(items, 'in')).toEqual([a, b]);
    expect(filterMailboxesByName(items, 'IN')).toEqual([a, b]);
  });

  it('returns an empty list when nothing matches', () => {
    expect(filterMailboxesByName(items, 'xyz')).toEqual([]);
  });

  it('preserves the input ordering', () => {
    const reversed = [c, b, a];
    expect(filterMailboxesByName(reversed, 'i')).toEqual([b, a]);
  });
});
