/**
 * clientlog.spec.ts
 *
 * Playwright e2e tests for the client-log viewer (REQ-ADM-202 extended,
 * REQ-ADM-230..233, REQ-OPS-211, REQ-OPS-212, REQ-OPS-218).
 *
 * All API calls are intercepted via page.route() so no real herold backend
 * is needed.
 *
 * Covered scenarios:
 *   - Navigate to /admin/clientlog and see the list
 *   - Filter by kind, verify the query parameter is sent
 *   - Open detail pane for a row
 *   - Click Symbolicate with a stub map (returns raw stack on parse failure
 *     -- we verify the fallback message is shown rather than a JS crash)
 *   - Enable live-tail for a session
 *   - Disable live-tail for a session
 *   - Paginate with "Load more"
 *   - Vital metric display (kind=vital detail pane shows Metric/Value/ID)
 */

import { test, expect } from '@playwright/test';

// ---------------------------------------------------------------------------
// Fixture data
// ---------------------------------------------------------------------------

const NOW = new Date().toISOString();

/**
 * makeRow builds a mock clientLogRowDTO as returned by GET
 * /api/v1/admin/clientlog.
 *
 * The server stores events in an enrichedPayload envelope:
 *   { server_recv_ts, clock_skew_ms, user_id, listener, endpoint,
 *     raw: <original wire event> }
 * Breadcrumbs are at payload.raw.breadcrumbs and vital data at
 * payload.raw.vital -- NOT at the top-level payload (see
 * clientlog_pipeline.go clientEventToRow / enrichedPayload).
 */
function makeRow(id: number, overrides: Record<string, unknown> = {}) {
  return {
    id,
    slice: 'auth',
    server_ts: NOW,
    client_ts: NOW,
    clock_skew_ms: 12,
    app: 'admin',
    kind: 'error',
    level: 'error',
    user_id: 'user-1',
    session_id: 'sess-abc',
    page_id: 'page-1',
    request_id: 'req-xyz',
    route: '/admin/dashboard',
    build_sha: 'abc123',
    ua: 'Mozilla/5.0 (vitest)',
    msg: `Error ${id}: something went wrong`,
    stack: `Error: something went wrong\n    at render (http://localhost:5174/admin/assets/index-abc123.js:42:17)\n    at mount (http://localhost:5174/admin/assets/index-abc123.js:10:5)`,
    // payload mirrors the enrichedPayload envelope stored in the ring buffer.
    // breadcrumbs and vital live under payload.raw, not at the top level.
    payload: {
      server_recv_ts: NOW,
      clock_skew_ms: 12,
      user_id: 'user-1',
      listener: 'admin',
      endpoint: 'auth',
      raw: {
        v: 1,
        kind: 'error',
        level: 'error',
        msg: `Error ${id}: something went wrong`,
        breadcrumbs: [
          { kind: 'route', ts: NOW, route: '/admin/dashboard' },
          { kind: 'fetch', ts: NOW, method: 'GET', url_path: '/api/v1/server/status', status: 200 },
        ],
      },
    },
    ...overrides,
  };
}

/** makeVitalRow builds a mock kind=vital row with metric data. */
function makeVitalRow(id: number, name: string, value: number, vitalId: string) {
  return {
    id,
    slice: 'auth',
    server_ts: NOW,
    client_ts: NOW,
    clock_skew_ms: 5,
    app: 'suite',
    kind: 'vital',
    level: 'info',
    user_id: 'user-1',
    session_id: 'sess-abc',
    page_id: 'page-vital',
    build_sha: 'abc123',
    ua: 'Mozilla/5.0 (vitest)',
    msg: `web vital: ${name}`,
    payload: {
      server_recv_ts: NOW,
      clock_skew_ms: 5,
      user_id: 'user-1',
      listener: 'admin',
      endpoint: 'auth',
      raw: {
        v: 1,
        kind: 'vital',
        level: 'info',
        msg: `web vital: ${name}`,
        vital: { name, value, id: vitalId },
      },
    },
  };
}

