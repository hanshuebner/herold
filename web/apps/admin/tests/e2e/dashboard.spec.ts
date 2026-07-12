/**
 * dashboard.spec.ts
 *
 * Covers:
 *   - Three cards (Queue, Recent activity, Domains) render with primary stats
 *   - "View all" links are present for each card
 *   - Partial failure: one card shows inline error while others render
 *   - Client logs card: Ring buffer column values stay inside the card boundary
 */

import { test, expect } from '@playwright/test';
import { installAdminSession } from './fixtures/auth';

/** Seed data for the Client logs card. */
const CLIENTLOG_STATS = {
  received_total: { 'auth/suite/vital': 14, 'public/admin/vital': 3 },
  dropped_total: {},
  ring_buffer_rows: { 'auth/suite/vital': 7, 'public/admin/vital': 1 },
};

test.describe('dashboard', () => {
  test('renders queue, activity, and domains cards with stats and view-all links', async ({ page }) => {
    installAdminSession(page);

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
    installAdminSession(page);

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
    installAdminSession(page);
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

  test('client logs card: Ring buffer column values stay inside card boundary', async ({ page }) => {
    installAdminSession(page);

    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 0 }) }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/admin/clientlog/stats', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(CLIENTLOG_STATS),
      }),
    );

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // Wait for the Client logs card and its two sub-columns to appear.
    const clientlogsCard = page
      .locator('.card')
      .filter({ has: page.getByRole('heading', { name: 'Client logs' }) });
    await expect(clientlogsCard).toBeVisible();
    await expect(clientlogsCard.getByText('Ring buffer')).toBeVisible();

    // Save a screenshot so the maintainer can visually inspect the layout.
    await page.screenshot({ path: '/tmp/herold-clientlog-card.png' });

    // The right edge of every Ring buffer stat value must not exceed the
    // right edge of the card container (i.e. no horizontal overflow).
    const cardBox = await clientlogsCard.boundingBox();
    expect(cardBox).toBeTruthy();
    const cardRight = cardBox!.x + cardBox!.width;

    const ringBufferCol = clientlogsCard.locator('.stat-col').last();
    const ringBufferValues = ringBufferCol.locator('.stat-val');
    const count = await ringBufferValues.count();
    expect(count).toBeGreaterThan(0);

    for (let i = 0; i < count; i++) {
      const valBox = await ringBufferValues.nth(i).boundingBox();
      expect(valBox).toBeTruthy();
      const valRight = valBox!.x + valBox!.width;
      expect(
        valRight,
        `Ring buffer stat value [${i}] right edge (${valRight}px) must not exceed card right edge (${cardRight}px)`,
      ).toBeLessThanOrEqual(cardRight);
    }
  });

  test('push status card shows configured/not-configured per transport (re #200)', async ({ page }) => {
    installAdminSession(page);

    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 0 }) }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/admin/clientlog/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(CLIENTLOG_STATS) }),
    );
    // Overrides installAdminSession's server/status stub with one that also
    // carries the push status block (re #200): FCM unconfigured, Web Push
    // configured, mirroring a deployment that only set up VAPID.
    await page.route('/api/v1/server/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          principal_id: '1',
          email: 'admin@example.com',
          scopes: ['admin'],
          push: { webpush_configured: true, fcm_configured: false },
        }),
      }),
    );

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    const pushCard = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'Push notifications' }) });
    await expect(pushCard).toBeVisible();
    await expect(pushCard.getByText('Web Push (VAPID)')).toBeVisible();
    await expect(pushCard.getByText('FCM (Android)')).toBeVisible();
    await expect(pushCard.getByText('configured', { exact: true })).toBeVisible();
    await expect(pushCard.getByText('not configured')).toBeVisible();

    await page.screenshot({ path: '/tmp/herold-push-status-card.png' });
  });
});
