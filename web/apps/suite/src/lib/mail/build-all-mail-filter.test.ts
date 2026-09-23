/**
 * Issue #426: the virtual "all"/"important"/"snoozed" views must exclude
 * the principal's Junk and Trash mailboxes the same way a label view does
 * (#310) -- REQ-SRC-06/07, REQ-UI-13b. Before the fix, `all` carried no
 * filter at all and `important`/`snoozed` carried only their `hasKeyword`
 * predicate, so Junk/Trash members leaked into every one of them.
 *
 * This file does not mock `../jmap/client`, so `jmap.hasCapability`
 * defaults to false: it exercises `applyTrashJunkExclusion`'s pre-#467
 * fallback (both Junk and Trash excluded via `inMailboxOtherThan`). The
 * capability-advertised path (Junk-only exclusion via `notInMailbox`) is
 * covered by apply-trash-junk-exclusion.test.ts.
 */

import { describe, it, expect } from 'vitest';
import { applyTrashJunkExclusion, buildAllMailFilter } from './store.svelte';
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

describe('buildAllMailFilter (re #426)', () => {
  it('excludes Junk and Trash from the "all" view when both exist', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = buildAllMailFilter(mailboxes);

    expect(filter).not.toHaveProperty('operator');
    expect(filter).toEqual({
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('treats an IMAP-imported mailbox named "Spam" as Junk via its role, excluding it too', () => {
    // internal/imapimport/sync.go's ensureMailbox assigns
    // store.MailboxAttrJunk to a mailbox named "Spam" (case-insensitive)
    // exactly like "Junk"; the server derives role: "junk" from that
    // attribute, not from the mailbox name. The frontend exclusion keys
    // off role, so it must catch this mailbox even though it isn't named
    // "Junk".
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-spam', mb('mb-spam', 'Spam', 'junk'));

    const filter = buildAllMailFilter(mailboxes);

    expect(filter).toEqual({ inMailboxOtherThan: ['mb-spam'] });
  });

  it('returns undefined (no filter) when the principal has neither a Junk nor a Trash mailbox', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));

    const filter = buildAllMailFilter(mailboxes);

    expect(filter).toBeUndefined();
  });
});

describe('important/snoozed virtual-folder filters carry the Junk/Trash exclusion (re #426)', () => {
  it('splices inMailboxOtherThan into the $important hasKeyword filter', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = applyTrashJunkExclusion({ hasKeyword: '$important' }, mailboxes);

    expect(filter).not.toHaveProperty('operator');
    expect(filter).toEqual({
      hasKeyword: '$important',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('splices inMailboxOtherThan into the $snoozed hasKeyword filter', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = applyTrashJunkExclusion({ hasKeyword: '$snoozed' }, mailboxes);

    expect(filter).not.toHaveProperty('operator');
    expect(filter).toEqual({
      hasKeyword: '$snoozed',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('leaves the $important/$snoozed filters unchanged when neither Junk nor Trash exists', () => {
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));

    expect(applyTrashJunkExclusion({ hasKeyword: '$important' }, mailboxes)).toEqual({
      hasKeyword: '$important',
    });
    expect(applyTrashJunkExclusion({ hasKeyword: '$snoozed' }, mailboxes)).toEqual({
      hasKeyword: '$snoozed',
    });
  });
});
