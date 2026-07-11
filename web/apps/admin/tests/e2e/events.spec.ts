/**
 * events.spec.ts
 *
 * Covers:
 *   - Events list renders entries with correct columns
 *   - Action filter sends action parameter to the API
 *   - Actor filter sends actor_id parameter to the API
 *   - Since / until date filters are sent as query parameters
 *   - Load-more advances the cursor and appends new rows (before_id cursor)
 *   - Clear button resets filters and reloads
 *   - Entries are displayed newest-first
 */

import { test, expect } from '@playwright/test';
import { installAdminSession } from './fixtures/auth';

const EVENTS_PAGE1 = Array.from({ length: 10 }, (_, i) => ({
  id: i + 1,
  at: new Date(Date.now() - (i + 1) * 60_000).toISOString(),
  action: i === 0 ? 'smtp.rcpt.resolve' : `smtp.action.${i}`,
  actor_id: i === 0 ? 'alice@example.local' : `system`,
  subject: `subject-${i}`,
  remote_addr: '127.0.0.1',
  outcome: i % 3 === 0 ? 'failure' : 'success',
  message: `Nachricht ${i}`,
  domain: 'example.local',
  metadata: null,
}));

const EVENTS_PAGE2 = Array.from({ length: 5 }, (_, i) => ({
  id: i + 11,
  at: new Date(Date.now() - (i + 11) * 60_000).toISOString(),
  action: `older.action.${i}`,
  actor_id: 'system',
  subject: `subject-old-${i}`,
  remote_addr: '127.0.0.1',
  outcome: 'success',
  message: '',
  domain: 'example.local',
  metadata: null,
}));

test.describe('events', () => {
  test.beforeEach(async ({ page }) => {
    installAdminSession(page);
  });

  test('events list renders entries with all columns', async ({ page }) => {
    await page.route('/api/v1/admin/system-events*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: EVENTS_PAGE1, next: 'cursor-xyz' }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();

    // Check table headers.
    await expect(page.getByRole('columnheader', { name: 'When' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Action' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Principal' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Outcome' })).toBeVisible();

    // Check first entry renders.
    await expect(page.getByText('smtp.rcpt.resolve')).toBeVisible();
    await expect(page.getByText('alice@example.local')).toBeVisible();
    await expect(page.getByText('success').first()).toBeVisible();

    // Verify "Weitere laden" button is visible when next cursor is present.
    await expect(page.getByRole('button', { name: 'Load more' })).toBeVisible();
  });

  test('entries are displayed newest-first', async ({ page }) => {
    await page.route('/api/v1/admin/system-events*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: EVENTS_PAGE1, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();

    const rows = page.getByRole('row');
    // Row 0 is the header; row 1 is the first data row (newest).
    await expect(rows.nth(1)).toContainText('smtp.rcpt.resolve');
    // The oldest entry must appear somewhere below the newest.
    const newestY = await rows.nth(1).boundingBox().then((b) => b?.y ?? 0);
    const oldestRow = page.getByRole('row', { name: /smtp\.action\.9/ });
    await expect(oldestRow).toBeVisible();
    const oldestY = await oldestRow.boundingBox().then((b) => b?.y ?? 0);
    expect(oldestY).toBeGreaterThan(newestY);
  });

  test('action filter sends action parameter', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/system-events*', (route) => {
      requests.push(route.request().url());
      const url = new URL(route.request().url());
      const action = url.searchParams.get('action') ?? '';
      const filtered = EVENTS_PAGE1.filter((e) =>
        action ? e.action === action : true,
      );
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: filtered, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();

    await page.getByRole('textbox', { name: /filter by action/i }).fill('smtp.rcpt.resolve');
    await page.getByRole('button', { name: 'Apply' }).click();

    await page.waitForTimeout(200);
    const filtered = requests.filter((u) => u.includes('action='));
    expect(filtered.length).toBeGreaterThan(0);
    expect(filtered[filtered.length - 1]).toContain('action=smtp.rcpt.resolve');
  });

  test('actor filter sends actor_id parameter', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/system-events*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: EVENTS_PAGE1, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();

    await page.getByRole('textbox', { name: /filter by principal/i }).fill('alice@example.local');
    await page.getByRole('button', { name: 'Apply' }).click();

    await page.waitForTimeout(200);
    const actorFiltered = requests.filter((u) => u.includes('actor_id='));
    expect(actorFiltered.length).toBeGreaterThan(0);
    expect(actorFiltered[actorFiltered.length - 1]).toContain('actor_id=alice%40example.local');
  });

  test('since/until filters are sent as query parameters', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/system-events*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: EVENTS_PAGE1, next: null }) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();

    await page.locator('#events-since').fill('2024-06-01T00:00');
    await page.locator('#events-until').fill('2024-06-02T00:00');
    await page.getByRole('button', { name: 'Apply' }).click();

    await page.waitForTimeout(200);
    const sinceRequests = requests.filter((u) => u.includes('since='));
    expect(sinceRequests.length).toBeGreaterThan(0);
    const untilRequests = requests.filter((u) => u.includes('until='));
    expect(untilRequests.length).toBeGreaterThan(0);
  });

  test('load-more appends additional rows and sends before_id cursor', async ({ page }) => {
    let callCount = 0;
    const loadMoreUrls: string[] = [];

    await page.route('/api/v1/admin/system-events*', (route) => {
      callCount++;
      const url = new URL(route.request().url());
      const hasCursor = url.searchParams.has('before_id');
      if (hasCursor) {
        loadMoreUrls.push(url.toString());
      }
      if (callCount === 1 || !hasCursor) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ items: EVENTS_PAGE1, next: 'cursor-xyz' }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: EVENTS_PAGE2, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();
    await expect(page.getByText('smtp.rcpt.resolve')).toBeVisible();

    const loadMoreBtn = page.getByRole('button', { name: 'Load more' });
    await expect(loadMoreBtn).toBeVisible();
    await loadMoreBtn.click();

    await expect(page.getByText('older.action.0')).toBeVisible();
    expect(loadMoreUrls.length).toBeGreaterThan(0);
    expect(loadMoreUrls[0]).toContain('before_id=');
  });

  test('clear button resets filters and reloads', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/system-events*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: EVENTS_PAGE1, next: null }) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Events' }).click();
    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();

    await page.getByRole('textbox', { name: /filter by action/i }).fill('smtp.some.action');
    await page.getByRole('button', { name: 'Apply' }).click();
    await page.waitForTimeout(100);

    await page.getByRole('button', { name: 'Clear' }).click();
    await page.waitForTimeout(100);

    await expect(page.getByRole('textbox', { name: /filter by action/i })).toHaveValue('');
    const lastRequest = requests[requests.length - 1];
    expect(lastRequest).not.toContain('action=smtp.some.action');
  });
});
