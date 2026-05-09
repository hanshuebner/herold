/**
 * Image allow-list / isImageAllowed semantics (issue #101).
 *
 * The "Always from <sender>" button is supposed to let the named
 * sender's external images load on every subsequent open of any
 * message from that sender. Pre-#101 the button stored the entry in
 * settings.imageAllowList correctly, but isImageAllowed() returned
 * early in 'never' mode (the default) without consulting the list,
 * so the user-visible behaviour was "click the button, reload, the
 * banner is back" — i.e. the setting did not persist.
 *
 * The fix makes the allow-list an explicit override that fires in
 * BOTH 'never' and 'per-sender' modes; only 'always' bypasses it
 * (because everything is allowed in that mode).
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

// Stub auth so storageKey() resolves to a stable per-account namespace.
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: { username: 'testuser@example.test' },
  },
}));

// Stub i18n so settings.hydrate() can resolve a locale.
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { locale: 'en' },
  detectLocale: () => 'en',
  LOCALES: ['en', 'de'],
}));

// Lazy import after mocks so the singleton picks them up.
async function freshSettings() {
  vi.resetModules();
  const mod = await import('./settings.svelte');
  // Clear any localStorage for the test account to start from defaults.
  for (const key of Object.keys(localStorage)) {
    if (key.includes('testuser@example.test')) localStorage.removeItem(key);
  }
  return mod.settings;
}

describe('settings.isImageAllowed (issue #101)', () => {
  beforeEach(() => {
    for (const key of Object.keys(localStorage)) {
      if (key.includes('testuser@example.test')) localStorage.removeItem(key);
    }
  });

  it("returns true for senders on the allow-list even when default is 'never'", async () => {
    const settings = await freshSettings();
    settings.hydrate();
    settings.setImageLoadDefault('never');
    settings.addImageAllowedSender('alice@example.test');

    expect(settings.isImageAllowed('alice@example.test')).toBe(true);
    // Other senders are still blocked.
    expect(settings.isImageAllowed('bob@example.test')).toBe(false);
  });

  it("returns true for senders on the allow-list when default is 'per-sender'", async () => {
    const settings = await freshSettings();
    settings.hydrate();
    settings.setImageLoadDefault('per-sender');
    settings.addImageAllowedSender('alice@example.test');

    expect(settings.isImageAllowed('alice@example.test')).toBe(true);
    expect(settings.isImageAllowed('bob@example.test')).toBe(false);
  });

  it("returns true for any sender when default is 'always'", async () => {
    const settings = await freshSettings();
    settings.hydrate();
    settings.setImageLoadDefault('always');

    expect(settings.isImageAllowed('alice@example.test')).toBe(true);
    expect(settings.isImageAllowed('whoever@nowhere.test')).toBe(true);
  });

  it("returns false for unlisted senders when default is 'never'", async () => {
    const settings = await freshSettings();
    settings.hydrate();
    settings.setImageLoadDefault('never');

    expect(settings.isImageAllowed('alice@example.test')).toBe(false);
    expect(settings.isImageAllowed(null)).toBe(false);
    expect(settings.isImageAllowed(undefined)).toBe(false);
    expect(settings.isImageAllowed('')).toBe(false);
  });

  it('persists the allow-list across rehydrate (the original repro)', async () => {
    const settings = await freshSettings();
    settings.hydrate();
    settings.setImageLoadDefault('never');
    settings.addImageAllowedSender('alice@example.test');
    expect(settings.isImageAllowed('alice@example.test')).toBe(true);

    // Simulate page reload: re-hydrate from localStorage.
    const reloaded = await freshSettings();
    // Don't clear localStorage between — that's the whole point.
    // freshSettings clears, so we need a different approach: re-import
    // after seeding localStorage manually.
    const key = 'herold.suite.settings.testuser@example.test.imageAllowList';
    localStorage.setItem(key, JSON.stringify(['alice@example.test']));
    reloaded.hydrate();
    reloaded.setImageLoadDefault('never');
    expect(reloaded.isImageAllowed('alice@example.test')).toBe(true);
  });

  it('removeImageAllowedSender clears the override', async () => {
    const settings = await freshSettings();
    settings.hydrate();
    settings.setImageLoadDefault('never');
    settings.addImageAllowedSender('alice@example.test');
    expect(settings.isImageAllowed('alice@example.test')).toBe(true);

    settings.removeImageAllowedSender('alice@example.test');
    expect(settings.isImageAllowed('alice@example.test')).toBe(false);
  });
});
