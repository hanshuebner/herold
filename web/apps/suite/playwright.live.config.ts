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
 * that); it runs in its own CI job (`.forgejo/workflows/ci.yml`'s
 * `web-e2e-live`) and can also be invoked by hand:
 *
 *   SUITE_URL=http://localhost:PORT pnpm --filter @herold/suite \
 *     exec playwright test --config=playwright.live.config.ts
 *
 * On CI (`process.env.CI` set), an HTML report is written to
 * `playwright-report/` and a trace is kept for every failed test, so a red
 * run can be diagnosed from the uploaded artifact without reproducing
 * locally.
 */

const BASE_URL = process.env.SUITE_URL ?? 'http://localhost:5173';

export default defineConfig({
  testDir: './tests/e2e-live',
  fullyParallel: false,
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',

  use: {
    baseURL: BASE_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
