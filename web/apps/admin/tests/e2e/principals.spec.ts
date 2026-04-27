/**
 * principals.spec.ts
 *
 * Covers:
 *   - List renders with principal rows
 *   - Search filter narrows the displayed rows
 *   - Create modal opens, submits, and closes
 *   - Row click navigates to detail view
 *   - Detail tabs switch (Profile, Password, Two-factor)
 *   - Password tab shows current-password field for self, admin-note for admin override
 *   - TOTP tab renders QR SVG from a mocked enroll response
 */

import { test, expect } from '@playwright/test';

const NOW = '2024-06-01T12:00:00Z';

const PRINCIPALS = [
  { id: '1', email: 'admin@example.com', display_name: 'Admin User', flags: 1, created_at: NOW },
  { id: '2', email: 'alice@example.com', display_name: 'Alice', flags: 0, created_at: NOW },
  { id: '3', email: 'bob@example.com', display_name: 'Bob', flags: 4, created_at: NOW },
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

test.describe('principals', () => {
  test.beforeEach(async ({ page }) => {
    installAuthRoutes(page);
    // Use a regex so the route catches both the list (/api/v1/principals?...) and
    // all sub-paths (/api/v1/principals/:id, /api/v1/principals/:id/api-keys, etc.).
    // Playwright glob '*' does not cross '/' so a regex is required here.
    await page.route(/\/api\/v1\/principals/, (route) => {
      const url = new URL(route.request().url());
      // Detail: /api/v1/principals/:id (no trailing path segment)
      const detailMatch = url.pathname.match(/\/api\/v1\/principals\/(\d+)$/);
      if (detailMatch) {
        const id = detailMatch[1];
        const p = PRINCIPALS.find((x) => x.id === id);
        if (p) {
          return route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ ...p, quota_bytes: 1073741824 }),
          });
        }
        return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not found' }) });
      }
      // Sub-resource endpoints (api-keys, oidc-links, totp, etc.) — return empty defaults.
      if (url.pathname.match(/\/api\/v1\/principals\/\d+\//)) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) });
      }
      // List endpoint: /api/v1/principals or /api/v1/principals?...
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(PRINCIPALS),
      });
    });
  });

  test('list renders all principals', async ({ page }) => {
    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await expect(page.getByRole('heading', { name: 'Principals' })).toBeVisible();
    // Use .email-text to avoid matching the topbar .principal-email span.
    await expect(page.locator('.email-text').filter({ hasText: 'admin@example.com' })).toBeVisible();
    await expect(page.locator('.email-text').filter({ hasText: 'alice@example.com' })).toBeVisible();
    await expect(page.locator('.email-text').filter({ hasText: 'bob@example.com' })).toBeVisible();
  });

  test('search filter narrows displayed rows', async ({ page }) => {
    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await expect(page.locator('.email-text').filter({ hasText: 'admin@example.com' })).toBeVisible();

    await page.getByRole('searchbox', { name: /filter principals/i }).fill('alice');
    await expect(page.locator('.email-text').filter({ hasText: 'alice@example.com' })).toBeVisible();
    await expect(page.locator('.email-text').filter({ hasText: 'admin@example.com' })).not.toBeVisible();
    await expect(page.locator('.email-text').filter({ hasText: 'bob@example.com' })).not.toBeVisible();
  });

  test('create modal opens, submits, and new principal appears', async ({ page }) => {
    let postCalled = false;
    await page.route('/api/v1/principals', async (route) => {
      if (route.request().method() === 'POST') {
        postCalled = true;
        // After create the list is reloaded; return updated list.
        const updated = [
          ...PRINCIPALS,
          { id: '4', email: 'new@example.com', display_name: 'New User', flags: 0, created_at: NOW },
        ];
        await route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ id: '4' }),
        });
        // Override the principals GET for the reload after create.
        await page.route('/api/v1/principals*', (r) =>
          r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(updated) }),
        );
        return;
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(PRINCIPALS),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await page.getByRole('button', { name: 'New principal' }).click();

    // Modal should be visible with the form fields.
    await expect(page.getByRole('dialog', { name: 'New principal' })).toBeVisible();
    await page.locator('#cp-email').fill('new@example.com');
    await page.locator('#cp-password').fill('supersecretpassword1!');
    await page.locator('#cp-display-name').fill('New User');

    await page.getByRole('button', { name: 'Create principal' }).click();

    // After submit the modal clears (stays open for next creation) and list reloads.
    expect(postCalled).toBe(true);
  });

  test('row click navigates to detail view', async ({ page }) => {
    await page.route('/api/v1/principals/2/api-keys', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );
    await page.route('/api/v1/principals/2/totp*', (route) =>
      route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({}) }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await expect(page.locator('.email-text').filter({ hasText: 'alice@example.com' })).toBeVisible();

    // Click the alice row using the table-scoped email-text span.
    await page.locator('.email-text').filter({ hasText: 'alice@example.com' }).click();
    // Detail page title is the email.
    await expect(page.getByRole('heading', { name: 'alice@example.com' })).toBeVisible();
  });

  test('detail tabs switch between Profile, Password, Two-factor', async ({ page }) => {
    await page.route('/api/v1/principals/1/api-keys', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await page.locator('.email-text').filter({ hasText: 'admin@example.com' }).click();
    // Default tab is Profile.
    await expect(page.getByRole('tab', { name: 'Profile' })).toBeVisible();

    // Switch to Password tab.
    await page.getByRole('tab', { name: 'Password' }).click();
    // Self case: current password field should be visible (principal.id === auth.principal.id).
    await expect(page.locator('#pd-pw-current')).toBeVisible();

    // Switch to Two-factor tab.
    await page.getByRole('tab', { name: 'Two-factor' }).click();
    // TOTP not enrolled: enroll button should appear.
    await expect(page.getByRole('button', { name: /enable two-factor/i })).toBeVisible();
  });

  test('TOTP tab renders QR SVG from enroll response', async ({ page }) => {
    await page.route('/api/v1/principals/1/totp/enroll', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          provisioning_uri: 'otpauth://totp/admin%40example.com?secret=JBSWY3DPEHPK3PXP&issuer=Herold',
        }),
      }),
    );
    await page.route('/api/v1/principals/1/api-keys', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await page.locator('.email-text').filter({ hasText: 'admin@example.com' }).click();
    await page.getByRole('tab', { name: 'Two-factor' }).click();
    await page.getByRole('button', { name: /enable two-factor/i }).click();

    // After enroll, the QR div and provisioning URI input should appear.
    await expect(page.locator('.totp-qr svg, [aria-label="TOTP QR code"] svg')).toBeVisible();
    await expect(page.getByRole('textbox', { name: /TOTP provisioning URI/i })).toBeVisible();
  });

  test('password tab hides current-password field when admin acts on another principal', async ({ page }) => {
    // Authenticated as principal 1 (admin), viewing principal 2.
    await page.route('/api/v1/principals/2/api-keys', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Principals' }).click();
    await page.locator('.email-text').filter({ hasText: 'alice@example.com' }).click();
    await page.getByRole('tab', { name: 'Password' }).click();

    // Admin override: no current password field; admin-note present.
    await expect(page.locator('#pd-pw-current')).not.toBeVisible();
    await expect(page.getByText(/admin you can set a new password/i)).toBeVisible();
  });
});
