/**
 * Unit tests for the chat presence module (REQ-CHAT-180..184).
 */

import { describe, it, expect, beforeEach } from 'vitest';
import { presence, _resetForTest } from './presence.svelte';

describe('chat presence (REQ-CHAT-180..184)', () => {
  beforeEach(() => {
    _resetForTest();
  });

  it("returns 'present-in-chat' when window focused AND compose focused on this conversation (REQ-CHAT-181)", () => {
    presence.windowFocused = true;
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c1')).toBe('present-in-chat');
  });

  it("returns 'present-elsewhere' for other conversations when one compose is focused (REQ-CHAT-184)", () => {
    presence.windowFocused = true;
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c2')).toBe('present-elsewhere');
  });

  it("returns 'present-elsewhere' when window focused but no compose has focus AND user is not idle (REQ-CHAT-182)", () => {
    presence.windowFocused = true;
    presence.lastInputAt = Date.now();
    expect(presence.stateFor('c1')).toBe('present-elsewhere');
  });

  it("returns 'absent' when window is not focused (REQ-CHAT-183)", () => {
    presence.windowFocused = false;
    presence.setComposeFocus('c1');
    expect(presence.stateFor('c1')).toBe('absent');
  });

  it("returns 'absent' when window focused but user has been idle longer than the threshold (REQ-CHAT-183)", () => {
    presence.windowFocused = true;
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

  it("present-in-chat requires the window to be focused too (REQ-CHAT-181)", () => {
    presence.setComposeFocus('c1');
    presence.windowFocused = false;
    expect(presence.stateFor('c1')).toBe('absent');
  });

  it('only one conversation can be present-in-chat at a time (REQ-CHAT-184)', () => {
    presence.windowFocused = true;
    presence.setComposeFocus('c1');
    presence.setComposeFocus('c2');
    expect(presence.stateFor('c1')).toBe('present-elsewhere');
    expect(presence.stateFor('c2')).toBe('present-in-chat');
  });
});
