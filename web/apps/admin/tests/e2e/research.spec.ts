/**
 * research.spec.ts
 *
 * Covers:
 *   - Email research page renders with a search input and queue items
 *   - Search input narrows the loaded list (client-side filter)
 *   - Clicking a row expands inline detail (shows item summary)
 *   - Clicking again collapses the expanded row
 *   - "Load more" button triggers a second fetch and appends rows
 */

import { test, expect } from '@playwright/test';

const ITEMS = [
  {
    id: 'q1',
    principal_id: '1',
    mail_from: 'alice@sender.com',
    rcpt_to: 'bob@recipient.org',
    envelope_id: 'env-1',
    state: 'queued',
    attempts: 0,
    created_at: new Date(Date.now() - 60_000).toISOString(),
  },
  {
    id: 'q2',
    principal_id: '1',
    mail_from: 'charlie@sender.com',
    rcpt_to: 'dave@recipient.org',
    envelope_id: 'env-2',
    state: 'deferred',
    attempts: 2,
    last_error: 'connection refused',
    created_at: new Date(Date.now() - 3600_000).toISOString(),
  },
  {
    id: 'q3',
    principal_id: '2',
    mail_from: 'eve@other.com',
    rcpt_to: 'frank@other.org',
    envelope_id: 'env-3',
    state: 'held',
    attempts: 1,
    created_at: new Date(Date.now() - 7200_000).toISOString(),
  },
];

const MORE_ITEMS = [
  {
    id: 'q4',
    principal_id: '1',
    mail_from: 'page2@sender.com',
    rcpt_to: 'page2@recipient.org',
    envelope_id: 'env-4',
    state: 'done',
    attempts: 3,
    created_at: new Date(Date.now() - 86_400_000).toISOString(),
  },
];

function installAuthRoutes(page: import('@playwright/test').Page) {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
    }),
  );
}

test.describe('research', () => {
  test.beforeEach(async ({ page }) => {
    installAuthRoutes(page);
  });

  test('research page renders queue items in a searchable table', async ({ page }) => {
    await page.route(/\/api\/v1\/queue/, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ITEMS, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Research' }).click();
    await expect(page.getByRole('heading', { name: 'Email research' })).toBeVisible();

    // All items should be listed.
    await expect(page.getByText('alice@sender.com')).toBeVisible();
    await expect(page.getByText('charlie@sender.com')).toBeVisible();
    await expect(page.getByText('eve@other.com')).toBeVisible();
  });

  test('search input narrows the list client-side', async ({ page }) => {
    await page.route(/\/api\/v1\/queue/, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ITEMS, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Research' }).click();
    await expect(page.getByText('alice@sender.com')).toBeVisible();

    await page.getByRole('searchbox', { name: /search by sender or recipient/i }).fill('alice');
    // Only alice's row should remain visible.
    await expect(page.getByText('alice@sender.com')).toBeVisible();
    await expect(page.getByText('charlie@sender.com')).not.toBeVisible();
    await expect(page.getByText('eve@other.com')).not.toBeVisible();
  });

  test('clicking a row shows inline detail with item summary', async ({ page }) => {
    await page.route(/\/api\/v1\/queue/, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ITEMS, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Research' }).click();
    await expect(page.getByText('alice@sender.com')).toBeVisible();

    // Click the alice row to expand it. Use the table row locator to avoid
    // matching the expanded-detail dd which also contains the sender text.
    await page.locator('table tbody tr.table-row').filter({ hasText: 'alice@sender.com' }).first().click();

    // The expanded row should show the inline detail definition list.
    // Scope checks to the expand-cell to avoid strict mode violations with
    // column headers that also contain "Sender"/"Recipient".
    const expandCell = page.locator('.expand-cell').first();
    await expect(expandCell.getByRole('term').filter({ hasText: 'Sender' })).toBeVisible();
    await expect(expandCell.getByRole('term').filter({ hasText: 'Recipient' })).toBeVisible();
    await expect(expandCell.getByText('env-1')).toBeVisible();
  });

  test('clicking an expanded row collapses it', async ({ page }) => {
    await page.route(/\/api\/v1\/queue/, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ITEMS, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Research' }).click();
    await expect(page.getByText('alice@sender.com')).toBeVisible();

    // Expand by clicking the table row (not the expanded detail dd).
    const aliceRow = page.locator('table tbody tr.table-row').filter({ hasText: 'alice@sender.com' }).first();
    await aliceRow.click();
    await expect(page.locator('.expand-cell').first().getByText('env-1')).toBeVisible();

    // Collapse: click the same row again (now the only tr.table-row with alice).
    await aliceRow.click();
    await expect(page.locator('.expand-cell')).not.toBeVisible();
  });

  test('load-more fetches next page and appends rows', async ({ page }) => {
    let callCount = 0;

    await page.route(/\/api\/v1\/queue/, (route) => {
      callCount++;
      const url = new URL(route.request().url());
      const hasAfter = url.searchParams.has('after_id');

      if (callCount === 1 || !hasAfter) {
        // Return page 1 with hasMore = true (exactly PAGE_LIMIT items would
        // trigger it; here we fake it by returning items with a non-null next
        // cursor). The SPA checks cursor !== null to determine hasMore.
        const page1 = Array.from({ length: 50 }, (_, i) => ({
          id: `p1-q${i}`,
          principal_id: '1',
          mail_from: `sender${i}@example.com`,
          rcpt_to: `rcpt${i}@example.com`,
          envelope_id: `env-p1-${i}`,
          state: 'queued',
          attempts: 0,
          created_at: new Date(Date.now() - (i + 1) * 1000).toISOString(),
        }));
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ items: page1, next: 'cursor-p1-last' }),
        });
      }
      // Page 2 (load-more).
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: MORE_ITEMS, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Research' }).click();
    await expect(page.getByRole('heading', { name: 'Email research' })).toBeVisible();
    await expect(page.getByText('sender0@example.com')).toBeVisible();

    // Load-more button should be visible since 50 items = PAGE_LIMIT.
    const loadMoreBtn = page.getByRole('button', { name: 'Load more' });
    await expect(loadMoreBtn).toBeVisible();
    await loadMoreBtn.click();

    await expect(page.getByText('page2@sender.com')).toBeVisible();
  });
});
