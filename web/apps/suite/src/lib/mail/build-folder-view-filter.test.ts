/**
 * Issue #310: a label view (any mailbox that is not Junk or Trash) must
 * exclude messages that also sit in Junk or Trash, so a message the spam
 * filter or the user filed away still surfaces in a label view. Junk,
 * Trash, and the "all" view are unaffected -- buildFolderViewFilter only
 * handles the mailbox-scoped case; loadFolder/#refreshFolderInPlace keep
 * their own branches for the virtual "important"/"snoozed"/"all" folders.
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
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('returns a plain inMailbox filter for a label view when neither special mailbox exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-label', mb('mb-label', 'Project X', null));

    const filter = buildFolderViewFilter('mb-label', mailboxes);

    expect(filter).toEqual({ inMailbox: 'mb-label' });
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
