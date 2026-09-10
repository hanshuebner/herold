/**
 * Admin IMAP-import excluded-folders round-trip tests (re #305).
 *
 * GET /api/v1/imap-imports/status returns the live worker snapshot, which
 * carries no configuration fields. load() must additionally fetch each
 * distinct principal's account list from
 * GET /api/v1/principals/{pid}/imap-imports and merge excluded_folders in
 * by account id. setExcludedFolders() must PATCH
 * /api/v1/principals/{pid}/imap-imports/{aid} with {"excluded_folders": [...]}
 * and update the local cache on success, surfacing the server's error
 * (e.g. a 400 for a blank entry) on failure.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problemResponse(detail: string, status = 400): Response {
  return new Response(JSON.stringify({ title: 'Bad Request', detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

interface Worker {
  account_id: string;
  principal_id: string;
  account_name: string;
  host: string;
  phase: string;
  connected: boolean;
  phase_since: string;
  consecutive_failures: number;
  messages_fetched: number;
  flags_propagated: number;
  debug_log: boolean;
}

interface ConfigItem {
  id: string;
  excluded_folders?: string[];
}

interface FetchBodies {
  workers: Worker[];
  configsByPrincipal: Record<string, ConfigItem[]>;
  patchImpl?: (url: string, body: unknown) => Response;
}

function makeFetch(bodies: FetchBodies) {
  const calls: { url: string; method: string; body: unknown }[] = [];
  const fn = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ url, method, body });

    if (url.includes('/api/v1/imap-imports/status')) {
      return Promise.resolve(jsonResponse({ items: bodies.workers }));
    }
    const listMatch = /\/api\/v1\/principals\/([^/]+)\/imap-imports$/.exec(url);
    if (listMatch && method === 'GET') {
      const pid = listMatch[1] ?? '';
      const items = bodies.configsByPrincipal[pid] ?? [];
      return Promise.resolve(jsonResponse({ items, next: null }));
    }
    const patchMatch = /\/api\/v1\/principals\/([^/]+)\/imap-imports\/([^/]+)$/.exec(url);
    if (patchMatch && method === 'PATCH') {
      if (bodies.patchImpl) {
        return Promise.resolve(bodies.patchImpl(url, body));
      }
      return Promise.resolve(jsonResponse({}));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
  return { fn, calls };
}

interface ImapImportsModule {
  imapImports: {
    status: string;
    workers: Worker[];
    excludedFoldersByAccount: Record<string, string[]>;
    errorMessage: string | null;
    load(): Promise<void>;
    setExcludedFolders(
      principalId: string,
      accountId: string,
      folders: string[],
    ): Promise<{ ok: boolean; errorMessage: string | null }>;
  };
}

async function loadModule(): Promise<ImapImportsModule> {
  vi.resetModules();
  return (await import('./imap-imports.svelte')) as unknown as ImapImportsModule;
}

const worker: Worker = {
  account_id: 'acc1',
  principal_id: '42',
  account_name: 'Gmail Import',
  host: 'imap.gmail.com',
  phase: 'idle',
  connected: true,
  phase_since: '2026-01-01T00:00:00Z',
  consecutive_failures: 0,
  messages_fetched: 0,
  flags_propagated: 0,
  debug_log: false,
};

describe('IMAP import excluded folders (admin, re #305)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('merges excluded_folders from the per-principal config list into the worker snapshot', async () => {
    const { imapImports } = await loadModule();
    const { fn } = makeFetch({
      workers: [worker],
      configsByPrincipal: {
        '42': [{ id: 'acc1', excluded_folders: ['Spam', 'Promo'] }],
      },
    });
    vi.stubGlobal('fetch', fn);

    await imapImports.load();

    expect(imapImports.status).toBe('ready');
    expect(imapImports.excludedFoldersByAccount['acc1']).toEqual(['Spam', 'Promo']);
  });

  it('defaults to an empty list when the config item omits excluded_folders', async () => {
    const { imapImports } = await loadModule();
    const { fn } = makeFetch({
      workers: [worker],
      configsByPrincipal: { '42': [{ id: 'acc1' }] },
    });
    vi.stubGlobal('fetch', fn);

    await imapImports.load();

    expect(imapImports.excludedFoldersByAccount['acc1']).toEqual([]);
  });

  it('setExcludedFolders PATCHes the account and updates the local cache on success', async () => {
    const { imapImports } = await loadModule();
    const { fn, calls } = makeFetch({
      workers: [worker],
      configsByPrincipal: { '42': [{ id: 'acc1', excluded_folders: [] }] },
    });
    vi.stubGlobal('fetch', fn);
    await imapImports.load();

    const result = await imapImports.setExcludedFolders('42', 'acc1', ['Spam']);

    expect(result.ok).toBe(true);
    expect(imapImports.excludedFoldersByAccount['acc1']).toEqual(['Spam']);
    const patchCall = calls.find(
      (c) => c.method === 'PATCH' && c.url.includes('/imap-imports/acc1'),
    );
    expect(patchCall?.body).toEqual({ excluded_folders: ['Spam'] });
  });

  it('surfaces the server validation error for a blank entry and leaves the cache untouched', async () => {
    const { imapImports } = await loadModule();
    const { fn } = makeFetch({
      workers: [worker],
      configsByPrincipal: { '42': [{ id: 'acc1', excluded_folders: ['Spam'] }] },
      patchImpl: () =>
        problemResponse('excluded_folders entries must not be empty', 400),
    });
    vi.stubGlobal('fetch', fn);
    await imapImports.load();

    const result = await imapImports.setExcludedFolders('42', 'acc1', ['Spam', '   ']);

    expect(result.ok).toBe(false);
    expect(result.errorMessage).toContain('excluded_folders entries must not be empty');
    // The optimistic cache is left untouched on failure.
    expect(imapImports.excludedFoldersByAccount['acc1']).toEqual(['Spam']);
  });
});