const PAGE1_ROWS = Array.from({ length: 5 }, (_, i) => makeRow(i + 1));
const PAGE2_ROWS = Array.from({ length: 3 }, (_, i) => makeRow(i + 10));

const TIMELINE_ENTRIES = [
  {
    source: 'client',
    effective_ts: NOW,
    clientlog: makeRow(1),
  },
];

const STATS = {
  received_total: { 'auth/admin/error': 42, 'auth/suite/log': 100 },
  dropped_total: { 'auth/rate_limit': 3 },
  ring_buffer_rows: { auth: 45, public: 10 },
};

// ---------------------------------------------------------------------------
// Auth stub helper
// ---------------------------------------------------------------------------

function installAuthRoutes(page: import('@playwright/test').Page) {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        principal_id: '1',
        email: 'admin@example.com',
        scopes: ['admin'],
      }),
    }),
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('clientlog viewer', () => {
  test.beforeEach(async ({ page }) => {
    installAuthRoutes(page);

    // Stub the stats endpoint (loaded by DashboardView; suppress 404 noise).
    void page.route('/api/v1/admin/clientlog/stats', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(STATS),
      }),
    );
  });

  test('renders list with headers and rows', async ({ page }) => {
    void page.route('/api/v1/admin/clientlog*', (route) => {
      const url = new URL(route.request().url());
      if (url.pathname.includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: PAGE1_ROWS, next_cursor: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByRole('heading', { name: 'Client logs' })).toBeVisible();

    // Table headers
    await expect(page.getByRole('columnheader', { name: 'When' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Kind' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Level' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Route' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Message' })).toBeVisible();

    // First row renders
    await expect(page.getByText('Error 1: something went wrong')).toBeVisible();
  });

  test('filter by kind sends kind parameter in request', async ({ page }) => {
    const requests: string[] = [];

    void page.route('/api/v1/admin/clientlog*', (route) => {
      const url = route.request().url();
      if (!url.includes('/stats')) {
        requests.push(url);
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: PAGE1_ROWS, next_cursor: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByRole('heading', { name: 'Client logs' })).toBeVisible();

    // Select kind = error
    await page.locator('#cl-kind').selectOption('error');
    await page.getByRole('button', { name: 'Apply' }).click();

    await page.waitForTimeout(200);
    const filteredRequests = requests.filter((u) => u.includes('kind=error'));
    expect(filteredRequests.length).toBeGreaterThan(0);
  });

  test('open detail pane shows row fields without href', async ({ page }) => {
    void page.route('/api/v1/admin/clientlog*', (route) => {
      if (route.request().url().includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: [makeRow(1)], next_cursor: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByText('Error 1: something went wrong')).toBeVisible();

    // Click the row to open the detail pane.
    await page.getByText('Error 1: something went wrong').click();

    // Detail pane should render.
    await expect(page.getByRole('heading', { name: 'Detail' })).toBeVisible();

    // Route is shown as plain text, no anchor tag (REQ-OPS-218).
    const routeText = page.getByText('/admin/dashboard').first();
    await expect(routeText).toBeVisible();
    // Verify there is no href attribute on the route element.
    const href = await routeText.getAttribute('href');
    expect(href).toBeNull();

    // Stack trace is shown in a pre element.
    await expect(page.locator('pre.stack-pre')).toBeVisible();
  });

  test('symbolicate button shows inline message on map fetch failure', async ({
    page,
  }) => {
    void page.route('/api/v1/admin/clientlog*', (route) => {
      if (route.request().url().includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: [makeRow(1)], next_cursor: null }),
      });
    });

    // Return 404 for all .map requests so symbolication fails gracefully.
    void page.route('**/*.js.map', (route) =>
      route.fulfill({ status: 404 }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByText('Error 1: something went wrong')).toBeVisible();
    await page.getByText('Error 1: something went wrong').click();
    await expect(page.getByRole('heading', { name: 'Detail' })).toBeVisible();

    // Click the Symbolicate button.
    await page.getByRole('button', { name: 'Symbolicate' }).click();

    // Should show an error message rather than crashing.
    await expect(page.getByText(/Symbolication failed/i)).toBeVisible({ timeout: 5000 });

    // The raw stack should still be shown as a fallback.
    await expect(page.locator('pre.stack-pre')).toBeVisible();
    await expect(page.locator('pre.stack-pre')).toContainText('Error: something went wrong');
  });

  test('enable live-tail calls POST livetail endpoint', async ({ page }) => {
    const livetailRequests: string[] = [];

    // Register routes with await so they are fully installed before navigation.
    await page.route('/api/v1/admin/clientlog/livetail', (route) => {
      const method = route.request().method();
      if (method === 'POST') {
        livetailRequests.push(route.request().url());
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            session_id: 'sess-abc',
            livetail_until: new Date(Date.now() + 15 * 60_000).toISOString(),
          }),
        });
      }
      return route.fulfill({ status: 405 });
    });
    await page.route('/api/v1/admin/clientlog*', (route) => {
      const url = route.request().url();
      if (url.includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: [makeRow(1)], next_cursor: null }),
      });
    });
    // Also intercept the clientlog ingest endpoints.
    await page.route('/api/v1/clientlog/public', (route) =>
      route.fulfill({ status: 200, body: '{}' }),
    );
    await page.route('/api/v1/clientlog', (route) =>
      route.fulfill({ status: 200, body: '{}' }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByText('Error 1: something went wrong')).toBeVisible({
      timeout: 5000,
    });

    // Click the row to open the detail pane.
    await page.getByText('Error 1: something went wrong').click();
    await expect(page.getByRole('heading', { name: 'Detail' })).toBeVisible({
      timeout: 3000,
    });

    // Scroll to the Live-tail section (the pane may be tall).
    await page.getByRole('heading', { name: 'Live-tail', level: 3 }).scrollIntoViewIfNeeded();

    // The enable button should be present because the row has a session_id.
    const enableBtn = page.getByRole('button', { name: 'Enable live-tail (15 min)' });
    await expect(enableBtn).toBeVisible({ timeout: 3000 });
    await enableBtn.click();

    // After success, the disable button appears.
    await expect(
      page.getByRole('button', { name: 'Disable live-tail' }),
    ).toBeVisible({ timeout: 5000 });

    expect(livetailRequests.length).toBe(1);
  });

  test('disable live-tail calls DELETE livetail endpoint', async ({ page }) => {
    const deleteRequests: string[] = [];

    // Register the livetail route first (most specific) with await.
    await page.route('/api/v1/admin/clientlog/livetail', (route) => {
      const method = route.request().method();
      if (method === 'POST') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            session_id: 'sess-abc',
            livetail_until: new Date(Date.now() + 15 * 60_000).toISOString(),
          }),
        });
      }
      return route.fulfill({ status: 405 });
    });
    // Register the livetail DELETE route (matches /livetail/sess-abc).
    await page.route('**/clientlog/livetail/**', (route) => {
      const method = route.request().method();
      if (method === 'DELETE') {
        deleteRequests.push(route.request().url());
        return route.fulfill({ status: 204 });
      }
      return route.fulfill({ status: 405 });
    });
    await page.route('/api/v1/admin/clientlog*', (route) => {
      const url = route.request().url();
      if (url.includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: [makeRow(1)], next_cursor: null }),
      });
    });
    // Intercept clientlog ingest endpoints.
    await page.route('/api/v1/clientlog/public', (route) =>
      route.fulfill({ status: 200, body: '{}' }),
    );
    await page.route('/api/v1/clientlog', (route) =>
      route.fulfill({ status: 200, body: '{}' }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByText('Error 1: something went wrong')).toBeVisible({
      timeout: 5000,
    });
    await page.getByText('Error 1: something went wrong').click();
    await expect(page.getByRole('heading', { name: 'Detail' })).toBeVisible({
      timeout: 3000,
    });

    await page.getByRole('heading', { name: 'Live-tail', level: 3 }).scrollIntoViewIfNeeded();

    // Enable first.
    const enableBtn = page.getByRole('button', { name: 'Enable live-tail (15 min)' });
    await expect(enableBtn).toBeVisible({ timeout: 3000 });
    await enableBtn.click();
    await expect(
      page.getByRole('button', { name: 'Disable live-tail' }),
    ).toBeVisible({ timeout: 5000 });

    // Then disable.
    await page.getByRole('button', { name: 'Disable live-tail' }).click();

    // The enable button should come back.
    await expect(
      page.getByRole('button', { name: 'Enable live-tail (15 min)' }),
    ).toBeVisible({ timeout: 5000 });

    expect(deleteRequests.length).toBe(1);
    expect(deleteRequests[0]).toContain('sess-abc');
  });

  test('load-more appends additional rows', async ({ page }) => {
    let callCount = 0;

    void page.route('/api/v1/admin/clientlog*', (route) => {
      if (route.request().url().includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      callCount++;
      const url = new URL(route.request().url());
      const hasCursor = url.searchParams.has('cursor');
      if (!hasCursor) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ rows: PAGE1_ROWS, next_cursor: 'cursor-page2' }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: PAGE2_ROWS, next_cursor: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByText('Error 1: something went wrong')).toBeVisible();

    const loadMoreBtn = page.getByRole('button', { name: 'Load more' });
    await expect(loadMoreBtn).toBeVisible();
    await loadMoreBtn.click();

    // After page 2 loads, the page-2 rows appear.
    await expect(page.getByText('Error 10: something went wrong')).toBeVisible({
      timeout: 3000,
    });
  });

  test('vital detail pane shows Metric / Value / ID for kind=vital rows', async ({ page }) => {
    // REQ-OPS-202: kind=vital rows carry payload.raw.vital with name/value/id.
    // The detail pane must render these in a "Web Vital" section.
    // Regression test for issue #71 where the prior admin SPA had no
    // rendering path for vital data and the TypeScript types were wrong.
    const vitalRow = makeVitalRow(100, 'LCP', 1234, 'v-lcp-e2e-1');

    void page.route('/api/v1/admin/clientlog*', (route) => {
      if (route.request().url().includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: [vitalRow], next_cursor: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();

    // The vital row should appear in the list with the message "web vital: LCP".
    await expect(page.getByText('web vital: LCP')).toBeVisible({ timeout: 5000 });

    // Click the row to open the detail pane.
    await page.getByText('web vital: LCP').click();
    await expect(page.getByRole('heading', { name: 'Detail' })).toBeVisible({ timeout: 3000 });

    // The "Web Vital" section heading must be visible.
    await expect(page.getByRole('heading', { name: 'Web Vital', level: 3 })).toBeVisible({
      timeout: 3000,
    });

    // Metric name: "LCP" — exact-match so we hit the detail-pane cell, not
    // the "web vital: LCP" row text in the list which also contains "LCP".
    await expect(page.getByText('LCP', { exact: true })).toBeVisible();

    // Value with unit: LCP is in ms, so "1234 ms".
    await expect(page.getByText('1234 ms')).toBeVisible();

    // Vital ID.
    await expect(page.getByText('v-lcp-e2e-1')).toBeVisible();
  });

  test('vital detail pane shows dimensionless value for CLS', async ({ page }) => {
    // CLS values are dimensionless (no " ms" suffix); verify the formatting branch.
    const clsRow = makeVitalRow(101, 'CLS', 0.1234, 'v-cls-e2e-1');

    void page.route('/api/v1/admin/clientlog*', (route) => {
      if (route.request().url().includes('/stats')) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(STATS),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ rows: [clsRow], next_cursor: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Client logs' }).click();
    await expect(page.getByText('web vital: CLS')).toBeVisible({ timeout: 5000 });
    await page.getByText('web vital: CLS').click();
    await expect(page.getByRole('heading', { name: 'Web Vital', level: 3 })).toBeVisible({ timeout: 3000 });

    // CLS: 4 decimal places, no " ms" suffix.
    await expect(page.getByText('0.1234')).toBeVisible();
    // Must NOT have " ms" after the CLS value.
    const valueCell = page.locator('.vital-value');
    await expect(valueCell).toBeVisible();
    const valueText = await valueCell.textContent();
    expect(valueText).not.toMatch(/ms/);
  });
});
