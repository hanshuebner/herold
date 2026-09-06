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

  // hashchange fires as a separate task in both real browsers and
  // happy-dom, so router.current only reflects a navigate() call after an
  // await; replace() updates it synchronously and is used to set up the
  // "current route before" state deterministically.
  describe('lastListPath (re #294)', () => {
    it('records a non-thread route left via replace()', () => {
      router.replace('/mail/folder/sent');
      router.replace('/mail/thread/t1');
      expect(router.lastListPath).toBe('/mail/folder/sent');
    });

    it('preserves the query string of a search route', () => {
      router.replace('/mail/search?q=invoice');
      router.replace('/mail/thread/t1');
      expect(router.lastListPath).toBe('/mail/search?q=invoice');
    });

    it('does not overwrite lastListPath when moving from one thread to another', () => {
      router.replace('/mail/folder/sent');
      router.replace('/mail/thread/t1');
      router.replace('/mail/thread/t2');
      expect(router.lastListPath).toBe('/mail/folder/sent');
    });

    it('leaving a thread back to a list route does not overwrite lastListPath with the thread path', () => {
      router.replace('/mail');
      router.replace('/mail/thread/t1');
      router.replace('/mail');
      expect(router.lastListPath).toBe('/mail');
    });

    it('navigate() eventually records lastListPath once the hashchange fires', async () => {
      router.replace('/mail/folder/sent');
      router.navigate('/mail/thread/t1');
      await new Promise((r) => window.addEventListener('hashchange', r, { once: true }));
      expect(router.parts).toEqual(['mail', 'thread', 't1']);
      expect(router.lastListPath).toBe('/mail/folder/sent');
    });
  });
});
