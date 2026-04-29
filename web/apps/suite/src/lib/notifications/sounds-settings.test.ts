/**
 * Unit tests for the notification-sounds localStorage persistence (REQ-SET-16).
 *
 * These tests exercise the NotificationSounds helper directly (the same
 * singleton that the Notifications settings section reads/writes) rather
 * than mounting the full SettingsView Svelte component, since the component
 * contains many unrelated sections and would require mocking a large
 * surface of the JMAP/auth stack.
 *
 * Coverage:
 *   1. hydrate() on fresh localStorage -> enabled defaults to true.
 *   2. setEnabled(false) -> localStorage entry written; enabled becomes false.
 *   3. After setEnabled(false), a fresh hydrate() -> enabled is false.
 *   4. setEnabled(true) -> localStorage entry written; enabled becomes true.
 *   5. Helper enabled state is reflected immediately after setEnabled().
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { _internals_forTest } from './sounds.svelte';

const { NotificationSounds, STORAGE_KEY } = _internals_forTest;

class AudioStub {
  currentTime = 0;
  play = vi.fn().mockResolvedValue(undefined);
  pause = vi.fn();
}

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal('Audio', AudioStub);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('sounds settings persistence', () => {
  it('defaults to enabled when localStorage is empty', () => {
    const snd = new NotificationSounds();
    snd.hydrate();
    expect(snd.enabled).toBe(true);
  });

  it('setEnabled(false) writes to localStorage and updates enabled', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(false);
    expect(snd.enabled).toBe(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBe('false');
  });

  it('a subsequent hydrate() picks up the persisted false value', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(false);
    // Simulate a second instance (e.g. page reload) reading the stored pref.
    const snd2 = new NotificationSounds();
    snd2.hydrate();
    expect(snd2.enabled).toBe(false);
  });

  it('setEnabled(true) writes to localStorage and updates enabled', () => {
    localStorage.setItem(STORAGE_KEY, 'false');
    const snd = new NotificationSounds();
    snd.hydrate();
    expect(snd.enabled).toBe(false);
    snd.setEnabled(true);
    expect(snd.enabled).toBe(true);
    expect(localStorage.getItem(STORAGE_KEY)).toBe('true');
  });

  it('enabled reflects the change immediately after setEnabled()', () => {
    const snd = new NotificationSounds();
    snd.setEnabled(false);
    expect(snd.enabled).toBe(false);
    snd.setEnabled(true);
    expect(snd.enabled).toBe(true);
  });
});
