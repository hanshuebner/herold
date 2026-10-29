/**
 * Regression test (issue #548): the quick options were computed once
 * when the component mounted, so a Suite tab left open across days kept
 * offering the load day's "Tomorrow morning" -- a wake time already in
 * the past, which the server's wake worker released within a minute.
 * The options are computed each time the picker opens.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import SnoozePicker from './SnoozePicker.svelte';
import { snoozePicker } from './snooze-picker.svelte';
import type { Mailbox } from './types';

const inbox: Mailbox = {
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
const mailboxes = new Map<string, Mailbox>([['mb-inbox', inbox]]);
const snoozeEmail = vi.fn();
vi.mock('./store.svelte', () => ({
  mail: {
    get mailboxes() {
      return mailboxes;
    },
    get inbox() {
      return inbox;
    },
    snoozeEmail: (...args: unknown[]) => snoozeEmail(...args),
  },
}));

describe('SnoozePicker quick options track the current time', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    snoozeEmail.mockReset();
    snoozePicker.close();
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it('offers Tomorrow morning relative to the day the picker is opened', () => {
    vi.setSystemTime(new Date(2026, 9, 28, 20, 0, 0)); // Wed 28 Oct, evening
    render(SnoozePicker);
    snoozePicker.open('e0');
    flushSync();
    snoozePicker.close();
    flushSync();

    // The tab stays open two days; the picker is opened on Fri 30 Oct.
    vi.setSystemTime(new Date(2026, 9, 30, 18, 33, 0));
    snoozePicker.open('e1');
    flushSync();

    const quick = screen.getAllByRole('button').filter((b) => b.closest('.quick-list'));
    const tomorrow = quick.find((b) => b.textContent?.includes('Tomorrow'))!;
    tomorrow.click();
    expect(snoozeEmail).toHaveBeenCalledTimes(1);
    const at = snoozeEmail.mock.calls[0]![1] as Date;
    expect(at.getTime()).toBe(new Date(2026, 9, 31, 8, 0, 0, 0).getTime());
  });

  it('refuses a quick option whose time passed while the picker stayed open', async () => {
    vi.setSystemTime(new Date(2026, 9, 29, 10, 0, 0)); // Later today = 13:00
    render(SnoozePicker);
    snoozePicker.open('e1');
    flushSync();
    const laterToday = () =>
      screen
        .getAllByRole('button')
        .filter((b) => b.closest('.quick-list'))
        .find((b) => b.textContent?.includes('Later today'))!;

    // Picker left open past 13:00; the stale click must not commit.
    vi.setSystemTime(new Date(2026, 9, 29, 13, 1, 0));
    laterToday().click();
    flushSync();
    expect(snoozeEmail).not.toHaveBeenCalled();
    expect(snoozePicker.isOpen).toBe(true);

    // The options were recomputed: Later today is now 16:00 and commits.
    laterToday().click();
    expect(snoozeEmail).toHaveBeenCalledTimes(1);
    const at = snoozeEmail.mock.calls[0]![1] as Date;
    expect(at.getTime()).toBe(new Date(2026, 9, 29, 16, 0, 0, 0).getTime());
  });
});
