/**
 * Regression test for issue #52 — drag-and-drop must move the whole thread,
 * not just the single representative email shown in the collapsed list.
 *
 * When `collapseThreads: true` is used by the list view, `listEmailIds`
 * contains exactly one email per thread (the most recent message). If the
 * user drags that row to a mailbox label, `bulkMoveToMailbox` must expand
 * to all emails in the thread before issuing `Email/set`.
 *
 * The expansion relies on `this.threads` being populated. If the thread has
 * never been opened in the reader, `threads` is empty for that thread.
 * The fix adds a `Thread/get` prefetch inside `bulkMoveToMailbox` to ensure
 * the thread membership is loaded before `expandToThreadIds` runs.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

// ── Stable mocks (hoisted so vi.mock factory closures can reference them) ───
const { batchMock } = vi.hoisted(() => ({
  batchMock: vi.fn(),
}));

vi.mock('../jmap/client', () => ({
  jmap: { batch: batchMock },
  // strict validates invocations; our mock returns well-formed ones so just pass through.
  strict: (responses: unknown[]) => responses,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
    },
  },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(), registerChangeHandler: vi.fn() },
}));

vi.mock('../notifications/sounds.svelte', () => ({
  sounds: { play: vi.fn() },
}));

vi.mock('../notifications/cue-gates', () => ({
  shouldPlayMailCue: vi.fn().mockReturnValue(false),
}));

vi.mock('../router/router.svelte', () => ({
  router: {
    navigate: vi.fn(),
    parts: [],
    matches: vi.fn().mockReturnValue(false),
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (k: string) => k,
  localeTag: () => 'en',
}));

// Import the real store after all mocks are in place.
import { mail } from './store.svelte';
import type { Email, Mailbox } from './types';

// ── Minimal stubs ─────────────────────────────────────────────────────────────

function makeEmail(id: string, threadId: string, mailboxId: string): Email {
  return {
    id,
    threadId,
    mailboxIds: { [mailboxId]: true },
    keywords: {},
    from: null,
    to: null,
    subject: `Subject for ${id}`,
    preview: '',
    receivedAt: '2024-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
  };
}

function makeMailbox(id: string, role: string, name: string): Mailbox {
  return {
    id,
    name,
    role,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
  };
}

/**
 * Inspect the first method name registered in a builder callback.
 * The builder receives an object with a `call(method, args)` stub;
 * we run the callback and return the first call's [method, args] pair.
 */
function sniffBuilder(builderFn: (b: unknown) => void): [string, Record<string, unknown>] {
  let captured: [string, Record<string, unknown>] = ['', {}];
  builderFn({
    call(method: string, args: Record<string, unknown>) {
      if (!captured[0]) captured = [method, args];
      return { ref: () => null };
    },
  });
  return captured;
}

/**
 * Build a `jmap.batch` mock that routes responses by the JMAP method name
 * registered in each builder call. Callers provide a map from method name
 * to the `[method, result, callId]` invocation to return.
 *
 * Any method not covered by the map returns a generic empty success so
 * background refreshes (Mailbox/get) don't crash the test.
 */
