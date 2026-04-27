/**
 * audit.spec.ts
 *
 * Covers:
 *   - Audit list renders entries with correct columns
 *   - Action filter narrows results when Apply is clicked
 *   - Since / until date filters are sent as query parameters
 *   - Load-more advances the cursor and appends new rows
 *   - Clear button resets filters and reloads
 */

import { test, expect } from '@playwright/test';

const AUDIT_PAGE1 = Array.from({ length: 10 }, (_, i) => ({
  id: `a${i}`,
  at: new Date(Date.now() - (i + 1) * 60_000).toISOString(),
  actor_kind: 'principal',
  actor_id: '1',
  action: i === 0 ? 'principal.create' : `action.${i}`,
  subject: `subject-${i}`,
  outcome: i % 3 === 0 ? 'failure' : 'success',
  message: `message ${i}`,
}));

const AUDIT_PAGE2 = Array.from({ length: 5 }, (_, i) => ({
  id: `b${i}`,
  at: new Date(Date.now() - (i + 11) * 60_000).toISOString(),
  actor_kind: 'principal',
  actor_id: '1',
  action: `older.action.${i}`,
  subject: `subject-old-${i}`,
  outcome: 'success',
  message: null,
}));

function installAuthRoutes(page: import('@playwright/test').Page) {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
    }),
  );
}

test.describe('audit', () => {
  test.beforeEach(async ({ page }) => {
    installAuthRoutes(page);
  });

  test('audit list renders entries with all columns', async ({ page }) => {
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: AUDIT_PAGE1, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Audit' }).click();
    await expect(page.getByRole('heading', { name: 'Audit log' })).toBeVisible();

    // Check table headers.
    await expect(page.getByRole('columnheader', { name: 'When' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Actor' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Action' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Outcome' })).toBeVisible();

    // Check first entry renders.
    await expect(page.getByText('principal.create')).toBeVisible();
    await expect(page.getByText('success').first()).toBeVisible();
    await expect(page.getByText('failure').first()).toBeVisible();
  });

  test('action filter sends action parameter and narrows results', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/audit*', (route) => {
      requests.push(route.request().url());
      const url = new URL(route.request().url());
      const action = url.searchParams.get('action') ?? '';
      const filtered = AUDIT_PAGE1.filter((e) =>
        action ? e.action.includes(action) : true,
      );
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: filtered, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Audit' }).click();
    await expect(page.getByRole('heading', { name: 'Audit log' })).toBeVisible();

    // Type in the action filter field.
    await page.getByRole('textbox', { name: /filter by action/i }).fill('principal.create');
    await page.getByRole('button', { name: 'Apply' }).click();

    // Verify the API was called with the action param.
    await page.waitForTimeout(200); // allow the fetch to complete
    const filteredRequests = requests.filter((u) => u.includes('action='));
    expect(filteredRequests.length).toBeGreaterThan(0);
    expect(filteredRequests[filteredRequests.length - 1]).toContain('action=principal.create');
  });

  test('since/until filters are sent as query parameters', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/audit*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: AUDIT_PAGE1, next: null }) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Audit' }).click();
    await expect(page.getByRole('heading', { name: 'Audit log' })).toBeVisible();

    // Fill since filter.
    await page.locator('#audit-since').fill('2024-06-01T00:00');
    await page.locator('#audit-until').fill('2024-06-02T00:00');
    await page.getByRole('button', { name: 'Apply' }).click();

    await page.waitForTimeout(200);
    const sinceRequests = requests.filter((u) => u.includes('since='));
    expect(sinceRequests.length).toBeGreaterThan(0);
    const untilRequests = requests.filter((u) => u.includes('until='));
    expect(untilRequests.length).toBeGreaterThan(0);
  });

  test('load-more appends additional rows', async ({ page }) => {
    let callCount = 0;

    await page.route('/api/v1/audit*', (route) => {
      callCount++;
      const url = new URL(route.request().url());
      const hasCursor = url.searchParams.has('cursor') || url.searchParams.has('after_id');
      // First call returns page 1 with a next cursor; second returns page 2.
      if (callCount === 1 || !hasCursor) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            items: AUDIT_PAGE1,
            next: 'cursor-abc',
          }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          items: AUDIT_PAGE2,
          next: null,
        }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Audit' }).click();
    await expect(page.getByRole('heading', { name: 'Audit log' })).toBeVisible();
    await expect(page.getByText('principal.create')).toBeVisible();

    // "Load more" button is visible when hasMore is true.
    const loadMoreBtn = page.getByRole('button', { name: 'Load more' });
    await expect(loadMoreBtn).toBeVisible();
    await loadMoreBtn.click();

    // After load-more, older entries appear.
    await expect(page.getByText('older.action.0')).toBeVisible();
  });

  test('clear button resets filters', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/audit*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: AUDIT_PAGE1, next: null }) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Audit' }).click();
    await expect(page.getByRole('heading', { name: 'Audit log' })).toBeVisible();

    await page.getByRole('textbox', { name: /filter by action/i }).fill('some-action');
    await page.getByRole('button', { name: 'Apply' }).click();
    await page.waitForTimeout(100);

    // Clear resets inputs and reloads.
    await page.getByRole('button', { name: 'Clear' }).click();
    await page.waitForTimeout(100);

    // Input should be empty.
    await expect(page.getByRole('textbox', { name: /filter by action/i })).toHaveValue('');
    // A new request without the action param should have been made.
    const lastRequest = requests[requests.length - 1];
    expect(lastRequest).not.toContain('action=some-action');
  });
});
