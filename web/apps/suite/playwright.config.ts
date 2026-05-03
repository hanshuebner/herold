import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright configuration for the Suite SPA e2e suite.
 *
 * The suite targets the Vite dev server. Request interception is done via
 * page.route() at the browser network layer -- no stub server needed, no
 * Vite proxy restarts between tests.
 *
 * Default project is chromium only (fast CI inner lane).
 * Firefox and WebKit run on demand: --project=firefox / --project=webkit.
 *
 * Browser support floor per docs/design/web/requirements/13-nonfunctional.md:
 * latest two stable versions of Chrome, Firefox, Safari, Edge.
 */

const BASE_URL = 'http://localhost:5173';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? 'github' : 'list',

  use: {
    baseURL: BASE_URL,
    trace: 'on-first-retry',
    screenshot: process.env.CI ? 'off' : 'only-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
    },
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] },
    },
  ],

  // Start Vite dev server before running tests. page.route() mocks intercept
  // all API traffic before it reaches the proxy, so HEROLD_URL points at a
  // placeholder that never receives real traffic.
  webServer: {
    command: 'HEROLD_URL=http://127.0.0.1:1 pnpm run dev',
    url: BASE_URL,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
