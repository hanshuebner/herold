/**
 * Tests for wireDesktopNotificationClick — the click handler for page-created
 * desktop notifications (issue #83). These notifications are shown by the open
 * tab and their click is handled in the page, never reaching the service
 * worker's notificationclick handler; without this wiring the click did
 * nothing.
 *
 * Clicking the notification now opens a chrome-less popup at the
 * thread-window route rather than navigating the existing tab. A per-thread
 * window name (herold-thread-<id>) means re-clicking the same notification
 * focuses the existing popup instead of stacking a new one (REQ-PUSH-60).
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
    wireDesktopNotificationClick(n, 't42', vi.fn());
    expect(typeof n.onclick).toBe('function');
  });

  it('opens a popup at the thread-window route on click', () => {
    const n = makeNotification();
    const open = vi.fn();
    wireDesktopNotificationClick(n, 't42', open);

    fireClick(n);

    expect(open).toHaveBeenCalledTimes(1);
    const [url, name, features] = open.mock.calls[0] as [string, string, string];
    expect(url).toBe('/#/thread-window/t42');
    expect(name).toBe('herold-thread-t42');
    expect(features).toContain('popup');
    expect(n.close).toHaveBeenCalledTimes(1);
  });

  it('does NOT navigate the existing tab', () => {
    // The old behaviour was to call navigate(path) on the existing tab.
    // The new behaviour opens a popup; the existing tab is untouched.
    const n = makeNotification();
    const open = vi.fn();
    wireDesktopNotificationClick(n, 't42', open);

    fireClick(n);

    // open() is called; there is no separate navigate call.
    expect(open).toHaveBeenCalledTimes(1);
  });

  it('prevents the default notification action', () => {
    const n = makeNotification();
    wireDesktopNotificationClick(n, 't1', vi.fn());
    const event = fireClick(n);
    expect((event.preventDefault as ReturnType<typeof vi.fn>)).toHaveBeenCalled();
  });

  it('encodes the thread id in the url', () => {
    const n = makeNotification();
    const open = vi.fn();
    wireDesktopNotificationClick(n, 'a/b?c', open);
    fireClick(n);
    const [url] = open.mock.calls[0] as [string, string, string];
    expect(url).toBe('/#/thread-window/a%2Fb%3Fc');
  });

  it('uses a per-thread window name so re-clicking focuses the existing popup', () => {
    const n = makeNotification();
    const open = vi.fn();
    wireDesktopNotificationClick(n, 'thread-99', open);
    fireClick(n);
    const [, name] = open.mock.calls[0] as [string, string, string];
    expect(name).toBe('herold-thread-thread-99');
  });

  it('passes popup window features', () => {
    const n = makeNotification();
    const open = vi.fn();
    wireDesktopNotificationClick(n, 't7', open);
    fireClick(n);
    const [, , features] = open.mock.calls[0] as [string, string, string];
    expect(features).toContain('popup');
    expect(features).toContain('width=');
    expect(features).toContain('height=');
  });

  it('records the click in the device-local debug ring', () => {
    const n = makeNotification();
    wireDesktopNotificationClick(n, 't9', vi.fn());
    fireClick(n);
    expect(appendEvent).toHaveBeenCalledWith(
      'page',
      'info',
      'desktop-notif.click',
      expect.objectContaining({ threadId: 't9', url: '/#/thread-window/t9' }),
    );
  });
});
