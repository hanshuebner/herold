/**
 * Tests for loadMoreSearch() (issue #219): the search result list must page
 * past the first loaded window via infinite scroll instead of capping at
 * SEARCH_PAGE_SIZE (50) matches, mirroring loadMoreFolder() (issue #161).
 *
 * Covers:
 *   - position sent to Email/query is the current searchEmailIds.length, not
 *     a separately tracked cursor.
 *   - a returned page is APPENDED to searchEmailIds, not a replacement.
 *   - searchHasMore flips false once a short page (< page size) comes back.
 *   - a page that overlaps an already-loaded id is deduped on append.
 *   - searchLoadingMore is true only while the fetch is in flight.
 *   - no-ops when searchHasMore is false, a page is already in flight, or the
 *     search is not 'ready'.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email } from './types';

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch },
  strict: (r: unknown[]) => r,
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()) },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acc1' },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

function makeEmail(id: string, threadId: string): Email {
  return {
    id,
    threadId,
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: 'test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: `blob-${id}`,
  };
}

/** Build a jmap.batch mock that dispatches on call name, capturing the args
 * passed to Email/query so tests can assert `position`/`limit`. */
function makeBatch(opts: {
  emailQueryIds: string[];
  emailGetEmails?: Email[];
  onEmailQuery?: (args: Record<string, unknown>) => void;
}): (builder: unknown) => Promise<{ responses: unknown[] }> {
  return async (builder: unknown) => {
    let counter = 0;
    const calls: { name: string; args: Record<string, unknown> }[] = [];
    const api = {
      call: (name: string, args: Record<string, unknown>) => {
        calls.push({ name, args });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (builder as (b: typeof api) => void)(api);

    const responses: [string, unknown, string][] = [];
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i]!;
      if (c.name === 'Email/query') {
        opts.onEmailQuery?.(c.args);
        responses.push([c.name, { ids: opts.emailQueryIds }, `c${i}`]);
      } else if (c.name === 'Email/get') {
        responses.push([c.name, { list: opts.emailGetEmails ?? [] }, `c${i}`]);
      } else if (c.name === 'Thread/get') {
        responses.push([c.name, { list: [] }, `c${i}`]);
      } else {
        responses.push([c.name, { list: [] }, `c${i}`]);
      }
    }
    return { responses };
  };
}

