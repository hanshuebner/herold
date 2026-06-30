/**
 * Shared auth mock helpers for admin SPA e2e tests.
 *
 * The admin SPA bootstrap sequence (since commit 2607cb46) probes
 * GET /api/v1/auth/me first to verify session validity and read roles +
 * elevation_expires_at. Only after confirming admin role WITH active elevation
 * does it call GET /api/v1/server/status. Tests that need the shell to render
 * must mock both endpoints.
 *
 * installAdminSession() is the single entry point for the common case: an
 * authenticated admin principal with an active TOTP elevation. Call it before
 * page.goto() in any test that needs the shell (nav rail + views) to render.
 */

import type { Page } from '@playwright/test';

export const ADMIN_PRINCIPAL_ID = '1';
export const ADMIN_EMAIL = 'admin@example.com';

/** RFC 3339 timestamp 1 hour in the future, representing an active elevation. */
function activeElevation(): string {
  return new Date(Date.now() + 3_600_000).toISOString();
}

/**
 * Install route mocks for the two auth bootstrap endpoints so that the SPA
 * considers the session authenticated with an active elevation and transitions
 * directly to status='ready', rendering the shell.
 *
 *   GET /api/v1/auth/me      -> 200, roles: ['admin'], active elevation
 *   GET /api/v1/server/status -> 200, principal_id + email + scopes
 */
export function installAdminSession(page: Page): void {
  void page.route('/api/v1/auth/me', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        principal_id: ADMIN_PRINCIPAL_ID,
        email: ADMIN_EMAIL,
        scopes: ['admin'],
        roles: ['admin'],
        elevation_expires_at: activeElevation(),
      }),
    }),
  );
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        principal_id: ADMIN_PRINCIPAL_ID,
        email: ADMIN_EMAIL,
        scopes: ['admin'],
      }),
    }),
  );
}
