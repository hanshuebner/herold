/**
 * Pure-state tests for the thread-row drag-and-drop coordinator
 * (REQ-UI-17, REQ-MAIL-54). The full source/target wiring is exercised
 * through the mail-view + sidebar component tests; here we cover the
 * coordinator's invariants:
 *   - begin/end lifecycle
 *   - the active-mailbox no-op (current view is not a valid drop target)
 *   - dragIdsForRow honours active multi-selection
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

const { mailMock } = vi.hoisted(() => ({
  mailMock: {
    listFolder: 'inbox' as string,
    listSelectedIds: new Set<string>(),
    mailboxes: new Map<string, { id: string; role: string }>(),
  },
}));

vi.mock('./store.svelte', () => ({ mail: mailMock }));

import { threadDnd, dragIdsForRow } from './dnd-thread.svelte';

beforeEach(() => {
  threadDnd.end();
  mailMock.listFolder = 'inbox';
  mailMock.listSelectedIds = new Set();
  mailMock.mailboxes = new Map([
    ['mb-inbox', { id: 'mb-inbox', role: 'inbox' }],
    ['mb-trash', { id: 'mb-trash', role: 'trash' }],
    ['mb-custom', { id: 'mb-custom', role: '' }],
  ]);
});

describe('threadDnd lifecycle', () => {
  it('begin sets the dragged ids and clears on end', () => {
    threadDnd.begin(['e1', 'e2']);
    expect(threadDnd.current?.ids).toEqual(['e1', 'e2']);
    expect(threadDnd.current?.hoveredMailboxId).toBeNull();
    threadDnd.end();
    expect(threadDnd.current).toBeNull();
  });

  it('begin([]) does not start a drag', () => {
    threadDnd.begin([]);
    expect(threadDnd.current).toBeNull();
  });

  it('setHovered no-ops outside a drag', () => {
    threadDnd.setHovered('mb-inbox');
    expect(threadDnd.current).toBeNull();
  });

  it('setHovered updates the hovered target during a drag', () => {
    threadDnd.begin(['e1']);
    threadDnd.setHovered('mb-trash');
    expect(threadDnd.current?.hoveredMailboxId).toBe('mb-trash');
    threadDnd.setHovered(null);
    expect(threadDnd.current?.hoveredMailboxId).toBeNull();
  });
});

describe('threadDnd.isValidTarget — active-mailbox rejection', () => {
  it('rejects the mailbox matching listFolder by role (inbox)', () => {
    threadDnd.begin(['e1']);
    mailMock.listFolder = 'inbox';
    expect(threadDnd.isValidTarget('mb-inbox')).toBe(false);
  });

  it('accepts other mailboxes when in inbox', () => {
    threadDnd.begin(['e1']);
    mailMock.listFolder = 'inbox';
    expect(threadDnd.isValidTarget('mb-trash')).toBe(true);
    expect(threadDnd.isValidTarget('mb-custom')).toBe(true);
  });

  it('rejects the active custom mailbox by id', () => {
    threadDnd.begin(['e1']);
    mailMock.listFolder = 'mb-custom';
    expect(threadDnd.isValidTarget('mb-custom')).toBe(false);
    expect(threadDnd.isValidTarget('mb-inbox')).toBe(true);
  });

  it('rejects any target when no drag is active', () => {
    expect(threadDnd.isValidTarget('mb-trash')).toBe(false);
  });

  it('treats listFolder=all as no specific mailbox (every mailbox is valid)', () => {
    threadDnd.begin(['e1']);
    mailMock.listFolder = 'all';
    expect(threadDnd.isValidTarget('mb-inbox')).toBe(true);
    expect(threadDnd.isValidTarget('mb-trash')).toBe(true);
  });
});

describe('dragIdsForRow — multi-selection honouring', () => {
  it('drags just the row when not in selection', () => {
    mailMock.listSelectedIds = new Set(['e2', 'e3']);
    expect(dragIdsForRow('e1')).toEqual(['e1']);
  });

  it('drags just the row when selection has only this row', () => {
    mailMock.listSelectedIds = new Set(['e1']);
    expect(dragIdsForRow('e1')).toEqual(['e1']);
  });

  it('drags all selected rows when the dragged row is in a multi-selection', () => {
    mailMock.listSelectedIds = new Set(['e1', 'e2', 'e3']);
    const ids = dragIdsForRow('e1');
    expect(new Set(ids)).toEqual(new Set(['e1', 'e2', 'e3']));
    expect(ids.length).toBe(3);
  });

  it('drags just the row when not in the (multi) selection', () => {
    mailMock.listSelectedIds = new Set(['e2', 'e3', 'e4']);
    expect(dragIdsForRow('e1')).toEqual(['e1']);
  });
});
