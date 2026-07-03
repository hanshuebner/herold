/**
 * session-expiry.spec.ts
 *
 * Verifies REQ-AS-106: the admin SPA proactively detects session expiry
 * and routes to /login without waiting for a user interaction or an API
 * call to return 401. Mirrors the elevation-expiry.spec.ts approach with
 * Playwright's page.clock API (fake timers) so the test completes in
 * milliseconds rather than waiting for a real timeout.
 *
 * Flow:
 *   1. Install fake clock before navigation.
 *   2. Mock auth/me to return an admin principal with session_idle_deadline
 *      5 seconds from the fake-clock start time, and an elevation that does
 *      not expire within the test window.
 *   3. Navigate to /admin/ — SPA bootstraps, auth.status => 'ready',
 *      dashboard renders, #scheduleSessionExpiry arms a setTimeout(5 000 ms).
 *   4. Assert "Dashboard" heading is visible (confirms 'ready' state).
 *   5. Fast-forward the browser clock by 6 000 ms — fires the session timer.
 *   6. Assert the login page/form is visible and the dashboard is gone
 *      (confirms proactive 'unauthenticated' transition without user action).
 */

import { test, expect } from '@playwright/test';
import { ADMIN_PRINCIPAL_ID, ADMIN_EMAIL } from './fixtures/auth';

test.describe('session expiry', () => {
  test('proactive lock: dashboard blanks and login form appears when session timer fires', async ({
    page,
  }) => {
    // 1. Install fake clock before navigating so every setTimeout/Date.now
    //    call made by the SPA is under Playwright's control.
    const now = Date.now();
    await page.clock.install({ time: now });

    // 2. Mock bootstrap endpoints. auth/me returns an active elevation that
    //    lasts well beyond the test, but a session idle deadline of 5 s.
    const elevationExpiresAt = new Date(now + 3_600_000).toISOString(); // 1 hour
    const sessionIdleDeadline = new Date(now + 5_000).toISOString(); // 5 seconds
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
          session_idle_deadline: sessionIdleDeadline,
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
    //    auth.status = 'ready'. #scheduleSessionExpiry arms setTimeout(5 000).
    await page.goto('/admin/');

    // 4. Dashboard is visible: confirms auth.status === 'ready'.
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // 5. Advance the fake clock by 6 000 ms. page.clock.fastForward() fires all
    //    pending timers whose deadline falls within the advanced window — the
    //    5-second session timer fires here. No user interaction occurs.
    await page.clock.fastForward(6_000);

    // 6. Proactive transition: auth.status === 'unauthenticated' -> router.replace('/login').
    //    The login view renders (email + password form); dashboard is gone.
    await expect(page.getByRole('heading', { name: 'Dashboard' })).not.toBeVisible();
    // The login form must be visible (email field is the canonical sentinel).
    await expect(page.locator('input[type="email"]')).toBeVisible();

    // Save a screenshot for the issue comment. The image shows the login form
    // on screen with no dashboard content visible — the proactive session-expiry
    // transition fired without any user interaction (re #106).
    await page.screenshot({ path: '/tmp/herold-session-expiry-locked.png', fullPage: false });
  });
});
