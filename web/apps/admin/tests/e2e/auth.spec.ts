/**
 * auth.spec.ts
 *
 * Covers:
 *   - Login page renders email and password fields (session absent)
 *   - Wrong credentials show error inline
 *   - TOTP step-up: password triggers TOTP field on second submit
 *   - Logout clears session and routes to /login
 *   - Happy-path login: correct credentials -> dashboard redirect
 *
 * All tests mock GET /api/v1/auth/me because that is now the first call in
 * bootstrap(). Tests that exercise the unauthenticated state return 401 from
 * auth/me; the logout and happy-path tests return an admin principal with an
 * active elevation so bootstrap reaches status='ready'.
 *
 * The happy-path login test uses page.route() with { times: 1 } to return 401
 * on the initial bootstrap (so the login form is displayed) and then 200 with
 * admin + elevation on the post-login bootstrap (so the dashboard renders).
 * Routes registered later take priority (LIFO), so the single-use 401 handler
 * is registered after the permanent 200 handler.
 */

import { test, expect } from '@playwright/test';
import { installAdminSession, ADMIN_PRINCIPAL_ID, ADMIN_EMAIL } from './fixtures/auth';

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
    // Bootstrap probes auth/me first; 401 -> unauthenticated -> login form.
    await page.route('/api/v1/auth/me', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'not authenticated' }) }),
    );
    await page.goto('/admin/');
    await expect(page.locator('#email')).toBeVisible();
    await expect(page.locator('#password')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toBeVisible();
    await expect(page.locator('#totp-code')).not.toBeVisible();
  });

  test('wrong credentials shows inline error', async ({ page }) => {
    await page.route('/api/v1/auth/me', (route) =>
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
    await page.route('/api/v1/auth/me', (route) =>
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
    // Register the permanent 200 handler first (lower priority due to LIFO).
    // After the single-use 401 fires and is removed, this handler takes over
    // for the post-login bootstrap call to auth/me.
    void page.route('/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          principal_id: ADMIN_PRINCIPAL_ID,
          email: ADMIN_EMAIL,
          scopes: ['admin'],
          roles: ['admin'],
          elevation_expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        }),
      }),
    );
    // Register the single-use 401 handler last (higher priority). It fires for
    // the initial bootstrap on page load and is then removed.
    void page.route(
      '/api/v1/auth/me',
      (route) =>
        route.fulfill({
          status: 401,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'not authenticated' }),
        }),
      { times: 1 },
    );

    // After the post-login bootstrap confirms elevation, it calls server/status.
    void page.route('/api/v1/server/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ principal_id: ADMIN_PRINCIPAL_ID, email: ADMIN_EMAIL, scopes: ['admin'] }),
      }),
    );

    void page.route('/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: {
          'Set-Cookie': 'herold_admin_csrf=test-csrf-token; Path=/',
        },
        body: JSON.stringify({
          principal_id: ADMIN_PRINCIPAL_ID,
          scopes: ['admin'],
          roles: ['admin'],
          elevation_expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        }),
      }),
    );

    // DashboardView fetches these on mount.
    void page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 3, deferred: 1 }) }),
    );
    void page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    void page.route('/api/v1/domains*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await expect(page.locator('#email')).toBeVisible();
    await loginWith(page, 'admin@example.com', 'correct');

    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
  });

  test('logout posts to /api/v1/auth/logout and routes to /login', async ({ page }) => {
    // Start authenticated so the dashboard is rendered immediately.
    installAdminSession(page);

    void page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 0 }) }),
    );
    void page.route('/api/v1/audit*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    void page.route('/api/v1/domains*', (route) =>
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
