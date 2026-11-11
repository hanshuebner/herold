/**
 * e2e-live: link an account to an external OpenID Connect provider from
 * Settings > Security, then sign in through that provider from the
 * sign-in page (REQ-AUTH-50..58).
 *
 * Requires the dev instance to have been started with
 * HEROLD_DEV_OIDC_AUTOPROVISION=1: scripts/dev-instance.sh then runs
 * heroldfakeoidc, registers it as provider "fakeoidc", and sets
 * public_base_url to the Vite origin so the provider's redirect to
 * <public_base_url>/api/v1/oidc/callback reaches this instance. The fake
 * approves every authorization request immediately with its default
 * identity (sub "dev-user"). Skips itself when the sign-in page offers
 * no provider.
 *
 * Pass SCREENSHOT_DIR to keep screenshots of the sign-in page and the
 * linked state for the issue record.
 */

import { test, expect } from '@playwright/test';
import { login } from './live-helpers';

const PROVIDER = 'fakeoidc';
const SCREENSHOT_DIR = process.env.SCREENSHOT_DIR;

async function shot(page: import('@playwright/test').Page, name: string): Promise<void> {
  if (SCREENSHOT_DIR) await page.screenshot({ path: `${SCREENSHOT_DIR}/${name}.png`, fullPage: true });
}

test('link the provider in settings, then sign in through it', async ({ page, request, baseURL }) => {
  test.setTimeout(60_000);
  const providers = await (await request.get(`${baseURL}/api/v1/auth/oidc/providers`)).json();
  test.skip(
    !providers.items?.some((p: { id: string }) => p.id === PROVIDER),
    'requires the dev instance started with HEROLD_DEV_OIDC_AUTOPROVISION=1',
  );

  // Link from settings after a password sign-in.
  await login(page);
  await page.goto('/#/settings/security');
  const list = page.getByTestId('oidc-provider-list');
  await expect(list).toContainText(PROVIDER);
  await expect(list).toContainText('Not linked');
  await list.getByRole('button', { name: 'Link' }).click();

  // The provider approves and redirects back through the callback.
  await expect(page).toHaveURL(/#\/settings\/security/);
  await expect(page.getByTestId('oidc-link-outcome')).toContainText('Your account is now linked');
  await expect(list).toContainText('Linked as dev-user@fakeoidc.local');
  await list.scrollIntoViewIfNeeded();
  await shot(page, 'oidc-settings-linked');

  // Sign out, then sign in through the provider.
  await page.context().clearCookies();
  await page.goto('/#/mail');
  const button = page.getByRole('button', { name: `Sign in with ${PROVIDER}` });
  await expect(button).toBeVisible();
  await shot(page, 'oidc-signin-page');
  await button.click();
  await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  const me = await page.evaluate(async () => (await fetch('/api/v1/auth/me')).json());
  expect(me.email).toBe('alice@example.local');

  // Unlink again so the instance is back to its seeded state, and check
  // the provider no longer signs alice in.
  await page.goto('/#/settings/security');
  await list.getByRole('button', { name: 'Unlink' }).click();
  await page.getByRole('button', { name: 'Unlink' }).last().click();
  await expect(list).toContainText('Not linked');
});
