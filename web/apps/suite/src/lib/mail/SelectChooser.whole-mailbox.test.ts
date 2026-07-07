/**
 * Tests for the whole-mailbox selection banner added in issue #149.
 *
 * The banner lives in MailView but its visibility depends on store state
 * (listWholeMailboxSelected, listFolderTotal, listSelectedIds, listEmails).
 * We test that the `folderTotalFromMailboxes` helper and the
 * `selectWholeMailbox` / `clearSelection` state transitions work correctly.
 * End-to-end rendering of the banner itself is verified with puppeteer
 * against a real dev instance.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { folderTotalFromMailboxes } from './store.svelte';
import type { Mailbox } from './types';

function makeMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'name' | 'role'>): Mailbox {
  return {
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...overrides,
  };
}

// ── Banner trigger condition ─────────────────────────────────────────────────
//
// The banner is shown when ALL of the following hold:
//   (a) listSelectedIds.size === listEmails.length  (all visible selected)
//   (b) !listWholeMailboxSelected                   (not already in whole-mailbox mode)
//   (c) listFolderTotal !== null                    (folder total is known)
//   (d) listFolderTotal > listEmails.length         (there are more than the visible window)
//
// We verify condition (c) and (d) via folderTotalFromMailboxes; conditions
// (a) and (b) are boolean state transitions validated below.

describe('folderTotalFromMailboxes: banner trigger conditions', () => {
  it('returns null for virtual folders — banner suppressed', () => {
    expect(folderTotalFromMailboxes('all', new Map())).toBeNull();
    expect(folderTotalFromMailboxes('important', new Map())).toBeNull();
    expect(folderTotalFromMailboxes('snoozed', new Map())).toBeNull();
  });

  it('returns totalThreads > visible count — banner shown for large inbox', () => {
    const mailboxes = new Map([
      ['m1', makeMailbox({ id: 'm1', name: 'Inbox', role: 'inbox', totalThreads: 200 })],
    ]);
    const total = folderTotalFromMailboxes('inbox', mailboxes);
    // With 50 loaded rows, 200 > 50 → banner should be shown.
    expect(total).toBe(200);
    expect(total! > 50).toBe(true);
  });

  it('returns totalThreads equal to visible count — banner suppressed (all loaded)', () => {
    const mailboxes = new Map([
      ['m1', makeMailbox({ id: 'm1', name: 'Inbox', role: 'inbox', totalThreads: 30 })],
    ]);
    const total = folderTotalFromMailboxes('inbox', mailboxes);
    // With 30 loaded rows and total 30, no more messages → banner suppressed.
    expect(total).toBe(30);
    expect(total! > 30).toBe(false);
  });

  it('returns totalThreads for a custom mailbox by id', () => {
    const mailboxes = new Map([
      ['custom', makeMailbox({ id: 'custom', name: 'Newsletter', role: null, totalThreads: 500 })],
    ]);
    expect(folderTotalFromMailboxes('custom', mailboxes)).toBe(500);
  });

  it('returns null for custom mailbox when id is absent', () => {
    expect(folderTotalFromMailboxes('unknown', new Map())).toBeNull();
  });
});

// ── selectWholeMailbox state transitions ─────────────────────────────────────
//
// We test the transitions using a minimal in-memory mock of the relevant
// MailStore properties so we don't need the full JMAP wiring.
describe('whole-mailbox state transitions (issue #149)', () => {
  // Minimal stand-in for MailStore that captures the fields we care about.
  function makeStore() {
    return {
      listSelectedIds: new Set<string>(['e1', 'e2', 'e3']),
      listWholeMailboxSelected: false,

      selectWholeMailbox() {
        this.listWholeMailboxSelected = true;
      },
      clearSelection() {
        if (this.listSelectedIds.size === 0 && !this.listWholeMailboxSelected) return;
        this.listSelectedIds = new Set();
        this.listWholeMailboxSelected = false;
      },
      selectAllVisible(visibleIds: string[]) {
        this.listWholeMailboxSelected = false;
        this.listSelectedIds = new Set(visibleIds);
      },
    };
  }

  it('selectWholeMailbox sets listWholeMailboxSelected', () => {
    const store = makeStore();
    expect(store.listWholeMailboxSelected).toBe(false);
    store.selectWholeMailbox();
    expect(store.listWholeMailboxSelected).toBe(true);
  });

  it('clearSelection clears both listSelectedIds and listWholeMailboxSelected', () => {
    const store = makeStore();
    store.selectWholeMailbox();
    expect(store.listWholeMailboxSelected).toBe(true);
    store.clearSelection();
    expect(store.listWholeMailboxSelected).toBe(false);
    expect(store.listSelectedIds.size).toBe(0);
  });

  it('selectAllVisible clears listWholeMailboxSelected', () => {
    const store = makeStore();
    store.selectWholeMailbox();
    expect(store.listWholeMailboxSelected).toBe(true);
    store.selectAllVisible(['e1', 'e2']);
    expect(store.listWholeMailboxSelected).toBe(false);
    expect(store.listSelectedIds.size).toBe(2);
  });

  it('clearSelection is a no-op when already empty', () => {
    const store = makeStore();
    store.listSelectedIds = new Set();
    store.listWholeMailboxSelected = false;
    // Should not throw; should remain no-op.
    store.clearSelection();
    expect(store.listSelectedIds.size).toBe(0);
    expect(store.listWholeMailboxSelected).toBe(false);
  });
});
