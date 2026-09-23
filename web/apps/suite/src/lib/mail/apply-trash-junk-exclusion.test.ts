/**
 * Issue #467: the inbox and every other folder view exclude Junk via the
 * herold `notInMailbox` filter condition -- a message that a classifier
 * verdict has filed to Junk stays out even while it keeps an Inbox (or
 * other label) membership. Trash is never excluded from a folder view:
 * a message that also sits in Trash is listed, per the store's
 * Trash-never-coexists invariant (#460).
 *
 * `applyTrashJunkExclusion` takes the `notInMailbox` path only when the
 * server advertises `Capability.HeroldEmailQueryExtensions`; against an
 * older server it falls back to the previous `inMailboxOtherThan:
 * [<trash>, <junk>]` shape, which excludes both.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox } from './types';

vi.mock('../jmap/client', () => ({
  jmap: { hasCapability: vi.fn(() => false) },
  strict: (r: unknown[]) => r,
  setJmapOnUnauthenticated: vi.fn(),
}));

import { jmap } from '../jmap/client';
import { applyTrashJunkExclusion, buildFolderViewFilter } from './store.svelte';

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

describe('applyTrashJunkExclusion (issue #467)', () => {
  beforeEach(() => {
    vi.mocked(jmap.hasCapability).mockReturnValue(false);
  });

  it('excludes only Junk via notInMailbox when the server advertises the capability', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(true);
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const out = applyTrashJunkExclusion({ inMailbox: 'mb-inbox' }, m);
    expect(out).not.toHaveProperty('operator');
    expect(out).toEqual({ inMailbox: 'mb-inbox', notInMailbox: ['mb-junk'] });
  });

  it('falls back to inMailboxOtherThan: [trash, junk] when the capability is absent', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(false);
    const m = new Map<string, Mailbox>();
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    m.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const out = applyTrashJunkExclusion({ inMailbox: 'mb-label' }, m);
    expect(out).toEqual({
      inMailbox: 'mb-label',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });

  it('returns the filter unchanged when no Junk mailbox exists, capability advertised', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(true);
    const m = new Map<string, Mailbox>();
    m.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    m.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    const filter = { inMailbox: 'mb-inbox' };
    expect(applyTrashJunkExclusion(filter, m)).toBe(filter);
  });
});

describe('buildFolderViewFilter shapes (issue #467)', () => {
  it('inbox view: {inMailbox, notKeyword, notInMailbox: [junk]} when the capability is advertised', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(true);
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = buildFolderViewFilter('mb-inbox', mailboxes);
    expect(filter).toEqual({
      inMailbox: 'mb-inbox',
      notKeyword: '$snoozed',
      notInMailbox: ['mb-junk'],
    });
  });

  it('label view: {inMailbox, notKeyword, notInMailbox: [junk]} when the capability is advertised', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(true);
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    mailboxes.set('mb-label', mb('mb-label', 'Project X', null));

    const filter = buildFolderViewFilter('mb-label', mailboxes);
    expect(filter).toEqual({
      inMailbox: 'mb-label',
      notKeyword: '$snoozed',
      notInMailbox: ['mb-junk'],
    });
  });

  it('falls back to inMailboxOtherThan: [trash, junk] for a label view when the capability is absent', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(false);
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));
    mailboxes.set('mb-label', mb('mb-label', 'Project X', null));

    const filter = buildFolderViewFilter('mb-label', mailboxes);
    expect(filter).toEqual({
      inMailbox: 'mb-label',
      notKeyword: '$snoozed',
      inMailboxOtherThan: expect.arrayContaining(['mb-trash', 'mb-junk']),
    });
  });
});

describe('the fold: Junk hides a message from the inbox, Trash does not (issue #467)', () => {
  // Minimal evaluator for the subset of FilterCondition semantics this
  // test needs -- inMailbox (RFC 8621 SS4.4.1's "in" test) and
  // notInMailbox (the herold extension, issue #467): both look only at
  // a message's own mailboxIds membership set.
  function matches(filter: Record<string, unknown>, mailboxIds: string[]): boolean {
    if (typeof filter.inMailbox === 'string' && !mailboxIds.includes(filter.inMailbox)) {
      return false;
    }
    const notIn = filter.notInMailbox as string[] | undefined;
    if (notIn && notIn.some((id) => mailboxIds.includes(id))) {
      return false;
    }
    return true;
  }

  it('a message in Inbox and Junk is not listed; one in Inbox and Trash is listed', () => {
    vi.mocked(jmap.hasCapability).mockReturnValue(true);
    const mailboxes = new Map<string, Mailbox>();
    mailboxes.set('mb-inbox', mb('mb-inbox', 'Inbox', 'inbox'));
    mailboxes.set('mb-trash', mb('mb-trash', 'Trash', 'trash'));
    mailboxes.set('mb-junk', mb('mb-junk', 'Junk', 'junk'));

    const filter = buildFolderViewFilter('mb-inbox', mailboxes) as Record<string, unknown>;

    expect(matches(filter, ['mb-inbox', 'mb-junk'])).toBe(false);
    expect(matches(filter, ['mb-inbox', 'mb-trash'])).toBe(true);
  });
});
