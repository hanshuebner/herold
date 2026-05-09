/**
 * REQ-SRC-06: default search scope excludes Trash + Junk via
 * applyTrashJunkExclusion. The helper wraps the parsed filter with
 * inMailboxOtherThan: [<trash-id>, <junk-id>] when those mailboxes
 * exist for the principal.
 */

import { describe, it, expect } from 'vitest';
import { applyTrashJunkExclusion } from './store.svelte';
import type { Mailbox } from './types';

function mb(id: string, name: string, role: string | null): Mailbox {
  return {
    id,
    name,
    role,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  };
}

describe('applyTrashJunkExclusion (REQ-SRC-06)', () => {
  it('wraps a text filter with inMailboxOtherThan: [trash, junk]', () => {
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const out = applyTrashJunkExclusion({ text: 'foo' }, m);
    expect(out).toEqual({
      operator: 'AND',
      conditions: [
        { text: 'foo' },
        { inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']) },
      ],
    });
  });

  it('returns the filter unchanged when neither role exists', () => {
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    const filter = { text: 'foo' };
    expect(applyTrashJunkExclusion(filter, m)).toBe(filter);
  });

  it('applies exclusion for an AND-tree filter as a whole', () => {
    const m = new Map<string, Mailbox>();
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    const inner = {
      operator: 'AND' as const,
      conditions: [{ from: 'alice' }, { hasAttachment: true }],
    };
    const out = applyTrashJunkExclusion(inner, m);
    expect(out).toEqual({
      operator: 'AND',
      conditions: [inner, { inMailboxOtherThan: ['mb-trash', 'mb-junk'] }],
    });
  });
});