describe('loadMoreSearch (issue #219)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
  });

  it('no-op when searchHasMore is false', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1'];
    mail.searchHasMore = false;

    await mail.loadMoreSearch();
    expect(batch).not.toHaveBeenCalled();
  });

  it('no-op when the search is not ready', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'loading';
    mail.searchEmailIds = ['e1'];
    mail.searchHasMore = true;

    await mail.loadMoreSearch();
    expect(batch).not.toHaveBeenCalled();
  });

  it('no-op when a page is already in flight', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1'];
    mail.searchHasMore = true;
    mail.searchLoadingMore = true;

    await mail.loadMoreSearch();
    expect(batch).not.toHaveBeenCalled();
  });

  it('requests position = current searchEmailIds.length (not a separate cursor)', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1', 'e2', 'e3'];
    mail.searchHasMore = true;

    let capturedPosition: unknown;
    let capturedLimit: unknown;
    batch.mockImplementation(
      makeBatch({
        emailQueryIds: [],
        onEmailQuery: (args) => {
          capturedPosition = args.position;
          capturedLimit = args.limit;
        },
      }),
    );

    await mail.loadMoreSearch();
    expect(capturedPosition).toBe(3);
    expect(capturedLimit).toBe(50);
  });

  it('appends the returned page to searchEmailIds rather than replacing it', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1', 'e2'];
    mail.searchHasMore = true;
    mail.emails = new Map([
      ['e1', makeEmail('e1', 't1')],
      ['e2', makeEmail('e2', 't2')],
    ]);

    batch.mockImplementation(
      makeBatch({
        emailQueryIds: ['e3', 'e4'],
        emailGetEmails: [makeEmail('e3', 't3'), makeEmail('e4', 't4')],
      }),
    );

    await mail.loadMoreSearch();
    expect(mail.searchEmailIds).toEqual(['e1', 'e2', 'e3', 'e4']);
    expect(mail.emails.has('e3')).toBe(true);
    expect(mail.emails.has('e4')).toBe(true);
    // Pre-existing cache entries survive the merge.
    expect(mail.emails.has('e1')).toBe(true);
  });

  it('sets searchHasMore false when the returned page is shorter than the page size', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = Array.from({ length: 50 }, (_, i) => `e${i}`);
    mail.searchHasMore = true;

    batch.mockImplementation(
      makeBatch({ emailQueryIds: ['e50', 'e51'] }), // 2 < 50 -> no more
    );

    await mail.loadMoreSearch();
    expect(mail.searchHasMore).toBe(false);
  });

  it('keeps searchHasMore true when a full page comes back', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = Array.from({ length: 50 }, (_, i) => `e${i}`);
    mail.searchHasMore = true;

    const fullPage = Array.from({ length: 50 }, (_, i) => `p${i}`);
    batch.mockImplementation(makeBatch({ emailQueryIds: fullPage }));

    await mail.loadMoreSearch();
    expect(mail.searchHasMore).toBe(true);
    expect(mail.searchEmailIds).toHaveLength(100);
  });

  it('dedupes an id the appended page shares with the already-loaded window', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1', 'e2'];
    mail.searchHasMore = true;

    // A race with a concurrent reconciliation could hand back an id we
    // already have (e2) alongside genuinely new ids.
    batch.mockImplementation(makeBatch({ emailQueryIds: ['e2', 'e3'] }));

    await mail.loadMoreSearch();
    expect(mail.searchEmailIds).toEqual(['e1', 'e2', 'e3']);
  });

  it('searchLoadingMore is true during the fetch and false once settled', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1'];
    mail.searchHasMore = true;

    let releaseQuery!: (v: { responses: [string, unknown, string][] }) => void;
    const gate = new Promise<{ responses: [string, unknown, string][] }>((r) => {
      releaseQuery = r;
    });
    batch.mockImplementation(async () => gate);

    const promise = mail.loadMoreSearch();
    await Promise.resolve();
    await Promise.resolve();
    expect(mail.searchLoadingMore).toBe(true);

    releaseQuery({
      responses: [
        ['Email/query', { ids: [] }, 'c0'],
        ['Email/get', { list: [] }, 'c1'],
        ['Thread/get', { list: [] }, 'c2'],
        ['Email/get', { list: [] }, 'c3'],
      ],
    });
    await promise;
    expect(mail.searchLoadingMore).toBe(false);
  });

  it('does not append a page that arrives after the query changed', async () => {
    const { mail } = await import('./store.svelte');
    mail.searchQuery = 'q1';
    mail.searchLoadStatus = 'ready';
    mail.searchEmailIds = ['e1'];
    mail.searchHasMore = true;

    let releaseQuery!: (v: { responses: [string, unknown, string][] }) => void;
    const gate = new Promise<{ responses: [string, unknown, string][] }>((r) => {
      releaseQuery = r;
    });
    batch.mockImplementation(async () => gate);

    const promise = mail.loadMoreSearch();
    // A different search starts (and completes) while the first page is
    // still in flight.
    mail.searchQuery = 'q2';
    mail.searchEmailIds = ['x1', 'x2'];

    releaseQuery({
      responses: [
        ['Email/query', { ids: ['e2'] }, 'c0'],
        ['Email/get', { list: [] }, 'c1'],
        ['Thread/get', { list: [] }, 'c2'],
        ['Email/get', { list: [] }, 'c3'],
      ],
    });
    await promise;

    // The stale page must not have been appended to the new search's ids.
    expect(mail.searchEmailIds).toEqual(['x1', 'x2']);
  });
});
