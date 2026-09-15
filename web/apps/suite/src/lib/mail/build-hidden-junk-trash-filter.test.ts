/**
 * Issue #384: the REQ-SRC-06 Junk/Trash exclusion `buildFolderViewFilter`
 * applies to a label view can hide every member -- most visibly when a
 * label is applied to a message while it still sits in Junk. Without any
 * indication, the label view just renders the empty state.
 *
 * `hasHiddenJunkTrashExclusion` gates whether the hidden-members count is
 * worth computing at all; `buildHiddenJunkTrashCountFilters` builds the
 * pair of flat, single-mailbox-per-row `Email/query` filters the count is
 * computed from (raw membership minus the folder view's own filtered
 * total) -- deliberately two flat queries rather than one combined
 * AND/OR query, because the query engine's per-row match can only ever
 * see one mailbox membership per candidate row
 * (`internal/protojmap/mail/email/load.go:listAccountMessages` lists one
 * `store.Message` row per `ListMessages` call, one call per mailbox), so
 * a filter ANDing two independent `inMailbox` conditions can never match
 * any row even when the message is genuinely in both mailboxes.
 */

import { describe, it, expect } from 'vitest';
import { hasHiddenJunkTrashExclusion, buildHiddenJunkTrashCountFilters } from './store.svelte';
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

describe('hasHiddenJunkTrashExclusion (re #384)', () => {
  it('is true for a genuine label when a Junk or Trash mailbox exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    mailboxes.set('mb-label', mb('mb-label', 'not-spam', null));

    expect(hasHiddenJunkTrashExclusion('mb-label', mailboxes)).toBe(true);
  });

  it('is false when the mailbox has no Trash or Junk mailbox to exclude', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-label', mb('mb-label', 'Project X', null));

    expect(hasHiddenJunkTrashExclusion('mb-label', mailboxes)).toBe(false);
  });

  it('is false for the Junk mailbox itself -- it is shown unfiltered', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    expect(hasHiddenJunkTrashExclusion('mb-junk', mailboxes)).toBe(false);
  });

  it('is false for the Trash mailbox itself -- it is shown unfiltered', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    expect(hasHiddenJunkTrashExclusion('mb-trash', mailboxes)).toBe(false);
  });

  it('is false for a system-role mailbox such as Inbox even when Trash exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));

    expect(hasHiddenJunkTrashExclusion('mb-inbox', mailboxes)).toBe(false);
  });
});

describe('buildHiddenJunkTrashCountFilters (re #384)', () => {
  it('pairs the folder view filter with the label\'s plain, unfiltered membership', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    mailboxes.set('mb-label', mb('mb-label', 'not-spam', null));

    const { visible, raw } = buildHiddenJunkTrashCountFilters('mb-label', mailboxes);

    // `visible` is exactly buildFolderViewFilter's output: a flat
    // condition, single row per candidate, correctly evaluable.
    expect(visible).toEqual({
      inMailbox: 'mb-label',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
    // `raw` is the label's plain membership, no exclusion.
    expect(raw).toEqual({ inMailbox: 'mb-label' });
  });
});
