/**
 * shift-click-selection-contacts.spec.ts (re #202)
 *
 * The contacts-list counterpart to shift-click-selection.spec.ts. The
 * ticket explicitly covers "all lists" and the shared `handleRowCheckboxClick`
 * / `reconcileSelection` primitives are used by both `MailView.svelte` and
 * `ContactsListView.svelte`, but the two views wire them into different
 * DOM structures, so this asserts the contacts list independently rather
 * than assuming parity from the mail-list spec alone.
 *
 * Requires a live herold instance (scripts/dev-instance.sh):
 *   SUITE_URL=http://localhost:PORT pnpm --filter @herold/suite exec \
 *     playwright test --config=playwright.live.config.ts \
 *     tests/e2e-live/shift-click-selection-contacts.spec.ts
 */

import { test, expect, type Page } from '@playwright/test';
import { login, clearContacts, seedContacts, bulkCountText } from './live-helpers';

function rowCheckbox(page: Page, name: string) {
  return page.locator('.contact-list li', { hasText: name }).locator('.row-check');
}

test.describe('shift-click range selection (contacts list)', () => {
  test('plain click + real Shift-modified click checks the inclusive range and the count matches', async ({
    page,
    request,
  }) => {
    await login(page);
    await page.goto('/#/contacts');
    await clearContacts(page, request);

    const names = ['Contact Alpha', 'Contact Bravo', 'Contact Charlie', 'Contact Delta', 'Contact Echo'];
    await seedContacts(page, request, names);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.contact-list li')).toHaveCount(names.length, { timeout: 15_000 });

    // Contacts render alphabetically by displayName -- Alpha is the top
    // row, Echo the bottom.
    await rowCheckbox(page, 'Contact Alpha').click();

    // Real Shift-modified click -- CDP Input.dispatchMouseEvent with the
    // Shift bit set, a trusted event exercising the browser's native
    // checkbox activation pipeline (re #202: a JS-dispatched synthetic
    // MouseEvent bypasses exactly the reactivity path under test).
    await rowCheckbox(page, 'Contact Echo').click({ modifiers: ['Shift'] });

    for (const name of names) {
      await expect(rowCheckbox(page, name), `row "${name}"`).toBeChecked();
    }

    await expect(bulkCountText(page)).toHaveText(`${names.length} selected`);
    await expect(page.locator('.contact-list .row-check:checked')).toHaveCount(names.length);
  });

  test('rows outside the shift-click range stay unchecked', async ({ page, request }) => {
    await login(page);
    await page.goto('/#/contacts');
    await clearContacts(page, request);

    // Alphabetically ordered names (the contacts list default-sorts by
    // displayName) so top-to-bottom render order is unambiguous.
    const names = ['Range Golf', 'Range Hotel', 'Range India', 'Range Juliett', 'Range Kilo', 'Range Lima'];
    await seedContacts(page, request, names);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.contact-list li')).toHaveCount(names.length, { timeout: 15_000 });

    // Anchor "Range Kilo" (second from bottom), shift-click "Range Hotel"
    // (second from top) -- range excludes "Range Lima" and "Range Golf".
    await rowCheckbox(page, 'Range Kilo').click();
    await rowCheckbox(page, 'Range Hotel').click({ modifiers: ['Shift'] });

    for (const name of ['Range Hotel', 'Range India', 'Range Juliett', 'Range Kilo']) {
      await expect(rowCheckbox(page, name), `row "${name}"`).toBeChecked();
    }
    for (const name of ['Range Lima', 'Range Golf']) {
      await expect(rowCheckbox(page, name), `row "${name}"`).not.toBeChecked();
    }

    await expect(bulkCountText(page)).toHaveText('4 selected');
    await expect(page.locator('.contact-list .row-check:checked')).toHaveCount(4);
  });
});
