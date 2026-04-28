/**
 * Tests for the layered keyboard engine. The engine has one
 * document-level keydown listener, a stack of layer maps, and three
 * sharp edges:
 *   1. shadowing — top-of-stack wins; pop restores the previous layer
 *   2. focus carve-outs — single-key shortcuts skip when an input has
 *      focus, except Escape and Mod+Enter
 *   3. two-key 'g X' sequences with a 1s timeout
 *
 * The exported singleton attaches its listener on init(), so each test
 * registers via the public surface and synthesises KeyboardEvents on
 * document.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { keyboard } from './engine.svelte';

function press(opts: {
  key: string;
  meta?: boolean;
  ctrl?: boolean;
  shift?: boolean;
  alt?: boolean;
  target?: EventTarget;
}): KeyboardEvent {
  const ev = new KeyboardEvent('keydown', {
    key: opts.key,
    metaKey: opts.meta ?? false,
    ctrlKey: opts.ctrl ?? false,
    shiftKey: opts.shift ?? false,
    altKey: opts.alt ?? false,
    bubbles: true,
    cancelable: true,
  });
  (opts.target ?? document).dispatchEvent(ev);
  return ev;
}

beforeEach(() => {
  // Reset every layer above the global one between tests by popping
  // private state — we don't have a public reset, but we DO have
  // pushLayer's pop function. Construct a sentinel layer, pop it, and
  // any leaked state from prior tests goes with the rebuild.
  // (Cheaper than re-constructing the singleton.)
  // Each test that registers via pushLayer captures its own pop fn.
  keyboard.init();
});

describe('keyboard engine', () => {
  it('fires the registered binding on a single key', () => {
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'j', action }]);
    press({ key: 'j' });
    expect(action).toHaveBeenCalledOnce();
    pop();
  });

  it('top-of-stack layer shadows the layer below', () => {
    const lower = vi.fn();
    const upper = vi.fn();
    const popLower = keyboard.pushLayer([{ key: 'j', action: lower }]);
    const popUpper = keyboard.pushLayer([{ key: 'j', action: upper }]);
    press({ key: 'j' });
    expect(upper).toHaveBeenCalledOnce();
    expect(lower).not.toHaveBeenCalled();
    popUpper();
    press({ key: 'j' });
    expect(lower).toHaveBeenCalledOnce();
    popLower();
  });

  it('skips single-key bindings when focus is in an input', () => {
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'j', action }]);
    press({ key: 'j', target: input });
    expect(action).not.toHaveBeenCalled();
    pop();
    input.remove();
  });

  it('passes Escape through input focus carve-out', () => {
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'Escape', action }]);
    press({ key: 'Escape', target: input });
    expect(action).toHaveBeenCalledOnce();
    pop();
    input.remove();
  });

  it('passes Mod+Enter through input focus carve-out', () => {
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'Mod+Enter', action }]);
    press({ key: 'Enter', meta: true, target: input });
    expect(action).toHaveBeenCalledOnce();
    pop();
    input.remove();
  });

  it("dispatches 'g i' as a two-key sequence", () => {
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'g i', action }]);
    press({ key: 'g' });
    press({ key: 'i' });
    expect(action).toHaveBeenCalledOnce();
    pop();
  });

  it("does not fire 'g X' when modifiers are held on the 'g' (Cmd+G is browser find)", () => {
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'g i', action }]);
    press({ key: 'g', meta: true });
    press({ key: 'i' });
    expect(action).not.toHaveBeenCalled();
    pop();
  });

  it('canonicalises modifier order Mod+Alt+Shift+Key', () => {
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'Mod+Shift+Enter', action }]);
    press({ key: 'Enter', meta: true, shift: true });
    expect(action).toHaveBeenCalledOnce();
    pop();
  });

  it('preventDefault is called when a binding fires', () => {
    const action = vi.fn();
    const pop = keyboard.pushLayer([{ key: 'j', action }]);
    const ev = press({ key: 'j' });
    expect(ev.defaultPrevented).toBe(true);
    pop();
  });
});
