/**
 * Regression test for issue #307: the Suite sidebar had no Junk mailbox
 * entry, so spam filed into Junk by the server (#297) was unreachable in
 * the UI -- no review, no false-positive rescue, no way to empty it.
 *
 * App.svelte's sidebar renders a `sidebar.junk` row that navigates to
 * `/mail/folder/junk`. The folder-route resolver (store.svelte.ts and
 * MailView.svelte) must treat 'junk' as a roled folder -- resolved via
 * `MailStore.junk` (the Mailbox whose role is 'junk') the same way
 * 'trash'/'sent'/'drafts' are -- rather than being rejected as an unknown
 * mailbox id. This test drives the store-level wiring the sidebar and the
 * folder route both depend on.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Mailbox } from './lib/mail/types';
import { t, i18n } from './lib/i18n/i18n.svelte';

function junkMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id' | 'name' | 'role'>): Mailbox {
  return {
    parentId: null,
    sortOrder: 0,
    totalEmails: 3,
    unreadEmails: 1,
    totalThreads: 3,
    unreadThreads: 1,
    ...overrides,
  };
}

describe('sidebar.junk i18n key (re #307)', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });

  it('resolves to "Spam" in English', () => {
    expect(t('sidebar.junk')).toBe('Spam');
  });

  it('resolves to "Spam" in German (matches the existing report-spam terminology)', () => {
    i18n.locale = 'de';
    expect(t('sidebar.junk')).toBe('Spam');
  });
});

describe('junk folder route wiring (re #307)', () => {
  let mailMod: typeof import('./lib/mail/store.svelte');

  beforeEach(async () => {
    vi.resetModules();
    mailMod = await import('./lib/mail/store.svelte');
  });

  it('mail.junk resolves the role="junk" mailbox (the getter App.svelte and the folder route both rely on)', () => {
    const { mail } = mailMod;
    mail.mailboxes = new Map([
      ['mbox-junk', junkMailbox({ id: 'mbox-junk', name: 'Junk', role: 'junk' })],
    ]);

    expect(mail.junk?.id).toBe('mbox-junk');
  });

  it("folderTotalFromMailboxes('junk', ...) resolves the role mailbox's total (the folder route no longer treats 'junk' as an unknown mailbox id)", () => {
    const { folderTotalFromMailboxes } = mailMod;
    const mailboxes = new Map([
      ['mbox-junk', junkMailbox({ id: 'mbox-junk', name: 'Junk', role: 'junk', totalEmails: 7 })],
    ]);

    expect(folderTotalFromMailboxes('junk', mailboxes)).toBe(7);
  });
});
