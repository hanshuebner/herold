/**
 * auth.spec.ts
 *
 * Covers:
 *   - Login happy path (correct credentials -> dashboard redirect)
 *   - Wrong credentials show error inline
 *   - TOTP step-up: password triggers TOTP field on second submit
 *   - Logout clears session and routes to /login
 */

import { test, expect } from '@playwright/test';

async function loginWith(
  page: import('@playwright/test').Page,
  email: string,
  password: string,
) {
  await page.fill('#email', email);
  await page.fill('#password', password);
  await page.click('button[type="submit"]');
}

test.describe('auth', () => {
  test('login page renders email and password fields', async ({ page }) => {
    // Bootstrap returns 401 so we stay on login.
    await page.route('/api/v1/server/status', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'not authenticated' }) }),
    );
    await page.goto('/admin/');
    await expect(page.locator('#email')).toBeVisible();
    await expect(page.locator('#password')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toBeVisible();
    await expect(page.locator('#totp-code')).not.toBeVisible();
  });

  test('wrong credentials shows inline error', async ({ page }) => {
    await page.route('/api/v1/server/status', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'not authenticated' }) }),
    );
    await page.route('/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'Invalid email or password.' }),
      }),
    );
    await page.goto('/admin/');
    await loginWith(page, 'bad@example.com', 'wrongpass');
    await expect(page.locator('[role="alert"]')).toBeVisible();
    await expect(page.locator('[role="alert"]')).toContainText('Invalid email or password.');
    await expect(page.locator('#totp-code')).not.toBeVisible();
  });

  test('TOTP step-up shows authenticator code field', async ({ page }) => {
    await page.route('/api/v1/server/status', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'not authenticated' }) }),
    );
    await page.route('/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ step_up_required: true }),
      }),
    );
    await page.goto('/admin/');
    await loginWith(page, 'admin@example.com', 'totp-required');
    await expect(page.locator('#totp-code')).toBeVisible();
  });

  test('happy path login navigates to dashboard', async ({ page }) => {
    // First bootstrap returns 401 so SPA shows login form.
    await page.route('/api/v1/server/status', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'not authenticated' }) }),
    );

    await page.route('/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: {
          'Set-Cookie': 'herold_admin_csrf=test-csrf-token; Path=/',
        },
        body: JSON.stringify({ principal_id: '1', scopes: ['admin'] }),
      }),
    );

    // After login succeeds the SPA calls router.replace('/dashboard') and
    // DashboardView mounts, which fires its data fetches.
    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 3, deferred: 1 }) }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await expect(page.locator('#email')).toBeVisible();
    await loginWith(page, 'admin@example.com', 'correct');

    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
  });

  test('logout posts to /api/v1/auth/logout and routes to /login', async ({ page }) => {
    await page.route('/api/v1/server/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
      }),
    );
    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 0 }) }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    let logoutCalled = false;
    await page.route('/api/v1/auth/logout', (route) => {
      logoutCalled = true;
      return route.fulfill({ status: 204 });
    });

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    await page.getByRole('button', { name: 'Sign out' }).click();

    await expect(page.locator('#email')).toBeVisible();
    expect(logoutCalled).toBe(true);
  });
});
