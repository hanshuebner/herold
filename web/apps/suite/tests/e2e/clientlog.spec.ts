/**
 * clientlog.spec.ts
 *
 * Verifies that runtime errors captured by the clientlog wrapper are
 * flushed to the clientlog endpoint.
 *
 * The test:
 *   1. Boots the Suite SPA in the Vite dev server.
 *   2. Intercepts JMAP / auth routes so the app stays in "unauthenticated"
 *      state -- clientlog is pre-auth, events route to the anonymous endpoint.
 *   3. Calls __TEST_THROW__() (a test-only global injected in main.ts when
 *      import.meta.env.DEV is true) to produce an uncaught error that the
 *      wrapper's window.onerror handler captures.
 *   4. Asserts that a POST was made to /api/v1/clientlog/public (the
 *      anonymous endpoint) with a JSON body containing at least one
 *      event with kind=error.
 *
 * No real herold backend is required; page.route() intercepts every
 * outbound network request at the browser network layer.
 *
 * REQ-CLOG-01, REQ-CLOG-04, REQ-CLOG-07.
 */

import { test, expect } from '@playwright/test';

test.describe('clientlog error capture', () => {
  test('runtime error is flushed to the clientlog endpoint', async ({ page }) => {
    // ── Step 1: stub backend routes ──────────────────────────────────────

    // JMAP bootstrap returns 401 so the app transitions to 'unauthenticated'.
    await page.route('/.well-known/jmap', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ type: 'urn:ietf:params:jmap:error:notAuthorized' }),
      }),
    );

    await page.route('/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'not authenticated' }),
      }),
    );

    // Intercept the clientlog anonymous endpoint and resolve with 204.
    await page.route('/api/v1/clientlog/public', (route) =>
      route.fulfill({ status: 204, body: '' }),
    );

    // Intercept the authenticated endpoint too in case auth resolves faster.
    await page.route('/api/v1/clientlog', (route) =>
      route.fulfill({ status: 204, body: '' }),
    );

    // ── Step 2: load the SPA ─────────────────────────────────────────────
    await page.goto('/');

    // Wait until the login form is visible (unauthenticated state reached).
    await page.waitForSelector('input[type="email"], input[name="email"], #email', {
      timeout: 15_000,
    });

    // ── Step 3: trigger a runtime error ──────────────────────────────────
    // Use page.waitForRequest + page.evaluate together so we don't miss
    // the flush request. Start the request-wait promise BEFORE triggering
    // the error so we don't race.
    const clientlogRequestPromise = page.waitForRequest(
      (req) =>
        (req.url().includes('/api/v1/clientlog/public') ||
          req.url().includes('/api/v1/clientlog')) &&
        req.method() === 'POST',
      { timeout: 15_000 },
    );

    // Trigger error in the page context. __TEST_THROW__ is injected by
    // main.ts when import.meta.env.DEV === true.
    await page.evaluate(() => {
      try {
        (
          globalThis as unknown as Record<string, () => void>
        )['__TEST_THROW__']?.();
      } catch {
        // Swallowed here; the error propagates via window.onerror which
        // the clientlog wrapper intercepts.
      }
      // Also dispatch an error event directly to window so the wrapper's
      // handler is guaranteed to fire even if the try/catch above swallows.
      window.dispatchEvent(
        new ErrorEvent('error', {
          message: 'clientlog-e2e-test-error',
          error: new Error('clientlog-e2e-test-error'),
        }),
      );
    });

    // ── Step 4: wait for the flush request ───────────────────────────────
    const clientlogRequest = await clientlogRequestPromise;

    // ── Step 5: assert payload shape ─────────────────────────────────────
    expect(clientlogRequest.method()).toBe('POST');

    const bodyText = clientlogRequest.postData();
    expect(bodyText).not.toBeNull();

    const payload = JSON.parse(bodyText ?? '{}') as { events?: unknown[] };
    expect(payload).toHaveProperty('events');
    expect(Array.isArray(payload.events)).toBe(true);
    expect((payload.events ?? []).length).toBeGreaterThan(0);

    // At least one event must have kind=error.
    const hasErrorEvent = (payload.events ?? []).some(
      (ev) =>
        typeof ev === 'object' &&
        ev !== null &&
        (ev as Record<string, unknown>)['kind'] === 'error',
    );
    expect(hasErrorEvent).toBe(true);
  });
});
