/**
 * elevation-expiry.spec.ts
 *
 * Verifies REQ-AS-106: the admin SPA proactively detects TOTP elevation expiry
 * and blanks confidential content without waiting for a user interaction or an
 * API call to return 403.
 *
 * Uses Playwright's page.clock API (fake timers) so the test completes in
 * milliseconds rather than waiting for a real timeout.
 *
 * Flow:
 *   1. Install fake clock in the browser before navigation.
 *   2. Mock auth/me to return an admin principal with a 5-second elevation.
 *   3. Navigate to /admin/ — SPA bootstraps, auth.status => 'ready',
 *      dashboard renders, #scheduleElevationExpiry arms a setTimeout(5 000 ms).
 *   4. Assert "Dashboard" heading is visible (confirms 'ready' state).
 *   5. Fast-forward the browser clock by 6 000 ms — fires the elevation timer.
 *   6. Assert "Two-factor authentication required" modal is visible and
 *      dashboard is gone (confirms proactive 'step_up_pending' transition
 *      without user action).
 */

import { test, expect } from '@playwright/test';
import { ADMIN_PRINCIPAL_ID, ADMIN_EMAIL } from './fixtures/auth';

test.describe('elevation expiry', () => {
  test('proactive lock: dashboard blanks and TOTP modal appears when elevation timer fires', async ({
    page,
  }) => {
    // 1. Install the fake clock before navigating so every setTimeout/Date.now
    //    call made by the SPA is under Playwright's control.
    const now = Date.now();
    await page.clock.install({ time: now });

    // 2. Mock bootstrap endpoints. auth/me returns an active elevation that
    //    expires 5 seconds from the fake-clock start time.
    const elevationExpiresAt = new Date(now + 5_000).toISOString();
    await page.route('/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          principal_id: ADMIN_PRINCIPAL_ID,
          email: ADMIN_EMAIL,
          scopes: ['admin'],
          roles: ['admin'],
          elevation_expires_at: elevationExpiresAt,
        }),
      }),
    );
    await page.route('/api/v1/server/status', (route) =>
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

    // Dashboard data endpoints — all succeed so the view renders fully.
    await page.route('/api/v1/queue/stats', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ queued: 3, deferred: 1 }),
      }),
    );
    await page.route('/api/v1/audit*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      }),
    );
    await page.route('/api/v1/domains*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      }),
    );
    await page.route('/api/v1/admin/clientlog/stats', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ received_total: {}, dropped_total: {}, ring_buffer_rows: {} }),
      }),
    );

    // 3. Navigate. Bootstrap calls auth/me -> 200 -> server/status -> 200 ->
    //    auth.status = 'ready'. #scheduleElevationExpiry arms setTimeout(5 000).
    await page.goto('/admin/');

    // 4. Dashboard is visible: confirms auth.status === 'ready'.
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // 5. Advance the fake clock by 6 000 ms. page.clock.fastForward() fires all
    //    pending timers whose deadline falls within the advanced window — the
    //    5-second elevation timer fires here. No user interaction occurs.
    await page.clock.fastForward(6_000);

    // 6. Proactive transition: auth.status === 'step_up_pending'.
    //    AuthGate stops rendering children; AdminStepUpModal appears.
    await expect(
      page.getByRole('heading', { name: 'Two-factor authentication required' }),
    ).toBeVisible();
    await expect(page.locator('[data-testid="su-code"]')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Dashboard' })).not.toBeVisible();

    // Save a screenshot for the issue comment. The image shows the TOTP modal
    // on a dark backdrop with no dashboard content visible.
    await page.screenshot({ path: '/tmp/herold-elevation-locked.png', fullPage: false });
  });
});
