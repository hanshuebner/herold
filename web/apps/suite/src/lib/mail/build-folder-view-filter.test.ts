/**
 * Issue #310: a label view (any mailbox that is not Junk or Trash) must
 * exclude messages that also sit in Junk or Trash, so a message the spam
 * filter or the user filed away still surfaces in a label view. Junk and
 * Trash themselves are unaffected -- buildFolderViewFilter only handles
 * the mailbox-scoped case; the virtual "all"/"important"/"snoozed" folders
 * apply the same exclusion via buildAllMailFilter and applyTrashJunkExclusion
 * directly in loadFolder/#refreshFolderInPlace/#buildCurrentFolderFilter
 * (re #426; see build-all-mail-filter.test.ts).
 *
 * Issue #468: every ordinary folder/label view also excludes a message
 * carrying the `$snoozed` keyword, so a snoozed conversation leaves the
 * Inbox (and any other non-Junk/Trash folder) until its reminder falls
 * due. The Snoozed virtual folder keeps its own positive `hasKeyword:
 * '$snoozed'` selection (see build-all-mail-filter.test.ts).
 */

import { describe, it, expect } from 'vitest';
import { buildFolderViewFilter } from './store.svelte';
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

describe('buildFolderViewFilter (issue #310)', () => {
  it('excludes Junk and Trash from a label view when both exist', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    mailboxes.set('mb-label', mb('mb-label', 'vorsitz@classic-computing.de', null));

    const filter = buildFolderViewFilter('mb-label', mailboxes);

    expect(filter).not.toHaveProperty('operator');
    expect(filter).toEqual({
      inMailbox: 'mb-label',
      notKeyword: '$snoozed',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('returns a plain inMailbox + notKeyword filter for a label view when neither special mailbox exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-label', mb('mb-label', 'Project X', null));

    const filter = buildFolderViewFilter('mb-label', mailboxes);

    expect(filter).toEqual({ inMailbox: 'mb-label', notKeyword: '$snoozed' });
  });

  it('excludes $snoozed from the Inbox view (re #468)', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));

    const filter = buildFolderViewFilter('mb-inbox', mailboxes);

    expect(filter).not.toHaveProperty('operator');
    expect(filter).toEqual({ inMailbox: 'mb-inbox', notKeyword: '$snoozed' });
  });

  it('leaves the Junk view unfiltered even when Trash also exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = buildFolderViewFilter('mb-junk', mailboxes);

    expect(filter).toEqual({ inMailbox: 'mb-junk' });
  });

  it('leaves the Trash view unfiltered even when Junk also exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = buildFolderViewFilter('mb-trash', mailboxes);

    expect(filter).toEqual({ inMailbox: 'mb-trash' });
  });
});
