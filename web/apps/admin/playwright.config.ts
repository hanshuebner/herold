import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright configuration for the admin SPA e2e suite.
 *
 * The suite targets the Vite dev server started by playwright's webServer
 * config below. The dev server proxies /api/v1/* to the stub server port
 * supplied via HEROLD_API_BASE env var (set by each spec's beforeAll).
 *
 * Default project is chromium only (fast CI inner lane).
 * Firefox and WebKit run on demand: --project=firefox / --project=webkit.
 * All three run in the web-e2e CI job via projects override.
 *
 * Browser support floor per docs/design/web/requirements/13-nonfunctional.md:
 * latest two stable versions of Chrome, Firefox, Safari, Edge.
 */

// The stub server port is chosen at runtime by the fixture. The Vite dev
// server reads HEROLD_API_BASE to point its proxy at the stub. Because
// Playwright's webServer is started once per run (before tests), and the
// stub port is not known at config-file evaluation time, we use a fixed
// well-known port for the primary stub server. Individual specs that need
// isolation start their own server on a different port; the webServer proxy
// variable HEROLD_API_BASE is set to the primary port for the dashboard/
// auth specs that share the dev server's proxy config.
//
// The Vite dev-server port is 5174 (admin's strictPort in vite.config.ts).
const BASE_URL = 'http://localhost:5174';

export default defineConfig({
  testDir: './tests/e2e',
  // Each test file gets a fresh context; never share state between specs.
  fullyParallel: false,
  // Fail fast on CI; devs can set --retries locally.
  retries: process.env.CI ? 1 : 0,
  // Single worker per project; the stub server is per-file.
  workers: 1,
  reporter: process.env.CI ? 'github' : 'list',

  use: {
    baseURL: BASE_URL,
    // Use /admin/ as the base path since Vite's dev server serves it there.
    // The browser starts at /admin/ and the SPA's router handles sub-routes.
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

  // Start Vite dev server before running tests. The server is stopped after
  // all tests complete. HEROLD_API_BASE must point at a running stub server;
  // specs that need a specific stub port set the env var and restart Vite,
  // or use Playwright's route interception (page.route) as the simpler path
  // for intercepting at the browser network layer without a real proxy.
  //
  // NOTE: Because we use page.route() in the specs for request interception
  // (avoiding the complexity of per-test Vite restarts), the webServer block
  // here starts the dev server in a mode where HEROLD_URL points at a
  // placeholder that will never receive real traffic. The page.route() mocks
  // intercept all /api/v1/* traffic before it reaches the proxy.
  webServer: {
    command: 'HEROLD_URL=http://127.0.0.1:1 pnpm run dev',
    url: BASE_URL + '/admin/',
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
