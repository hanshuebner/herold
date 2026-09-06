/**
 * Unit tests for navigateBackFromThread (re #83, re #294).
 *
 * The helper guards three navigation cases:
 *  (a) thread-window route: stay on the thread (never go to /mail).
 *  (b) a prior list route is known (router.lastListPath set): push it.
 *  (c) no prior list route (thread opened directly by URL): fall back to
 *      the mail listing for the current folder.
 *
 * (b) replaced a `window.history.back()` call (re #294): back() reused the
 * existing list history entry in place rather than pushing a new one, which
 * left the thread's own entry stranded ahead of the current position -- a
 * subsequent browser Back press skipped over the thread instead of landing
 * on it. Pushing `lastListPath` keeps the thread entry directly behind the
 * newly-pushed list entry, so Back reaches the thread.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { navigateBackFromThread } from './navigate-back';

// ── hoisted fixtures ───────────────────────────────────────────────────────────

const { routerMock, mailMock } = vi.hoisted(() => {
  const routerMock = {
    parts: ['mail'] as readonly string[],
    lastListPath: null as string | null,
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
  routerMock.lastListPath = null;
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

    it('does NOT push lastListPath from thread-window', () => {
      routerMock.lastListPath = '/mail';
      routerMock.parts = ['thread-window', 'tid-xyz'];
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/thread-window/tid-xyz');
      expect(routerMock.navigate).not.toHaveBeenCalledWith('/mail');
    });
  });

  describe('(b) a prior list route is known — push it, never window.history.back()', () => {
    it('pushes router.lastListPath via router.navigate', () => {
      routerMock.lastListPath = '/mail/folder/sent';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/mail/folder/sent');
    });

    it('pushes a search route recorded as lastListPath, preserving the query', () => {
      routerMock.lastListPath = '/mail/search?q=invoice';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/mail/search?q=invoice');
    });

    it('does not call window.history.back() (re #294 -- back() corrupts the stack)', () => {
      const backSpy = vi.spyOn(window.history, 'back').mockImplementation(() => {});
      routerMock.lastListPath = '/mail';
      navigateBackFromThread();
      expect(backSpy).not.toHaveBeenCalled();
      backSpy.mockRestore();
    });
  });

  describe('(c) no prior list route — fall back to the mail listing', () => {
    it('navigates to /mail when the folder is inbox', () => {
      mailMock.listFolder = 'inbox';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/mail');
    });

    it('navigates to /mail/folder/<name> for non-inbox folders', () => {
      mailMock.listFolder = 'sent';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith('/mail/folder/sent');
    });

    it('percent-encodes a folder name that contains special characters', () => {
      mailMock.listFolder = 'my folder/sub';
      navigateBackFromThread();
      expect(routerMock.navigate).toHaveBeenCalledWith(
        '/mail/folder/' + encodeURIComponent('my folder/sub'),
      );
    });
  });
});
