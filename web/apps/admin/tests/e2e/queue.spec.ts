/**
 * queue.spec.ts
 *
 * Covers:
 *   - Queue list renders items
 *   - State filter dropdown changes the loaded set
 *   - Queue item detail action buttons: retry, hold, release, delete
 *   - Delete confirm dialog two-stage flow
 *   - Flush deferred confirm dialog posts to /api/v1/queue/flush
 */

import { test, expect } from '@playwright/test';

const NOW = new Date(Date.now() + 5 * 60_000).toISOString(); // 5 min from now

const QUEUE_ITEMS = [
  {
    id: 'q1',
    principal_id: '1',
    mail_from: 'sender@example.com',
    rcpt_to: 'rcpt@dest.org',
    envelope_id: 'env-1',
    state: 'queued',
    attempts: 0,
    next_attempt_at: NOW,
    created_at: new Date(Date.now() - 60_000).toISOString(),
  },
  {
    id: 'q2',
    principal_id: '1',
    mail_from: 'sender@example.com',
    rcpt_to: 'other@dest.org',
    envelope_id: 'env-2',
    state: 'deferred',
    attempts: 2,
    last_error: 'connection refused',
    next_attempt_at: NOW,
    created_at: new Date(Date.now() - 3600_000).toISOString(),
  },
  {
    id: 'q3',
    principal_id: '1',
    mail_from: 'batch@example.com',
    rcpt_to: 'third@dest.org',
    envelope_id: 'env-3',
    state: 'held',
    attempts: 1,
    created_at: new Date(Date.now() - 7200_000).toISOString(),
  },
];

const QUEUE_RESP = { items: QUEUE_ITEMS, next: null };

function installAuthRoutes(page: import('@playwright/test').Page) {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
    }),
  );
}

test.describe('queue', () => {
  test.beforeEach(async ({ page }) => {
    installAuthRoutes(page);
    await page.context().addCookies([
      { name: 'herold_admin_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);
  });

  test('queue list renders items with state chips', async ({ page }) => {
    // Use regex so sub-paths like /api/v1/queue/stats are also intercepted.
    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/queue') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(QUEUE_RESP),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    await expect(page.getByRole('heading', { name: 'Queue' })).toBeVisible();
    await expect(page.getByText('sender@example.com').first()).toBeVisible();
    // Scope chip lookups inside the table to avoid matching the state-filter <option> elements.
    const tbody = page.locator('table tbody');
    await expect(tbody.locator('.chip-blue')).toBeVisible();   // queued
    await expect(tbody.locator('.chip-amber')).toBeVisible();  // deferred
    await expect(tbody.locator('.chip-grey')).toBeVisible();   // held
  });

  test('state filter select narrows results to deferred only', async ({ page }) => {
    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      const stateParam = url.searchParams.get('state');
      const items = stateParam ? QUEUE_ITEMS.filter((i) => i.state === stateParam) : QUEUE_ITEMS;
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    // Verify initial load shows multiple states (scoped to table body chips).
    const tbody2 = page.locator('table tbody');
    await expect(tbody2.locator('.chip-blue')).toBeVisible();

    // Change state filter to "Deferred".
    await page.getByRole('combobox', { name: /state filter/i }).selectOption('deferred');

    // After reload only deferred items show.
    await expect(tbody2.locator('.chip-amber')).toBeVisible();
    await expect(tbody2.locator('.chip-grey')).not.toBeVisible();
  });

  test('queue item detail Retry button hits /api/v1/queue/:id/retry', async ({ page }) => {
    let retryCalled = false;

    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/queue') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_RESP) });
      }
      if (url.pathname === '/api/v1/queue/q2') {
        if (route.request().method() === 'GET') {
          return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_ITEMS[1]) });
        }
      }
      if (url.pathname === '/api/v1/queue/q2/retry') {
        retryCalled = true;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ...QUEUE_ITEMS[1], state: 'queued', attempts: 3 }),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    // Click the deferred row (q2).
    await page.getByText('other@dest.org').click();
    await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    await page.getByRole('button', { name: 'Retry' }).click();
    expect(retryCalled).toBe(true);
  });

  test('queue item detail Hold button hits /api/v1/queue/:id/hold', async ({ page }) => {
    let holdCalled = false;

    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/queue') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_RESP) });
      }
      if (url.pathname === '/api/v1/queue/q1') {
        if (route.request().method() === 'GET') {
          return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_ITEMS[0]) });
        }
      }
      if (url.pathname === '/api/v1/queue/q1/hold') {
        holdCalled = true;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ...QUEUE_ITEMS[0], state: 'held' }),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    await page.getByText('rcpt@dest.org').click();
    await expect(page.getByRole('button', { name: 'Hold' })).toBeVisible();
    await page.getByRole('button', { name: 'Hold' }).click();
    expect(holdCalled).toBe(true);
  });

  test('queue item detail Release button hits /api/v1/queue/:id/release', async ({ page }) => {
    let releaseCalled = false;

    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/queue') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_RESP) });
      }
      if (url.pathname === '/api/v1/queue/q3') {
        if (route.request().method() === 'GET') {
          return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_ITEMS[2]) });
        }
      }
      if (url.pathname === '/api/v1/queue/q3/release') {
        releaseCalled = true;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ...QUEUE_ITEMS[2], state: 'queued' }),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    await page.getByText('third@dest.org').click();
    await expect(page.getByRole('button', { name: 'Release' })).toBeVisible();
    await page.getByRole('button', { name: 'Release' }).click();
    expect(releaseCalled).toBe(true);
  });

  test('queue item detail delete two-stage confirm navigates back after delete', async ({ page }) => {
    let deleteCalled = false;

    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/queue') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_RESP) });
      }
      if (url.pathname === '/api/v1/queue/q1') {
        if (route.request().method() === 'DELETE') {
          deleteCalled = true;
          return route.fulfill({ status: 204 });
        }
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_ITEMS[0]) });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    await page.getByText('rcpt@dest.org').click();

    // First click shows confirm dialog inline.
    await page.getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByText('Delete?')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Confirm delete' })).toBeVisible();

    await page.getByRole('button', { name: 'Confirm delete' }).click();
    expect(deleteCalled).toBe(true);
    // After delete, router.navigate('/queue') is called.
    await expect(page.getByRole('heading', { name: 'Queue' })).toBeVisible();
  });

  test('flush deferred dialog confirms and posts to /api/v1/queue/flush', async ({ page }) => {
    let flushCalled = false;

    await page.route(/\/api\/v1\/queue/, (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/queue/flush') {
        flushCalled = true;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ flushed: 3 }),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(QUEUE_RESP) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Queue' }).click();
    await page.getByRole('button', { name: 'Flush deferred' }).click();

    await expect(page.getByRole('dialog', { name: 'Flush deferred items' })).toBeVisible();
    await expect(page.getByText(/move all deferred items/i)).toBeVisible();

    // Click the flush button inside the dialog.
    await page.getByRole('dialog', { name: 'Flush deferred items' }).getByRole('button', { name: 'Flush deferred' }).click();

    expect(flushCalled).toBe(true);
    // Success message shows flushed count.
    await expect(page.getByRole('status')).toContainText('3');
  });
});
