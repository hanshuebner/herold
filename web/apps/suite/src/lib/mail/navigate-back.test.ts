/**
 * Unit tests for navigateBackFromThread (re #83).
 *
 * The helper guards three navigation cases:
 *  (a) thread-window route: stay on the thread (never go to /mail).
 *  (b) no browser history: fall back to the inbox listing (/mail).
 *  (c) no browser history, non-inbox folder: fall back to the folder listing.
 *  (d) browser history available: call window.history.back().
 *
 * The thread-window guard (a) is the bug-fix path introduced in re #83.
 * Cases (b–d) are pre-existing behaviour, verified here so regressions are
 * caught before they reach the browser.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { navigateBackFromThread } from './navigate-back';

// ── hoisted fixtures ───────────────────────────────────────────────────────────

const { routerMock, mailMock } = vi.hoisted(() => {
  const routerMock = {
    parts: ['mail'] as readonly string[],
    navigate: vi.fn(),
  };
  const mailMock = {
    listFolder: 'inbox' as string,
  };
  return { routerMock, mailMock };
});

// ── module mocks ───────────────────────────────────────────────────────────────

vi.mock('../router/router.svelte', () => ({ router: routerMock }));
vi.mock('./store.svelte', () => ({ mail: mailMock }));

// ── setup ──────────────────────────────────────────────────────────────────────

beforeEach(() => {
  routerMock.parts = ['mail'];
  routerMock.navigate.mockClear();
  mailMock.listFolder = 'inbox';
});

// ── tests ──────────────────────────────────────────────────────────────────────

describe('navigateBackFromThread', () => {
  describe('(a) thread-window route — stay on the thread', () => {
    it('navigates to the same thread-window URL', () => {
      routerMock.parts = ['thread-window', 'tid-abc123'];
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/thread-window/tid-abc123');
    });

    it('handles a missing threadId segment gracefully', () => {
      routerMock.parts = ['thread-window'];
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/thread-window/');
    });

    it('percent-encodes a threadId that contains URI-special characters', () => {
      const raw = 'tid/has+special=chars';
      routerMock.parts = ['thread-window', raw];
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith(
        '/thread-window/' + encodeURIComponent(raw),
      );
    });

    it('does NOT call history.back() from thread-window', () => {
      const backSpy = vi.spyOn(window.history, 'back').mockImplementation(() => {});
      routerMock.parts = ['thread-window', 'tid-xyz'];
      navigateBackFromThread();
      expect(backSpy).not.toHaveBeenCalled();
      backSpy.mockRestore();
    });
  });

  describe('(b/c) no browser history — fall back to mail listing', () => {
    // happy-dom's history.length starts at 1 (no prior entries), so tests
    // in this block naturally exercise the fallback path without manipulation.

    it('(b) navigates to /mail when the folder is inbox', () => {
      mailMock.listFolder = 'inbox';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/mail');
    });

    it('(c) navigates to /mail/folder/<name> for non-inbox folders', () => {
      mailMock.listFolder = 'sent';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/mail/folder/sent');
    });

    it('(c) percent-encodes a folder name that contains special characters', () => {
      mailMock.listFolder = 'my folder/sub';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith(
        '/mail/folder/' + encodeURIComponent('my folder/sub'),
      );
    });
  });

  describe('(d) browser history available — call history.back()', () => {
    let backSpy: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
      backSpy = vi.spyOn(window.history, 'back').mockImplementation(() => {});
      // Push two states so history.length > 1.
      window.history.pushState(null, '', '#/prev');
      window.history.pushState(null, '', '#/curr');
    });

    afterEach(() => {
      backSpy.mockRestore();
    });

    it('calls window.history.back() and does NOT navigate via router', () => {
      expect(window.history.length).toBeGreaterThan(1);
      navigateBackFromThread();
      expect(backSpy).toHaveBeenCalledOnce();
      expect(routerMock.navigate).not.toHaveBeenCalled();
    });
  });
});
