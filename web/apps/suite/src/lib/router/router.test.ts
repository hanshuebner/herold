/**
 * Tests for the hash router. The exported `router` is a singleton whose
 * constructor reads window.location.hash; we exercise it via its public
 * surface (replace + matches + parts). happy-dom provides window /
 * history / location.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { router } from './router.svelte';

beforeEach(() => {
  router.replace('/mail');
});

describe('router', () => {
  it('navigates by setting the hash', () => {
    router.navigate('/mail/folder/sent');
    expect(window.location.hash).toBe('#/mail/folder/sent');
  });

  it('parts is the path split on /', () => {
    router.replace('/mail/thread/abc-123');
    expect(router.parts).toEqual(['mail', 'thread', 'abc-123']);
  });

  it("matches('mail') is true for /mail and /mail/...", () => {
    router.replace('/mail');
    expect(router.matches('mail')).toBe(true);
    router.replace('/mail/folder/sent');
    expect(router.matches('mail')).toBe(true);
  });

  it("matches('mail', 'folder', 'sent') is exact-prefix", () => {
    router.replace('/mail/folder/sent');
    expect(router.matches('mail', 'folder', 'sent')).toBe(true);
    expect(router.matches('mail', 'folder', 'drafts')).toBe(false);
    expect(router.matches('chat')).toBe(false);
  });

  it('matches returns false when prefix is longer than path', () => {
    router.replace('/mail');
    expect(router.matches('mail', 'thread', 'x')).toBe(false);
  });

  it('replace prepends a slash if missing', () => {
    router.replace('settings');
    expect(router.parts).toEqual(['settings']);
  });

  it('navigate prepends a slash if missing', () => {
    router.navigate('chat');
    expect(window.location.hash).toBe('#/chat');
  });

  it("matches('help') for /help and /help/:chapter", () => {
    router.replace('/help');
    expect(router.matches('help')).toBe(true);
    router.replace('/help/intro');
    expect(router.matches('help')).toBe(true);
    expect(router.parts).toEqual(['help', 'intro']);
  });

  it("parts exposes chapter and section for /help/:chapter/:section", () => {
    router.replace('/help/setup/install');
    expect(router.parts).toEqual(['help', 'setup', 'install']);
    expect(router.matches('help')).toBe(true);
  });
});
