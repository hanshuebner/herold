/**
 * Tests for the manual reload button's in-flight state (issue #309).
 *
 * refreshFolder() must expose listRefreshing so the toolbar button can
 * drive a spin animation and its disabled guard, must hold that flag for
 * at least REFRESH_MIN_SPIN_MS even when the round trip resolves sooner,
 * and must not be re-triggerable while a check is already running.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Email } from './types';

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch, session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
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
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc1',
        'urn:ietf:params:jmap:submission': 'acc1',
      },
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

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: 'test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    ...overrides,
  };
}

type BatchResponse = [string, unknown, string];
type BatchResult = { responses: BatchResponse[]; using: Set<string> };

function makeBatchImpl(opts: {
  emailQueryIds?: string[];
  emailGetEmails?: Email[];
  emailState?: string;
}): (builder: unknown) => Promise<BatchResult> {
  return async (builder: unknown) => {
    let counter = 0;
    const calls: { name: string }[] = [];
    const api = {
      call: (name: string, _args: unknown) => {
        calls.push({ name });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (builder as (b: typeof api) => void)(api);

    const responses: BatchResponse[] = [];
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i]!;
      switch (c.name) {
        case 'Email/query':
          responses.push([c.name, { ids: opts.emailQueryIds ?? [] }, `c${i}`]);
          break;
        case 'Email/get':
          responses.push([
            c.name,
            { list: opts.emailGetEmails ?? [], state: opts.emailState ?? 'state-x' },
            `c${i}`,
          ]);
          break;
        case 'Thread/get':
          responses.push([c.name, { list: [] }, `c${i}`]);
          break;
        default:
          responses.push([c.name, { list: [] }, `c${i}`]);
      }
    }
    return { responses, using: new Set<string>() };
  };
}

describe('mail store: manual refresh in-flight state (issue #309)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('listRefreshing is true while refreshFolder() is in flight and false once settled', async () => {
    const { mail } = await import('./store.svelte');

    mail.emails = new Map([['e1', makeEmail({ id: 'e1', threadId: 't1' })]]);
    mail.listEmailIds = ['e1'];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';

    let releaseQuery!: (val: BatchResult) => void;
    const queryGate = new Promise<BatchResult>((resolve) => {
      releaseQuery = resolve;
    });
    batch.mockImplementation(() => queryGate);

    expect(mail.listRefreshing).toBe(false);

    const refreshDone = mail.refreshFolder();
    await Promise.resolve();
    await Promise.resolve();

    expect(mail.listRefreshing).toBe(true);

    releaseQuery({
      responses: [
        ['Email/query', { ids: ['e1'] }, 'c0'],
        ['Email/get', { list: [], state: 'state-2' }, 'c1'],
        ['Thread/get', { list: [] }, 'c2'],
        ['Email/get', { list: [] }, 'c3'],
      ],
      using: new Set<string>(),
    });

    // The JMAP round trip has resolved, but the minimum-visible-duration
    // timer has not fired yet: the flag must still read true.
    await Promise.resolve();
    await Promise.resolve();
    expect(mail.listRefreshing).toBe(true);

    // Advance past the minimum spin duration; the flag now clears.
    await vi.advanceTimersByTimeAsync(500);
    await refreshDone;
    expect(mail.listRefreshing).toBe(false);
  });

  it('holds listRefreshing for at least REFRESH_MIN_SPIN_MS even when the round trip is instant', async () => {
    const { mail, REFRESH_MIN_SPIN_MS } = await import('./store.svelte');

    mail.emails = new Map();
    mail.listEmailIds = [];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';

    // Round trip resolves on the next microtask -- as fast as it gets.
    batch.mockImplementation(makeBatchImpl({ emailQueryIds: [] }));

    const refreshDone = mail.refreshFolder();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // Even though the query has already resolved, the flag is still set
    // because the minimum spin duration has not elapsed.
    expect(mail.listRefreshing).toBe(true);

    await vi.advanceTimersByTimeAsync(REFRESH_MIN_SPIN_MS - 1);
    expect(mail.listRefreshing).toBe(true);

    await vi.advanceTimersByTimeAsync(1);
    await refreshDone;
    expect(mail.listRefreshing).toBe(false);
  });

  it('a second refreshFolder() call while one is in flight is a no-op', async () => {
    const { mail } = await import('./store.svelte');

    mail.emails = new Map();
    mail.listEmailIds = [];
    mail.listLoadStatus = 'ready';
    mail.listFolder = 'all';

    let callCount = 0;
    let releaseQuery!: (val: BatchResult) => void;
    const queryGate = new Promise<BatchResult>((resolve) => {
      releaseQuery = resolve;
    });
    batch.mockImplementation(() => {
      callCount++;
      return queryGate;
    });

    const first = mail.refreshFolder();
    await Promise.resolve();
    await Promise.resolve();
    expect(mail.listRefreshing).toBe(true);

    // A second call while the first is still running must not issue
    // another batch and must resolve without waiting on the first.
    await mail.refreshFolder();
    expect(callCount).toBe(1);

    releaseQuery({
      responses: [
        ['Email/query', { ids: [] }, 'c0'],
        ['Email/get', { list: [], state: 'state-2' }, 'c1'],
        ['Thread/get', { list: [] }, 'c2'],
        ['Email/get', { list: [] }, 'c3'],
      ],
      using: new Set<string>(),
    });
    await vi.advanceTimersByTimeAsync(500);
    await first;
    expect(mail.listRefreshing).toBe(false);
  });
});
