/**
 * IMAPImportsView excluded-folders editor tests (re #305).
 *
 * Covers:
 *   - Existing excluded folders render as chips (merged from the
 *     per-principal config list on load()).
 *   - Adding a folder name PATCHes the account with the extended list.
 *   - An empty entry is rejected client-side without a network call.
 *   - Removing a chip PATCHes the account with the folder removed.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup, fireEvent } from '@testing-library/svelte';

// Imported statically so the Svelte transform is paid during module
// collection, not inside a test body (see PrincipalDetailView.test.ts).
import IMAPImportsView from './IMAPImportsView.svelte';
import { imapImports } from '../lib/imap-imports/imap-imports.svelte';

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

const worker = {
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

interface FetchBodies {
  excludedFolders?: string[];
  patchImpl?: (body: unknown) => Response;
}

function makeFetch(bodies: FetchBodies = {}) {
  const calls: { url: string; method: string; body: unknown }[] = [];
  const fn = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ url, method, body });

    if (url.includes('/api/v1/imap-imports/status')) {
      return Promise.resolve(jsonResponse({ items: [worker] }));
    }
    if (/\/api\/v1\/principals\/42\/imap-imports$/.test(url) && method === 'GET') {
      return Promise.resolve(
        jsonResponse({
          items: [{ id: 'acc1', excluded_folders: bodies.excludedFolders ?? [] }],
          next: null,
        }),
      );
    }
    if (/\/api\/v1\/principals\/42\/imap-imports\/acc1$/.test(url) && method === 'PATCH') {
      if (bodies.patchImpl) return Promise.resolve(bodies.patchImpl(body));
      return Promise.resolve(jsonResponse({}));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
  return { fn, calls };
}

function resetImapImportsState(): void {
  imapImports.status = 'idle';
  imapImports.workers = [];
  imapImports.excludedFoldersByAccount = {};
  imapImports.errorMessage = null;
}

describe('IMAPImportsView excluded folders editor (re #305)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    cleanup();
  });

  it('renders existing excluded folders as chips', async () => {
    resetImapImportsState();
    const { fn } = makeFetch({ excludedFolders: ['Spam', 'Promo'] });
    vi.stubGlobal('fetch', fn);

    render(IMAPImportsView);

    const chips = await screen.findAllByTestId('worker-excluded-folder-chip');
    expect(chips).toHaveLength(2);
    expect(chips[0]).toHaveTextContent('Spam');
    expect(chips[1]).toHaveTextContent('Promo');
  });

  it('adds a folder and PATCHes the extended excluded_folders list', async () => {
    resetImapImportsState();
    const { fn, calls } = makeFetch({ excludedFolders: ['Spam'] });
    vi.stubGlobal('fetch', fn);

    render(IMAPImportsView);
    await screen.findAllByTestId('worker-excluded-folder-chip');

    const input = screen.getByTestId(
      'worker-excluded-folder-input-acc1',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'Trash' } });
    await fireEvent.click(screen.getByTestId('worker-excluded-folder-add-acc1'));

    // Wait for the PATCH round-trip to settle and the chip list to grow.
    await vi.waitFor(() => {
      expect(screen.getAllByTestId('worker-excluded-folder-chip')).toHaveLength(2);
    });
    const patchCall = calls.find((c) => c.method === 'PATCH');
    expect(patchCall?.body).toEqual({ excluded_folders: ['Spam', 'Trash'] });
  });

  it('rejects an empty entry without issuing a PATCH', async () => {
    resetImapImportsState();
    const { fn, calls } = makeFetch({ excludedFolders: [] });
    vi.stubGlobal('fetch', fn);

    render(IMAPImportsView);
    await screen.findByTestId('worker-excluded-folder-input-acc1');

    await fireEvent.click(screen.getByTestId('worker-excluded-folder-add-acc1'));

    expect(
      screen.getByText('Enter a folder name before adding.'),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('surfaces the server error when the PATCH is rejected', async () => {
    resetImapImportsState();
    const { fn } = makeFetch({
      excludedFolders: ['Spam'],
      patchImpl: () => problemResponse('excluded_folders entries must not be empty'),
    });
    vi.stubGlobal('fetch', fn);

    render(IMAPImportsView);
    await screen.findAllByTestId('worker-excluded-folder-chip');

    const input = screen.getByTestId(
      'worker-excluded-folder-input-acc1',
    ) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'Trash' } });
    await fireEvent.click(screen.getByTestId('worker-excluded-folder-add-acc1'));

    expect(
      await screen.findByText(/excluded_folders entries must not be empty/),
    ).toBeInTheDocument();
  });

  it('removes a chip and PATCHes the reduced excluded_folders list', async () => {
    resetImapImportsState();
    const { fn, calls } = makeFetch({ excludedFolders: ['Spam', 'Promo'] });
    vi.stubGlobal('fetch', fn);

    render(IMAPImportsView);
    await screen.findAllByTestId('worker-excluded-folder-chip');

    const removeButtons = screen.getAllByTestId('worker-excluded-folder-remove');
    await fireEvent.click(removeButtons[0]!);

    await vi.waitFor(() => {
      const chips = screen.getAllByTestId('worker-excluded-folder-chip');
      expect(chips).toHaveLength(1);
      expect(chips[0]).toHaveTextContent('Promo');
    });
    const patchCall = calls.find((c) => c.method === 'PATCH');
    expect(patchCall?.body).toEqual({ excluded_folders: ['Promo'] });
  });
});
