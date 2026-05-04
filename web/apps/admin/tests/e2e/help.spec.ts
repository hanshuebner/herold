/**
 * help.spec.ts
 *
 * Covers:
 *   - Navigate to /admin/ and click Help in the nav rail -> chapter list visible
 *   - Deep-link to #/help/install via URL -> correct heading visible
 *   - Bundle load error shows a friendly error message
 *   - TOC navigation updates the displayed chapter
 *   - Keyboard shortcut g h navigates to /help
 */

import { test, expect } from '@playwright/test';

// ---------------------------------------------------------------------------
// Fixture bundle
//
// Constructed as plain objects matching the Markdoc Tag shape so that the
// Manual component renders them without requiring @markdoc/markdoc as a dep
// of the admin SPA or this test file.
//
// The $$mdtype: 'Tag' discriminator is what isTag() in
// @herold/manual/src/markdoc/render.ts checks.
// ---------------------------------------------------------------------------

type TagNode = {
  $$mdtype: 'Tag';
  name: string;
  attributes: Record<string, unknown>;
  children: (TagNode | string | null)[];
};

function tag(name: string, children: (TagNode | string | null)[] = [], attributes: Record<string, unknown> = {}): TagNode {
  return { $$mdtype: 'Tag', name, attributes, children };
}

function makeChapter(slug: string, title: string, body: string) {
  return {
    slug,
    title,
    source: `admin/${slug}.mdoc`,
    ast: tag('document', [tag('h1', [title]), tag('p', [body])]),
    outline: [] as Array<{ id: string; level: 2 | 3; text: string }>,
  };
}

const FIXTURE_BUNDLE = {
  audience: 'admin' as const,
  home: 'index',
  chapters: [
    makeChapter('index', 'Welcome', 'Welcome to the admin manual.'),
    makeChapter('install', 'Installation', 'Linux or macOS.'),
    makeChapter('operate', 'Operating Herold', 'Run herold.'),
  ],
};

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

/**
 * Install auth routes so the SPA considers the session authenticated and
 * routes to /dashboard on startup.
 */
function installAuthRoutes(page: import('@playwright/test').Page): void {
  void page.route('/api/v1/server/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ principal_id: '1', email: 'admin@example.com', scopes: ['admin'] }),
    }),
  );
}

/**
 * Install the dashboard data routes expected by DashboardView on mount.
 */
function installDashboardRoutes(page: import('@playwright/test').Page): void {
  void page.route('/api/v1/queue/stats', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ queued: 0 }) }),
  );
  void page.route('/api/v1/audit*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
  );
  void page.route('/api/v1/domains*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) }),
  );
}

/**
 * Intercept the admin.json bundle fetch and return the fixture bundle.
 */
function installBundleRoute(page: import('@playwright/test').Page): void {
  void page.route('/admin/help/bundle.json', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(FIXTURE_BUNDLE),
    }),
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('help', () => {
  test('clicking Help nav item shows the manual chapter list', async ({ page }) => {
    installAuthRoutes(page);
    installDashboardRoutes(page);
    installBundleRoute(page);

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // Click Help in the navigation rail.
    await page.getByRole('button', { name: 'Help' }).click();

    // The manual should load and show chapter titles in the TOC.
    await expect(page.getByRole('button', { name: 'Welcome' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Installation' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Operating Herold' })).toBeVisible();
  });

  test('deep-link to #/help/install renders the installation chapter heading', async ({ page }) => {
    installAuthRoutes(page);
    installDashboardRoutes(page);
    installBundleRoute(page);

    // Navigate directly to the install chapter via hash deep-link.
    await page.goto('/admin/#/help/install');

    // The Installation chapter heading must be visible.
    await expect(page.getByRole('heading', { name: 'Installation' })).toBeVisible();
  });

  test('TOC navigation switches chapters', async ({ page }) => {
    installAuthRoutes(page);
    installDashboardRoutes(page);
    installBundleRoute(page);

    await page.goto('/admin/#/help');

    // Wait for Welcome chapter (home).
    await expect(page.getByRole('button', { name: 'Welcome' })).toBeVisible();

    // Click "Installation" in the TOC.
    await page.getByRole('button', { name: 'Installation' }).click();

    // The Installation chapter should now be rendered in the main area.
    await expect(page.locator('[data-slug="install"]')).toBeVisible();
  });

  test('shows error message when bundle fails to load', async ({ page }) => {
    installAuthRoutes(page);
    installDashboardRoutes(page);

    // Bundle endpoint returns 404.
    void page.route('/admin/help/bundle.json', (route) =>
      route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not found"}' }),
    );

    await page.goto('/admin/#/help');

    // The HelpView shows an inline error (not a full crash).
    await expect(page.getByRole('alert')).toBeVisible();
    await expect(page.getByRole('alert')).toContainText('Manual unavailable');
    await expect(page.getByRole('alert')).toContainText('HTTP 404');

    // The rest of the SPA remains accessible: nav rail is still present.
    await expect(page.getByRole('button', { name: 'Dashboard' })).toBeVisible();
  });

  test('keyboard shortcut g h navigates to /help from any view', async ({ page }) => {
    installAuthRoutes(page);
    installDashboardRoutes(page);
    installBundleRoute(page);

    await page.goto('/admin/');
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // Trigger the g h shortcut. The engine requires that these are pressed in
    // sequence with the document body focused (not an input field).
    await page.keyboard.press('g');
    await page.keyboard.press('h');

    // Manual should load and display the home chapter.
    await expect(page.getByRole('button', { name: 'Welcome' })).toBeVisible();
  });

  test('help route remains accessible when navigating away and back', async ({ page }) => {
    installAuthRoutes(page);
    installDashboardRoutes(page);
    installBundleRoute(page);

    await page.goto('/admin/#/help');
    await expect(page.getByRole('button', { name: 'Welcome' })).toBeVisible();

    // Navigate away to Dashboard.
    await page.getByRole('button', { name: 'Dashboard' }).click();
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

    // Navigate back to Help.
    await page.getByRole('button', { name: 'Help' }).click();
    await expect(page.getByRole('button', { name: 'Welcome' })).toBeVisible();
  });
});
