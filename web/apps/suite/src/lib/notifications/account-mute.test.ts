/**
 * Unit tests for the per-sub-account notification mute persistence
 * (issue #212, REQ-MAIL-SUB-06: "its own notification channel, mutable
 * independently"). Mirrors sounds-settings.test.ts's pattern: drive the
 * singleton directly against real localStorage rather than mounting a
 * component.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('../auth/auth.svelte', () => ({
  auth: { session: { username: 'alice@example.local' } },
  registerAccountResetCallback: vi.fn(),
}));

beforeEach(() => {
  localStorage.clear();
  vi.resetModules();
});

describe('accountNotificationMute persistence', () => {
  it('defaults to unmuted (absent from the set) when localStorage is empty', async () => {
    const { accountNotificationMute } = await import('./account-mute.svelte');
    accountNotificationMute.hydrate();
    expect(accountNotificationMute.isMuted('acct-sub-1')).toBe(false);
  });

  it('setMuted(true) mutes immediately and persists to localStorage', async () => {
    const { accountNotificationMute } = await import('./account-mute.svelte');
    accountNotificationMute.hydrate();
    accountNotificationMute.setMuted('acct-sub-1', true);
    expect(accountNotificationMute.isMuted('acct-sub-1')).toBe(true);
    expect(localStorage.getItem('herold.suite.alice@example.local.sub-account-notifications-muted')).toBe(
      '["acct-sub-1"]',
    );
  });

  it('a fresh instance (module reset, simulating page reload) picks up the persisted mute', async () => {
    const first = await import('./account-mute.svelte');
    first.accountNotificationMute.hydrate();
    first.accountNotificationMute.setMuted('acct-sub-1', true);

    vi.resetModules();
    const second = await import('./account-mute.svelte');
    second.accountNotificationMute.hydrate();
    expect(second.accountNotificationMute.isMuted('acct-sub-1')).toBe(true);
  });

  it('setMuted(false) unmutes and removes the id from the persisted set', async () => {
    const { accountNotificationMute } = await import('./account-mute.svelte');
    accountNotificationMute.hydrate();
    accountNotificationMute.setMuted('acct-sub-1', true);
    accountNotificationMute.setMuted('acct-sub-1', false);
    expect(accountNotificationMute.isMuted('acct-sub-1')).toBe(false);
    expect(localStorage.getItem('herold.suite.alice@example.local.sub-account-notifications-muted')).toBe(
      '[]',
    );
  });

  it('mutes are independent per accountId', async () => {
    const { accountNotificationMute } = await import('./account-mute.svelte');
    accountNotificationMute.hydrate();
    accountNotificationMute.setMuted('acct-sub-1', true);
    expect(accountNotificationMute.isMuted('acct-sub-1')).toBe(true);
    expect(accountNotificationMute.isMuted('acct-sub-2')).toBe(false);
  });
});
