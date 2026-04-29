/**
 * Unit tests for the NotificationSounds helper — REQ-PUSH-95..99, REQ-SET-16.
 *
 * Coverage:
 *   1. hydrate() reads localStorage and sets enabled accordingly.
 *   2. setEnabled() persists to localStorage and updates the state.
 *   3. play() is a no-op when enabled is false.
 *   4. play() calls Audio.play().
 *   5. play() resets currentTime on rapid repeats (same element reused).
 *   6. play() swallows the Audio.play() Promise rejection silently.
 *   7. stop() pauses and resets currentTime.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { _internals_forTest } from './sounds.svelte';

const { NotificationSounds, STORAGE_KEY } = _internals_forTest;

// ── Audio mock ─────────────────────────────────────────────────────────────

function makeAudioMock() {
  return {
    currentTime: 0,
    play: vi.fn().mockResolvedValue(undefined),
    pause: vi.fn(),
    constructCount: 0,
  };
}

let audioMock = makeAudioMock();

// Audio must be stubbed as a constructor (class) so `new Audio(...)` works.
// We track instantiation count on audioMock.constructCount manually because
// a plain class stub is not a vitest spy.
beforeEach(() => {
  audioMock = makeAudioMock();
  class AudioStub {
    constructor(_src?: string) {
      audioMock.constructCount++;
      // Proxy property writes back to the shared audioMock.
      Object.defineProperty(this, 'currentTime', {
        get: () => audioMock.currentTime,
        set: (v: number) => { audioMock.currentTime = v; },
      });
      Object.defineProperty(this, 'play', { get: () => audioMock.play });
      Object.defineProperty(this, 'pause', { get: () => audioMock.pause });
    }
  }
  vi.stubGlobal('Audio', AudioStub);
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

// ── 1. hydrate ─────────────────────────────────────────────────────────────

describe('NotificationSounds.hydrate', () => {
  it('defaults to true when localStorage has no entry', () => {
    const snd = new NotificationSounds();
    snd.hydrate();
    expect(snd.enabled).toBe(true);
  });

  it('reads false from localStorage', () => {
    localStorage.setItem(STORAGE_KEY, 'false');
    const snd = new NotificationSounds();
    snd.hydrate();
    expect(snd.enabled).toBe(false);
  });

  it('reads true from localStorage', () => {
    localStorage.setItem(STORAGE_KEY, 'true');
    const snd = new NotificationSounds();
    snd.hydrate();
    expect(snd.enabled).toBe(true);
  });
});

// ── 2. setEnabled ──────────────────────────────────────────────────────────

describe('NotificationSounds.setEnabled', () => {
  it('sets enabled to false and persists to localStorage', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(false);
    expect(snd.enabled).toBe(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBe('false');
  });

  it('sets enabled to true and persists to localStorage', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    expect(snd.enabled).toBe(true);
    expect(localStorage.getItem(STORAGE_KEY)).toBe('true');
  });
});

// ── 3. play() no-op when disabled ─────────────────────────────────────────

describe('NotificationSounds.play — master toggle off', () => {
  it('does not call Audio.play() when enabled is false', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(false);
    snd.play('mail');
    expect(audioMock.play).not.toHaveBeenCalled();
  });
});

// ── 4. play() invokes Audio.play ───────────────────────────────────────────

describe('NotificationSounds.play — enabled', () => {
  it('calls Audio.play() for the mail sound', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    snd.play('mail');
    expect(audioMock.play).toHaveBeenCalledTimes(1);
  });

  it('calls Audio.play() for the chat sound', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    snd.play('chat');
    expect(audioMock.play).toHaveBeenCalledTimes(1);
  });

  it('calls Audio.play() for the call sound', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    snd.play('call');
    expect(audioMock.play).toHaveBeenCalledTimes(1);
  });
});

// ── 5. Rapid repeats reuse the same element + reset currentTime ────────────

describe('NotificationSounds.play — element reuse', () => {
  it('resets currentTime to 0 on the second play call', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    snd.play('chat');
    // Simulate audio having advanced.
    audioMock.currentTime = 1.5;
    snd.play('chat');
    // currentTime should have been set back to 0 before the second play().
    expect(audioMock.currentTime).toBe(0);
    // Audio constructor should have been called only once (element reused).
    expect(audioMock.constructCount).toBe(1);
  });
});

// ── 6. play() swallows rejection ───────────────────────────────────────────

describe('NotificationSounds.play — rejection swallowed', () => {
  it('does not propagate a rejected play() Promise', async () => {
    audioMock.play.mockRejectedValue(new DOMException('Not allowed by user'));
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    // Must not throw.
    expect(() => snd.play('mail')).not.toThrow();
    // Allow the microtask to settle so the rejection is observed.
    await Promise.resolve();
    // No uncaught rejection should have been thrown.
  });
});

// ── 7. stop() ─────────────────────────────────────────────────────────────

describe('NotificationSounds.stop', () => {
  it('pauses and resets currentTime for an element that has been created', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(true);
    snd.play('call');
    audioMock.currentTime = 2.0;
    snd.stop('call');
    expect(audioMock.pause).toHaveBeenCalled();
    expect(audioMock.currentTime).toBe(0);
  });

  it('is a no-op when the element has never been created', () => {
    const snd = new NotificationSounds();
    expect(() => snd.stop('call')).not.toThrow();
  });
});
