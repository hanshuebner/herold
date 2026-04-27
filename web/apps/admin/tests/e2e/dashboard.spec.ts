/**
 * dashboard.spec.ts
 *
 * Covers:
 *   - Three cards (Queue, Recent activity, Domains) render with primary stats
 *   - "View all" links are present for each card
 *   - Partial failure: one card shows inline error while others render
 */

import { test, expect } from '@playwright/test';

function installAuthRoutes(page: import('@playwright/test').Page) {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
    }),
  );
}

test.describe('dashboard', () => {
  test('renders queue, activity, and domains cards with stats and view-all links', async ({ page }) => {
    installAuthRoutes(page);

    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ queued: 5, deferred: 2, held: 1, failed: 0 }),
      }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            id: 'a1',
            action: 'principal.create',
            principal_email: 'admin@example.com',
            created_at: new Date(Date.now() - 60_000).toISOString(),
          },
        ]),
      }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { name: 'example.com', created_at: '2024-01-01T00:00:00Z' },
          { name: 'test.org', created_at: '2024-02-01T00:00:00Z' },
        ]),
      }),
    );

    await page.goto('/admin/');
    // Auth bootstrap routes to /dashboard automatically.
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // Queue card: stat shows active total (queued+deferred+held = 8)
    await expect(page.getByRole('heading', { name: 'Queue' })).toBeVisible();
    await expect(page.getByText('8')).toBeVisible();
    // View all button for queue
    const queueViewAll = page.getByRole('button', { name: 'View all' }).first();
    await expect(queueViewAll).toBeVisible();

    // Recent activity card
    await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible();
    await expect(page.getByText('principal.create')).toBeVisible();
    // Second "View all" button
    await expect(page.getByRole('button', { name: 'View all' }).nth(1)).toBeVisible();

    // Domains card shows count. Use locator scoped to the Domains card to avoid
    // ambiguity: the queue stat-val `<dd>` for "deferred: 2" and the card-stat
    // `<p>` for domain count both contain "2".
    const domainsCard = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'Domains' }) });
    await expect(domainsCard).toBeVisible();
    await expect(domainsCard.locator('.card-stat')).toHaveText('2');
    await expect(domainsCard.getByText('example.com')).toBeVisible();
    // Third "View all" button
    await expect(page.getByRole('button', { name: 'View all' }).nth(2)).toBeVisible();
  });

  test('queue card shows inline error when stats endpoint fails', async ({ page }) => {
    installAuthRoutes(page);

    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'internal error' }),
      }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // Queue card has an inline error; other cards still visible.
    await expect(page.getByRole('heading', { name: 'Queue' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible();
  });

  test('clicking queue View all navigates to /queue', async ({ page }) => {
    installAuthRoutes(page);
    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 0 }) }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/queue*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
    await page.getByRole('button', { name: 'View all' }).first().click();
    await expect(page.getByRole('heading', { name: 'Queue' })).toBeVisible();
  });
});
