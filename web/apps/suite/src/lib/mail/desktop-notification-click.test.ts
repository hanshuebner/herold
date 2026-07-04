/**
 * Tests for wireDesktopNotificationClick — the click handler for page-created
 * desktop notifications (issue #83). These notifications are shown by the open
 * tab and their click is handled in the page, never reaching the service
 * worker's notificationclick handler; without this wiring the click did
 * nothing.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../debug-ring/debug-ring', () => ({
  appendEvent: vi.fn(() => Promise.resolve()),
}));

import { wireDesktopNotificationClick } from './store.svelte';
import { appendEvent } from '../debug-ring/debug-ring';

function makeNotification() {
  return {
    onclick: null as ((event: Event) => void) | null,
    close: vi.fn(),
  };
}

function fireClick(n: ReturnType<typeof makeNotification>) {
  const event = { preventDefault: vi.fn() } as unknown as Event;
  n.onclick?.(event);
  return event;
}

describe('wireDesktopNotificationClick', () => {
  beforeEach(() => vi.clearAllMocks());

  it('installs an onclick handler', () => {
    const n = makeNotification();
    wireDesktopNotificationClick(n, 't42', vi.fn(), vi.fn());
    expect(typeof n.onclick).toBe('function');
  });

  it('focuses the window and navigates to the thread on click', () => {
    const n = makeNotification();
    const navigate = vi.fn();
    const focus = vi.fn();
    wireDesktopNotificationClick(n, 't42', navigate, focus);

    fireClick(n);

    expect(focus).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith('/mail/thread/t42');
    expect(n.close).toHaveBeenCalledTimes(1);
  });

  it('prevents the default notification action', () => {
    const n = makeNotification();
    wireDesktopNotificationClick(n, 't1', vi.fn(), vi.fn());
    const event = fireClick(n);
    expect((event.preventDefault as ReturnType<typeof vi.fn>)).toHaveBeenCalled();
  });

  it('encodes the thread id in the route', () => {
    const n = makeNotification();
    const navigate = vi.fn();
    wireDesktopNotificationClick(n, 'a/b?c', navigate, vi.fn());
    fireClick(n);
    expect(navigate).toHaveBeenCalledWith('/mail/thread/a%2Fb%3Fc');
  });

  it('still navigates when focus() throws (browser denied focus)', () => {
    const n = makeNotification();
    const navigate = vi.fn();
    const focus = vi.fn(() => {
      throw new Error('focus denied');
    });
    wireDesktopNotificationClick(n, 't7', navigate, focus);

    expect(() => fireClick(n)).not.toThrow();
    expect(navigate).toHaveBeenCalledWith('/mail/thread/t7');
    expect(n.close).toHaveBeenCalled();
  });

  it('records the click in the device-local debug ring', () => {
    const n = makeNotification();
    wireDesktopNotificationClick(n, 't9', vi.fn(), vi.fn());
    fireClick(n);
    expect(appendEvent).toHaveBeenCalledWith(
      'page',
      'info',
      'desktop-notif.click',
      expect.objectContaining({ threadId: 't9', path: '/mail/thread/t9' }),
    );
  });
});
