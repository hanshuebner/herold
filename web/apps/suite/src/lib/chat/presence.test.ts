/**
 * Unit tests for the chat presence module (REQ-CHAT-180..184).
 *
 * `windowFocused` is driven by document.hasFocus() &&
 * document.visibilityState === 'visible'. Tests that exercise the
 * unfocused branch stub document.hasFocus() before mutating state, so
 * the sync inside setComposeFocus / tick does not clobber the test
 * setup.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { presence, _resetForTest } from './presence.svelte';

describe('chat presence (REQ-CHAT-180..184)', () => {
  beforeEach(() => {
    _resetForTest();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("returns 'present-in-chat' when window focused AND compose focused on this conversation (REQ-CHAT-181)", () => {
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c1')).toBe('present-in-chat');
  });

  it("returns 'present-elsewhere' for other conversations when one compose is focused (REQ-CHAT-184)", () => {
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c2')).toBe('present-elsewhere');
  });

  it("returns 'present-elsewhere' when window focused but no compose has focus AND user is not idle (REQ-CHAT-182)", () => {
    presence.lastInputAt = Date.now();
    expect(presence.stateFor('c1')).toBe('present-elsewhere');
  });

  it("returns 'absent' when document.hasFocus() is false (REQ-CHAT-183)", () => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(false);
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c1')).toBe('absent');
  });

  it("returns 'absent' when document.visibilityState is 'hidden' (REQ-CHAT-183)", () => {
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => 'hidden',
    });
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c1')).toBe('absent');
    // Restore for subsequent tests.
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => 'visible',
    });
  });

  it("returns 'absent' when window focused but user has been idle longer than the threshold (REQ-CHAT-183)", () => {
    presence.composeFocusedId = null;
    presence.idleThresholdSeconds = 1;
    presence.lastInputAt = Date.now() - 10_000;
    expect(presence.stateFor('c1')).toBe('absent');
  });

  it('idle threshold defaults to 120 seconds (REQ-CHAT-183)', () => {
    expect(presence.idleThresholdSeconds).toBe(120);
  });

  it('clearComposeFocus only clears when the conversation matches', () => {
    presence.setComposeFocus('c1');
    presence.clearComposeFocus('c2');
    expect(presence.composeFocusedId).toBe('c1');
    presence.clearComposeFocus('c1');
    expect(presence.composeFocusedId).toBeNull();
  });

  it('present-in-chat requires the window to be focused too (REQ-CHAT-181)', () => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(false);
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c1')).toBe('absent');
  });

  it('only one conversation can be present-in-chat at a time (REQ-CHAT-184)', () => {
    presence.setComposeFocus('c1');
    presence.setComposeFocus('c2');
    expect(presence.stateFor('c1')).toBe('present-elsewhere');
    expect(presence.stateFor('c2')).toBe('present-in-chat');
  });
});
