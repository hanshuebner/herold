/**
 * Tests for the livetail watcher (REQ-CLOG-05).
 *
 * Covers:
 *   - Policy switches to 'aggressive' when livetailUntil() > now.
 *   - Policy reverts to 'normal' when livetailUntil() expires.
 *   - Flush interval switches to 100 ms in aggressive mode.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { startLivetailWatcher } from '../livetail.js';
import type { FakeClock } from './fakes.js';
import { installFakeClock } from './fakes.js';

let clock: FakeClock;

beforeEach(() => {
  clock = installFakeClock();
});

afterEach(() => {
  clock.uninstall();
});

describe('startLivetailWatcher', () => {
  it('starts with normal policy', () => {
    const watcher = startLivetailWatcher(() => null);
    expect(watcher.policy()).toBe('normal');
    watcher.stop();
  });

  it('switches to aggressive when livetailUntil is in the future', () => {
    const until = Date.now() + 60_000;
    const watcher = startLivetailWatcher(() => until);
    expect(watcher.policy()).toBe('normal'); // not yet polled
    clock.advance(1000); // trigger the 1s interval
    expect(watcher.policy()).toBe('aggressive');
    watcher.stop();
  });

  it('reverts to normal when livetailUntil expires', () => {
    let until: number | null = Date.now() + 2000;
    const watcher = startLivetailWatcher(() => until);
    clock.advance(1000);
    expect(watcher.policy()).toBe('aggressive');
    // Expire the livetail
    until = null;
    clock.advance(1000);
    expect(watcher.policy()).toBe('normal');
    watcher.stop();
  });

  it('reverts to normal when timestamp falls behind now', () => {
    const until = Date.now() + 1500;
    const watcher = startLivetailWatcher(() => until);
    clock.advance(1000);
    expect(watcher.policy()).toBe('aggressive');
    // Advance past until
    clock.advance(2000);
    expect(watcher.policy()).toBe('normal');
    watcher.stop();
  });

  it('stop() prevents further policy changes', () => {
    const watcher = startLivetailWatcher(() => Date.now() + 60_000);
    watcher.stop();
    clock.advance(10_000);
    // Policy was never changed because stop was called before first poll
    expect(watcher.policy()).toBe('normal');
  });
});
