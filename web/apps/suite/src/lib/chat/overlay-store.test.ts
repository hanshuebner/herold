/**
 * Unit tests for the chat overlay window store.
 *
 * Exercises: open, close, minimise, expand, max-3 eviction,
 * deduplication (same conversation cannot be open twice).
 */

import { describe, it, expect, beforeEach } from 'vitest';
import { chatOverlay, _internals_forTest } from './overlay-store.svelte';

const { MAX_WINDOWS } = _internals_forTest;

beforeEach(() => {
  // Reset state between tests.
  chatOverlay.windows = [];
});

describe('openWindow', () => {
  it('opens a window for a conversation', () => {
    chatOverlay.openWindow('c1');
    expect(chatOverlay.windows).toHaveLength(1);
    expect(chatOverlay.windows[0]!.conversationId).toBe('c1');
    expect(chatOverlay.windows[0]!.minimized).toBe(false);
  });

  it('deduplicates: second open of same conversation moves it to end, not a second window', () => {
    chatOverlay.openWindow('c1');
    chatOverlay.openWindow('c2');
    chatOverlay.openWindow('c1');
    // c1 still only once, now last.
    const ids = chatOverlay.windows.map((w) => w.conversationId);
    expect(ids).toEqual(['c2', 'c1']);
  });

  it('dedup unminimizes a minimized window', () => {
    chatOverlay.openWindow('c1');
    chatOverlay.minimizeWindow(chatOverlay.windows[0]!.key);
    expect(chatOverlay.windows[0]!.minimized).toBe(true);
    chatOverlay.openWindow('c1');
    expect(chatOverlay.windows[0]!.minimized).toBe(false);
  });

  it(`evicts the oldest when more than ${MAX_WINDOWS} are opened`, () => {
    // Open MAX_WINDOWS + 1 distinct conversations.
    for (let i = 1; i <= MAX_WINDOWS + 1; i++) {
      chatOverlay.openWindow(`c${i}`);
    }
    expect(chatOverlay.windows).toHaveLength(MAX_WINDOWS);
    // c1 was the oldest and should have been evicted.
    const ids = chatOverlay.windows.map((w) => w.conversationId);
    expect(ids).not.toContain('c1');
    expect(ids).toContain(`c${MAX_WINDOWS + 1}`);
  });
});

describe('closeWindow', () => {
  it('removes a window by key', () => {
    chatOverlay.openWindow('c1');
    const key = chatOverlay.windows[0]!.key;
    chatOverlay.closeWindow(key);
    expect(chatOverlay.windows).toHaveLength(0);
  });

  it('is a no-op for an unknown key', () => {
    chatOverlay.openWindow('c1');
    chatOverlay.closeWindow('does-not-exist');
    expect(chatOverlay.windows).toHaveLength(1);
  });
});

describe('minimizeWindow / expandWindow', () => {
  it('minimizes a window', () => {
    chatOverlay.openWindow('c1');
    const key = chatOverlay.windows[0]!.key;
    chatOverlay.minimizeWindow(key);
    expect(chatOverlay.windows[0]!.minimized).toBe(true);
  });

  it('expands a minimized window', () => {
    chatOverlay.openWindow('c1');
    const key = chatOverlay.windows[0]!.key;
    chatOverlay.minimizeWindow(key);
    chatOverlay.expandWindow(key);
    expect(chatOverlay.windows[0]!.minimized).toBe(false);
  });
});

describe('toggleMinimize', () => {
  it('toggles from expanded to minimized', () => {
    chatOverlay.openWindow('c1');
    const key = chatOverlay.windows[0]!.key;
    chatOverlay.toggleMinimize(key);
    expect(chatOverlay.windows[0]!.minimized).toBe(true);
  });

  it('toggles from minimized to expanded', () => {
    chatOverlay.openWindow('c1');
    const key = chatOverlay.windows[0]!.key;
    chatOverlay.minimizeWindow(key);
    chatOverlay.toggleMinimize(key);
    expect(chatOverlay.windows[0]!.minimized).toBe(false);
  });
});

describe('isOpen', () => {
  it('returns true when conversation has a window', () => {
    chatOverlay.openWindow('c1');
    expect(chatOverlay.isOpen('c1')).toBe(true);
  });

  it('returns false when conversation has no window', () => {
    expect(chatOverlay.isOpen('c99')).toBe(false);
  });

  it('returns false after the window is closed', () => {
    chatOverlay.openWindow('c1');
    chatOverlay.closeWindow(chatOverlay.windows[0]!.key);
    expect(chatOverlay.isOpen('c1')).toBe(false);
  });
});
