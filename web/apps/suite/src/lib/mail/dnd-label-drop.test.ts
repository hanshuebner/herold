/**
 * Regression tests for issue #50 — drag-and-drop to a custom-mailbox (label)
 * target must work even when the sidebar "More" section is manually collapsed.
 *
 * App.svelte derives:
 *   moreOpenEffective = moreOpen || threadDnd.current !== null
 *
 * so the custom-mailbox list is rendered for the duration of any active drag,
 * regardless of the user's toggle state. These tests document the threadDnd
 * invariant that the derived value relies on.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

const { mailMock } = vi.hoisted(() => ({
  mailMock: {
    listFolder: 'inbox' as string,
    listSelectedIds: new Set<string>(),
    mailboxes: new Map<string, { id: string; role: string }>([
      ['mb-inbox', { id: 'mb-inbox', role: 'inbox' }],
      ['mb-label', { id: 'mb-label', role: '' }],
    ]),
  },
}));

vi.mock('./store.svelte', () => ({ mail: mailMock }));

import { threadDnd } from './dnd-thread.svelte';

beforeEach(() => {
  threadDnd.end();
  mailMock.listFolder = 'inbox';
  mailMock.listSelectedIds = new Set();
});

describe('drag-to-label: moreOpenEffective preconditions (re #50)', () => {
  it('threadDnd.current is non-null while a drag is active', () => {
    // When moreOpen is false and a drag begins, moreOpenEffective becomes
    // true, so custom-mailbox rows are rendered and can receive drop events.
    threadDnd.begin(['e1']);
    expect(threadDnd.current).not.toBeNull();
  });

  it('threadDnd.current stays non-null after setHovered during a drag', () => {
    threadDnd.begin(['e1']);
    threadDnd.setHovered('mb-label');
    expect(threadDnd.current).not.toBeNull();
  });

  it('threadDnd.current is null after end — moreOpenEffective returns to moreOpen', () => {
    threadDnd.begin(['e1']);
    threadDnd.end();
    expect(threadDnd.current).toBeNull();
  });

  it('a custom mailbox (label) is a valid drop target when dragging from inbox', () => {
    threadDnd.begin(['e1']);
    mailMock.listFolder = 'inbox';
    // mb-label has role='' so it is a user-created label; isValidTarget must
    // return true so the drop handler fires and bulkMoveToMailbox is called.
    expect(threadDnd.isValidTarget('mb-label')).toBe(true);
  });

  it('the current label view is not a valid drop target for itself', () => {
    threadDnd.begin(['e1']);
    mailMock.listFolder = 'mb-label';
    expect(threadDnd.isValidTarget('mb-label')).toBe(false);
  });
});
