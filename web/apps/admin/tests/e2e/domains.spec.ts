/**
 * domains.spec.ts
 *
 * Covers:
 *   - Domain list renders
 *   - Create domain modal posts and causes list reload
 *   - Detail page shows aliases section (via /api/v1/aliases?domain=...)
 *   - Alias create modal posts to /api/v1/aliases with CSRF token
 *   - Alias delete two-stage confirm: first click shows confirm/cancel, second deletes
 */

import { test, expect } from '@playwright/test';

const NOW = '2024-06-01T00:00:00Z';

const DOMAINS_RESP = {
  items: [
    { name: 'example.com', local: true, created_at: NOW },
    { name: 'test.org', local: true, created_at: NOW },
  ],
  next: null,
};

const ALIASES = [
  { id: 'alias-1', local: 'postmaster', domain: 'example.com', target_principal_id: '1', expires_at: null, created_at: NOW },
  { id: 'alias-2', local: 'info', domain: 'example.com', target_principal_id: '2', expires_at: null, created_at: NOW },
];

const ALIASES_RESP = { items: ALIASES, next: null };

function installAuthRoutes(page: import('@playwright/test').Page) {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
    }),
  );
}

test.describe('domains', () => {
  test.beforeEach(async ({ page }) => {
    installAuthRoutes(page);
  });

  test('domain list renders all domains', async ({ page }) => {
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(DOMAINS_RESP),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Domains' }).click();
    await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible();
    // Use .domain-name spans to avoid matching partial email text in the topbar.
    await expect(page.locator('.domain-name').filter({ hasText: 'example.com' })).toBeVisible();
    await expect(page.locator('.domain-name').filter({ hasText: 'test.org' })).toBeVisible();
  });

  test('create domain modal posts and list reloads', async ({ page }) => {
    const updatedDomains = {
      items: [
        ...DOMAINS_RESP.items,
        { name: 'newdomain.net', local: true, created_at: NOW },
      ],
      next: null,
    };
    let createCalled = false;
    let listCallCount = 0;

    await page.route('/api/v1/domains*', async (route) => {
      const url = new URL(route.request().url());
      if (route.request().method() === 'POST' && url.pathname === '/api/v1/domains') {
        createCalled = true;
        await route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ name: 'newdomain.net', local: true, created_at: NOW }),
        });
        return;
      }
      // GET list -- return updated list after create.
      if (url.pathname === '/api/v1/domains') {
        listCallCount++;
        const resp = listCallCount > 1 ? updatedDomains : DOMAINS_RESP;
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(resp) });
      }
      return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Domains' }).click();
    await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible();

    await page.getByRole('button', { name: 'New domain' }).click();
    await expect(page.getByRole('dialog', { name: 'New domain' })).toBeVisible();

    await page.locator('#cd-name').fill('newdomain.net');
    await page.getByRole('button', { name: 'Create domain' }).click();

    expect(createCalled).toBe(true);
    // Dialog should close after success.
    await expect(page.getByRole('dialog', { name: 'New domain' })).not.toBeVisible();
  });

  test('domain detail page shows aliases section', async ({ page }) => {
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(DOMAINS_RESP),
      }),
    );
    await page.route('/api/v1/aliases*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(ALIASES_RESP),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Domains' }).click();
    await page.locator('.domain-name').filter({ hasText: 'example.com' }).click();

    await expect(page.getByRole('heading', { name: 'Aliases' })).toBeVisible();
    // Aliases display as "local@domain" format.
    await expect(page.getByText('postmaster@example.com')).toBeVisible();
    await expect(page.getByText('info@example.com')).toBeVisible();
  });

  test('alias create modal posts to /api/v1/aliases with CSRF token', async ({ page }) => {
    let aliasCreateHeaders: Record<string, string | undefined> | null = null;

    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DOMAINS_RESP) }),
    );
    await page.route('/api/v1/aliases*', async (route) => {
      const url = new URL(route.request().url());
      if (route.request().method() === 'POST' && url.pathname === '/api/v1/aliases') {
        aliasCreateHeaders = route.request().headers() as Record<string, string | undefined>;
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ id: 'alias-3', local: 'support', domain: 'example.com', target_principal_id: '1', expires_at: null, created_at: NOW }),
        });
      }
      // GET aliases for domain.
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ALIASES_RESP) });
    });

    // Mock principals endpoint for the autocomplete search.
    // DomainDetailView's fetchPrincipals() uses fetch() directly and expects
    // { items: [...] } envelope from GET /api/v1/principals?limit=20.
    await page.route('/api/v1/principals*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          items: [
            { id: 1, email: 'admin@example.com', display_name: 'Admin', flags: 1, created_at: NOW },
          ],
        }),
      }),
    );

    // Set CSRF cookie.
    await page.context().addCookies([
      { name: 'herold_admin_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Domains' }).click();
    await page.locator('.domain-name').filter({ hasText: 'example.com' }).click();

    await expect(page.getByRole('heading', { name: 'Aliases' })).toBeVisible();
    await page.getByRole('button', { name: 'New alias' }).click();

    await expect(page.getByRole('dialog', { name: 'New alias' })).toBeVisible();
    await page.locator('#ca-local').fill('support');

    // Trigger autocomplete.
    await page.locator('#ca-principal').fill('admin');
    await expect(page.locator('.ac-option').first()).toBeVisible();
    await page.locator('.ac-option').first().click();

    await page.getByRole('button', { name: 'Create alias' }).click();

    expect(aliasCreateHeaders).not.toBeNull();
    expect(aliasCreateHeaders!['x-csrf-token']).toBe('test-csrf-token');
  });

  test('alias delete two-stage confirm: first shows confirm, second deletes', async ({ page }) => {
    let deleteAliasId: string | null = null;

    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DOMAINS_RESP) }),
    );
    // Use regex so /api/v1/aliases/:id DELETE calls are also intercepted
    // (Playwright glob '*' does not cross '/').
    await page.route(/\/api\/v1\/aliases/, (route) => {
      const url = new URL(route.request().url());
      if (route.request().method() === 'DELETE') {
        deleteAliasId = url.pathname.split('/').pop() ?? null;
        return route.fulfill({ status: 204 });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ALIASES_RESP) });
    });

    await page.context().addCookies([
      { name: 'herold_admin_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Domains' }).click();
    await page.locator('.domain-name').filter({ hasText: 'example.com' }).click();

    await expect(page.getByText('postmaster@example.com')).toBeVisible();

    // Click the Delete button on the first alias row (btn-ghost-sm within the table).
    // Scope to the aliases table to avoid matching the danger-zone Delete button.
    await page.locator('table').getByRole('button', { name: 'Delete' }).first().click();
    await expect(page.getByText('Delete?')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Confirm' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Cancel' })).toBeVisible();

    // Confirm deletes the alias.
    await page.getByRole('button', { name: 'Confirm' }).click();
    expect(deleteAliasId).toBe('alias-1');
  });
});
