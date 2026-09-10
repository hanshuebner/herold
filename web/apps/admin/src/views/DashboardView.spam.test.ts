/**
 * Dashboard spam-filtering card tests (Wave 4.1, re #301).
 *
 * GET /api/v1/spam/status (internal/protoadmin/spam.go handleGetSpamStatus)
 * answers {enabled, plugin, reason?}. The Dashboard's "Spam filtering" card
 * renders three states: enabled with the plugin name, off with the reason,
 * and a fetch-failure error state matching the view's other cards.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/svelte';

// Imported statically so the Svelte transform is paid during module
// collection, not inside a test body (see PrincipalDetailView.test.ts).
import DashboardView from './DashboardView.svelte';
// The dashboard state is a module-level singleton shared by every render
// in this file; reset the spam fields before each test so a prior test's
// resolved fetch cannot leave stale text in the DOM for `findByText` to
// match against before the new fetch resolves.
import { dashboard } from '../lib/dashboard/dashboard.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface FetchBodies {
  spamStatus?: unknown;
  spamStatusStatus?: number;
  spamStatusFails?: boolean;
}

function makeFetch(bodies: FetchBodies = {}) {
  const { spamStatus, spamStatusStatus = 200, spamStatusFails = false } = bodies;

  return vi.fn().mockImplementation((url: string) => {
    if ((url as string).includes('/api/v1/spam/status')) {
      if (spamStatusFails) {
        // apiGet() never rejects -- request() catches fetch failures and
        // returns {ok:false, errorMessage} (web/apps/admin/src/lib/api/client.ts),
        // so simulate the server-error branch instead of a rejected promise.
        return Promise.resolve(new Response(null, { status: 500 }));
      }
      return Promise.resolve(jsonResponse(spamStatus ?? {}, spamStatusStatus));
    }
    if ((url as string).includes('/api/v1/domains')) {
      return Promise.resolve(jsonResponse({ items: [], next: null }));
    }
    if ((url as string).includes('/api/v1/queue/stats')) {
      return Promise.resolve(
        jsonResponse({ counts: { queued: 0, deferred: 0, delivered: 0, failed: 0, held: 0 } }),
      );
    }
    if ((url as string).includes('/api/v1/audit')) {
      return Promise.resolve(jsonResponse({ items: [], next: null }));
    }
    if ((url as string).includes('/api/v1/admin/clientlog/stats')) {
      return Promise.resolve(
        jsonResponse({ received_total: {}, dropped_total: {}, ring_buffer_rows: {} }),
      );
    }
    if ((url as string).includes('/api/v1/server/status')) {
      return Promise.resolve(jsonResponse({}));
    }
    return Promise.resolve(new Response(null, { status: 404 }));
  });
}

describe('Dashboard spam filtering card (Wave 4.1, re #301)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    cleanup();
  });

  function resetSpamState(): void {
    dashboard.status = 'idle';
    dashboard.spamStatus = null;
    dashboard.spamStatusError = null;
  }

  it('shows Enabled with the plugin name when the classifier is healthy', async () => {
    resetSpamState();
    vi.stubGlobal(
      'fetch',
      makeFetch({ spamStatus: { enabled: true, plugin: 'herold-spam-llm' } }),
    );

    render(DashboardView);

    // findByText retries until the fetch resolves and the singleton's
    // spamStatus updates, rather than matching stale DOM left by a
    // still-settling prior test's render of the same shared singleton.
    expect(await screen.findByText('herold-spam-llm')).toBeInTheDocument();
    expect(screen.getByText('Enabled')).toBeInTheDocument();
  });

  it('shows Off with the reason when no plugin is configured', async () => {
    resetSpamState();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        spamStatus: {
          enabled: false,
          plugin: '',
          reason: 'no spam plugin configured in system.toml',
        },
      }),
    );

    render(DashboardView);

    expect(
      await screen.findByText('no spam plugin configured in system.toml'),
    ).toBeInTheDocument();
    expect(screen.getByText('Off')).toBeInTheDocument();
  });

  it('shows Off with the supervisor error when a configured plugin is failing', async () => {
    resetSpamState();
    vi.stubGlobal(
      'fetch',
      makeFetch({
        spamStatus: {
          enabled: false,
          plugin: 'herold-spam-llm',
          reason: 'plugin process exited: context deadline exceeded',
        },
      }),
    );

    render(DashboardView);

    expect(
      await screen.findByText('plugin process exited: context deadline exceeded'),
    ).toBeInTheDocument();
    expect(screen.getByText('Off')).toBeInTheDocument();
  });

  it('shows the inline error state when the status fetch fails', async () => {
    resetSpamState();
    vi.stubGlobal('fetch', makeFetch({ spamStatusFails: true }));

    render(DashboardView);

    expect(await screen.findByText('HTTP 500')).toBeInTheDocument();
  });
});
