/**
 * MailingListsView component test (epic #183, REQ-MLIST-42).
 *
 * Imported statically at module top level so the Svelte transform is paid
 * during module collection rather than inside a test body, where it would
 * count against the per-test timeout on the CI runner (re the f4a89ab4 CI
 * break / 0ef95abc fix, restated in web/CLAUDE.md).
 */

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, cleanup, waitFor } from '@testing-library/svelte';
import MailingListsView from './MailingListsView.svelte';
import { mlists } from '../lib/mlists/mlists.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function mailingListDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    principal_id: 100,
    posting_address: 'announce@example.local',
    domain: 'example.local',
    display_name: 'Announcements',
    owner_principal_id: 2,
    arc_seal: true,
    posting_policy: 'open',
    subscribe_policy: 'closed',
    created_at: '2026-07-12T18:39:05.400041Z',
    updated_at: '2026-07-12T18:39:05.425518Z',
    ...overrides,
  };
}

describe('MailingListsView', () => {
  beforeEach(() => {
    // The list state is a module singleton; reset it directly (rather than
    // vi.resetModules(), which would desync it from the statically imported
    // component instance) so each test observes a fresh 'idle' load.
    mlists.status = 'idle';
    mlists.items = [];
    mlists.cursor = null;
    mlists.hasMore = false;
    mlists.errorMessage = null;
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('renders lists unwrapped from the {items,next} envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/api/v1/lists')) {
          return Promise.resolve(
            jsonResponse({
              items: [mailingListDTO(), mailingListDTO({ id: 2, posting_address: 'dev@example.local', display_name: 'Dev list' })],
              next: null,
            }),
          );
        }
        return Promise.resolve(new Response(null, { status: 404 }));
      }),
    );

    render(MailingListsView);

    expect(await screen.findByText('Announcements')).toBeInTheDocument();
    expect(screen.getByText('Dev list')).toBeInTheDocument();
    expect(screen.getByText('announce@example.local')).toBeInTheDocument();
  });

  it('shows the empty state, not a broken render, when items is empty', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ items: [], next: null })));

    render(MailingListsView);

    await waitFor(() => {
      expect(screen.getByText('No mailing lists found.')).toBeInTheDocument();
    });
  });
});
