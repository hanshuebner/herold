/**
 * Component tests for SnoozePicker's wake-destination affordance
 * (issue #274). The destination select defaults to Inbox and commits
 * alongside whichever "when" the user picks (quick option or custom
 * datetime) -- choosing a destination never becomes a second required
 * step.
 *
 * A direct-select-interaction test ("pick Archive, then commit, and
 * assert the non-default id reaches snoozeEmail") is not included here:
 * happy-dom's synthetic `change` dispatch on a `<select>` does not
 * drive Svelte 5's compiled `bind:value` listener the way a real
 * browser does (reproduced independently of this component -- a
 * minimal `<select bind:value>` fixture shows the same gap). That path
 * is exercised in a real browser via the puppeteer verification for
 * this change instead.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import SnoozePicker from './SnoozePicker.svelte';
import { snoozePicker } from './snooze-picker.svelte';
import type { Mailbox } from './types';

const inboxMailbox: Mailbox = {
  id: 'mb-inbox',
  name: 'Inbox',
  role: 'inbox',
  parentId: null,
  sortOrder: 0,
  totalEmails: 0,
  unreadEmails: 0,
  totalThreads: 0,
  unreadThreads: 0,
};
const sentMailbox: Mailbox = {
  id: 'mb-sent',
  name: 'Sent',
  role: 'sent',
  parentId: null,
  sortOrder: 2,
  totalEmails: 0,
  unreadEmails: 0,
  totalThreads: 0,
  unreadThreads: 0,
};
const archiveMailbox: Mailbox = {
  id: 'mb-archive',
  name: 'Archive',
  role: 'archive',
  parentId: null,
  sortOrder: 1,
  totalEmails: 0,
  unreadEmails: 0,
  totalThreads: 0,
  unreadThreads: 0,
};

const mockMailboxes = new Map<string, Mailbox>([
  ['mb-inbox', inboxMailbox],
  ['mb-sent', sentMailbox],
  ['mb-archive', archiveMailbox],
]);

const snoozeEmail = vi.fn();
vi.mock('./store.svelte', () => ({
  mail: {
    get mailboxes() {
      return mockMailboxes;
    },
    get inbox() {
      return inboxMailbox;
    },
    snoozeEmail: (...args: unknown[]) => snoozeEmail(...args),
  },
}));

describe('SnoozePicker wake-destination affordance', () => {
  beforeEach(() => {
    snoozeEmail.mockReset();
    snoozePicker.close();
  });

  it('defaults the wake-destination select to Inbox', () => {
    snoozePicker.open('e1');
    render(SnoozePicker);
    const select = screen.getByLabelText('Wake in') as HTMLSelectElement;
    expect(select.value).toBe('mb-inbox');
  });

  it('lists every mailbox as a candidate destination, roled mailboxes first', () => {
    snoozePicker.open('e1');
    render(SnoozePicker);
    const select = screen.getByLabelText('Wake in') as HTMLSelectElement;
    const optionValues = Array.from(select.options).map((o) => o.value);
    expect(optionValues).toEqual(['mb-inbox', 'mb-archive', 'mb-sent']);
  });

  it('a quick-pick commit sends the default Inbox destination in one interaction', async () => {
    snoozePicker.open('e1');
    render(SnoozePicker);
    const [firstQuickButton] = screen
      .getAllByRole('button')
      .filter((b) => b.closest('.quick-list'));
    await fireEvent.click(firstQuickButton!);
    expect(snoozeEmail).toHaveBeenCalledTimes(1);
    const [emailId, at, wake] = snoozeEmail.mock.calls[0]!;
    expect(emailId).toBe('e1');
    expect(at).toBeInstanceOf(Date);
    expect(wake).toBe('mb-inbox');
  });
});
