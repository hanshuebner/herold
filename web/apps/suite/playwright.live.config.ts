import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright configuration for e2e specs that require a real herold
 * backend instead of page.route() mocks (re #202).
 *
 * These specs exercise gestures -- a genuine Shift-modified click -- that
 * only a real, CDP-trusted browser event can produce; a JS-dispatched
 * `new MouseEvent('click', { shiftKey: true })` bypasses the browser's
 * native checkbox activation behaviour, which is exactly the reactivity
 * path under test. `page.click(selector, { modifiers: ['Shift'] })`
 * dispatches via CDP Input.dispatchMouseEvent with the modifier bit set,
 * indistinguishable to the page from real hardware input.
 *
 * There is no webServer entry here: the caller is expected to already
 * have a live herold instance running (scripts/dev-instance.sh) and to
 * pass its Suite URL via SUITE_URL. This config is not part of the
 * `test:e2e` / `test:e2e:all` CI lane (see playwright.config.ts for
 * that); it is invoked explicitly:
 *
 *   SUITE_URL=http://localhost:PORT pnpm --filter @herold/suite \
 *     exec playwright test --config=playwright.live.config.ts
 */

const BASE_URL = process.env.SUITE_URL ?? 'http://localhost:5173';

export default defineConfig({
  testDir: './tests/e2e-live',
  fullyParallel: false,
  retries: 0,
  workers: 1,
  reporter: 'list',

  use: {
    baseURL: BASE_URL,
    trace: 'off',
    screenshot: 'only-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