function makeBatchMock(routes: Record<string, unknown[]>): typeof batchMock {
  return vi.fn().mockImplementation(async (builderFn: (b: unknown) => void) => {
    const [method] = sniffBuilder(builderFn);
    const response = routes[method] ?? [method, {}, 'c0'];
    return {
      responses: [response],
      sessionState: 'state1',
    };
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  // Reset all mutable store state between tests.
  mail.emails = new Map();
  mail.threads = new Map();
  mail.mailboxes = new Map();
  mail.listEmailIds = [];
  mail.listFolder = 'inbox';
  mail.listLoadStatus = 'idle';
  mail.listFocusedIndex = -1;
  mail.listSelectedIds = new Set();
});

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('bulkMoveToMailbox — whole-thread expansion (re #52)', () => {
  it('fetches Thread/get for uncached threads and moves all emails in the thread', async () => {
    // Arrange
    const inboxId = 'mb-inbox';
    const targetId = 'mb-label';
    mail.mailboxes = new Map([
      [inboxId, makeMailbox(inboxId, 'inbox', 'Inbox')],
      [targetId, makeMailbox(targetId, '', 'Work')],
    ]);
    // Three emails in the same thread; the list (collapseThreads) only exposes e3.
    mail.emails = new Map([
      ['e1', makeEmail('e1', 't1', inboxId)],
      ['e2', makeEmail('e2', 't1', inboxId)],
      ['e3', makeEmail('e3', 't1', inboxId)],
    ]);
    mail.listEmailIds = ['e3']; // representative
    mail.listFolder = 'inbox';
    // threads map is empty — the user never opened this thread in the reader.
    expect(mail.threads.has('t1')).toBe(false);

    // Track which email IDs were passed to Email/set.
    let movedIds: string[] = [];

    batchMock.mockImplementation(async (builderFn: (b: unknown) => void) => {
      const [method, args] = sniffBuilder(builderFn);
      if (method === 'Thread/get') {
        // Return thread membership so expandToThreadIds can expand e3 → e1/e2/e3.
        return {
          responses: [
            ['Thread/get', { list: [{ id: 't1', emailIds: ['e1', 'e2', 'e3'] }] }, 'c-thread'],
          ],
          sessionState: 's1',
        };
      }
      if (method === 'Email/set') {
        movedIds = Object.keys((args['update'] as Record<string, unknown>) ?? {});
        return {
          responses: [
            [
              'Email/set',
              { updated: Object.fromEntries(movedIds.map((id) => [id, {}])), notUpdated: {} },
              'c-email',
            ],
          ],
          sessionState: 's2',
        };
      }
      // Mailbox/get (background refresh) and anything else: return an empty success.
      return {
        responses: [[method, { list: [] }, 'c-other']],
        sessionState: 's3',
      };
    });

    // Act
    await mail.bulkMoveToMailbox(['e3'], targetId);

    // Assert: Email/set must cover all three thread emails, not just the dragged one.
    expect(new Set(movedIds)).toEqual(new Set(['e1', 'e2', 'e3']));
  });

  it('does not issue Thread/get when thread is already cached', async () => {
    const inboxId = 'mb-inbox';
    const targetId = 'mb-label';
    mail.mailboxes = new Map([
      [inboxId, makeMailbox(inboxId, 'inbox', 'Inbox')],
      [targetId, makeMailbox(targetId, '', 'Work')],
    ]);
    mail.emails = new Map([
      ['e1', makeEmail('e1', 't1', inboxId)],
      ['e2', makeEmail('e2', 't1', inboxId)],
    ]);
    // Pre-populate threads — simulates having opened the thread reader before dragging.
    mail.threads = new Map([['t1', { id: 't1', emailIds: ['e1', 'e2'] }]]);
    mail.listEmailIds = ['e2'];
    mail.listFolder = 'inbox';

    const calledMethods: string[] = [];
    batchMock.mockImplementation(async (builderFn: (b: unknown) => void) => {
      const [method] = sniffBuilder(builderFn);
      calledMethods.push(method);
      if (method === 'Email/set') {
        return {
          responses: [
            ['Email/set', { updated: { e1: {}, e2: {} }, notUpdated: {} }, 'c-email'],
          ],
          sessionState: 's1',
        };
      }
      return { responses: [[method, { list: [] }, 'c-other']], sessionState: 's2' };
    });

    await mail.bulkMoveToMailbox(['e2'], targetId);

    // Thread/get must NOT have been issued; the cached entry was sufficient.
    expect(calledMethods).not.toContain('Thread/get');
    expect(calledMethods).toContain('Email/set');
  });

  it('still moves the representative email if Thread/get fails (best-effort)', async () => {
    const inboxId = 'mb-inbox';
    const targetId = 'mb-label';
    mail.mailboxes = new Map([
      [inboxId, makeMailbox(inboxId, 'inbox', 'Inbox')],
      [targetId, makeMailbox(targetId, '', 'Work')],
    ]);
    mail.emails = new Map([['e3', makeEmail('e3', 't1', inboxId)]]);
    mail.listEmailIds = ['e3'];
    mail.listFolder = 'inbox';

    let movedIds: string[] = [];
    batchMock.mockImplementation(async (builderFn: (b: unknown) => void) => {
      const [method, args] = sniffBuilder(builderFn);
      if (method === 'Thread/get') {
        throw new Error('simulated network failure');
      }
      if (method === 'Email/set') {
        movedIds = Object.keys((args['update'] as Record<string, unknown>) ?? {});
        return {
          responses: [
            [
              'Email/set',
              { updated: Object.fromEntries(movedIds.map((id) => [id, {}])), notUpdated: {} },
              'c-email',
            ],
          ],
          sessionState: 's1',
        };
      }
      return { responses: [[method, { list: [] }, 'c-other']], sessionState: 's2' };
    });

    // Must not throw — the move proceeds best-effort even when Thread/get fails.
    await expect(mail.bulkMoveToMailbox(['e3'], targetId)).resolves.toBeUndefined();

    // The representative email (e3) must still be moved.
    expect(movedIds).toContain('e3');
  });
});
