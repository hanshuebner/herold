/**
 * imap-imports.spec.ts (re #138)
 *
 * Covers:
 *   - IMAP-Import-Diagnose view renders worker cards with status fields
 *   - Connected/disconnected badge renders correctly
 *   - Phase badge is shown for each worker
 *   - Debug-log toggle sends PATCH request with toggled value
 *   - Empty state renders when no workers are active
 *   - "Ereignisse filtern" button navigates to the events view
 */

import { test, expect } from '@playwright/test';
import { installAdminSession } from './fixtures/auth';

const WORKER_CONNECTED: Record<string, unknown> = {
  account_id: 'acc-001',
  principal_id: '2',
  account_name: 'Gmail (alice)',
  host: 'imap.gmail.com',
  phase: 'idle',
  current_folder: '',
  conn_mode: 'dual',
  watch_mode: 'idle',
  connected: true,
  phase_since: new Date(Date.now() - 300_000).toISOString(),
  last_sync_at: new Date(Date.now() - 120_000).toISOString(),
  consecutive_failures: 0,
  messages_fetched: 257,
  flags_propagated: 12,
  last_error: '',
  debug_log: false,
};

const WORKER_ERRORED: Record<string, unknown> = {
  account_id: 'acc-002',
  principal_id: '3',
  account_name: 'Outlook (bob)',
  host: 'outlook.office365.com',
  phase: 'backoff',
  current_folder: '',
  conn_mode: '',
  watch_mode: '',
  connected: false,
  phase_since: new Date(Date.now() - 60_000).toISOString(),
  last_sync_at: null,
  consecutive_failures: 3,
  messages_fetched: 0,
  flags_propagated: 0,
  last_error: 'authentication failed: invalid credentials',
  debug_log: true,
};

test.describe('imap-imports', () => {
  test.beforeEach(async ({ page }) => {
    installAdminSession(page);
  });

  test('renders worker cards with status fields', async ({ page }) => {
    await page.route('/api/v1/imap-imports/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [WORKER_CONNECTED, WORKER_ERRORED] }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'IMAP import' }).click();
    await expect(page.getByRole('heading', { name: 'IMAP import diagnostics' })).toBeVisible();

    // First worker (connected)
    await expect(page.getByTestId('worker-card-acc-001')).toBeVisible();
    await expect(page.getByTestId('worker-card-acc-001').getByTestId('worker-account-name')).toHaveText('Gmail (alice)');
    await expect(page.getByTestId('worker-card-acc-001').getByTestId('worker-connected')).toHaveText('Connected');
    await expect(page.getByTestId('worker-card-acc-001').getByTestId('worker-phase')).toHaveText('idle');
    await expect(page.getByTestId('worker-card-acc-001').getByTestId('worker-messages-fetched')).toHaveText('257');

    // Second worker (errored)
    await expect(page.getByTestId('worker-card-acc-002')).toBeVisible();
    await expect(page.getByTestId('worker-card-acc-002').getByTestId('worker-connected')).toHaveText('Disconnected');
    await expect(page.getByTestId('worker-card-acc-002').getByTestId('worker-phase')).toHaveText('backoff');
    await expect(page.getByTestId('worker-card-acc-002').getByTestId('worker-last-error')).toContainText(
      'authentication failed: invalid credentials',
    );
  });

  test('debug-log toggle sends PATCH with toggled value', async ({ page }) => {
    await page.route('/api/v1/imap-imports/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [WORKER_CONNECTED] }),
      }),
    );

    let patchBody: unknown = null;
    await page.route('/api/v1/principals/2/imap-imports/acc-001', (route) => {
      patchBody = route.request().postDataJSON() as unknown;
      void route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...WORKER_CONNECTED, debug_log: true }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'IMAP import' }).click();
    await expect(page.getByTestId('worker-card-acc-001')).toBeVisible();

    // Toggle is initially unchecked (debug_log: false)
    const toggle = page.getByTestId('worker-debug-toggle-acc-001');
    await expect(toggle).not.toBeChecked();

    await toggle.click();

    // Verify PATCH was sent with debug_log: true
    await expect.poll(() => patchBody).toMatchObject({ debug_log: true });
  });

  test('debug-log toggle reflects current state (pre-enabled)', async ({ page }) => {
    await page.route('/api/v1/imap-imports/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [WORKER_ERRORED] }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'IMAP import' }).click();
    await expect(page.getByTestId('worker-card-acc-002')).toBeVisible();

    // debug_log is true for WORKER_ERRORED — toggle should be checked
    const toggle = page.getByTestId('worker-debug-toggle-acc-002');
    await expect(toggle).toBeChecked();
  });

  test('empty state renders when no workers are active', async ({ page }) => {
    await page.route('/api/v1/imap-imports/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [] }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'IMAP import' }).click();
    await expect(page.getByTestId('imap-imports-empty')).toBeVisible();
  });

  test('"Filter events" button navigates to events view', async ({ page }) => {
    await page.route('/api/v1/imap-imports/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [WORKER_CONNECTED] }),
      }),
    );
    // Stub the events endpoint so the view can render
    await page.route('/api/v1/admin/system-events*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'IMAP import' }).click();
    await expect(page.getByTestId('worker-card-acc-001')).toBeVisible();

    await page.getByTestId('worker-events-btn-acc-001').click();

    await expect(page.getByRole('heading', { name: 'System events' })).toBeVisible();
  });
});
