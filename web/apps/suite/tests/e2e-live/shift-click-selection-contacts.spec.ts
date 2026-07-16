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
import { login, clearContacts, seedContacts, bulkCountText, jmapSession, jmapCall } from './live-helpers';

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

  test('shift-click applies the direction of the last plain click: select-range then deselect-range (re #202 handback, comment 3131)', async ({
    page,
    request,
  }) => {
    await login(page);
    await page.goto('/#/contacts');
    await clearContacts(page, request);

    // Contacts sort alphabetically by displayName, so the array order
    // below is the top-to-bottom render order.
    const names = [
      'Direction Alpha',
      'Direction Bravo',
      'Direction Charlie',
      'Direction Delta',
      'Direction Echo',
    ];
    await seedContacts(page, request, names);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.contact-list li')).toHaveCount(names.length, { timeout: 15_000 });

    const [row1, row2, , , rowN] = names;

    // Plain click row 1 -- a selecting click -- sets the anchor with a
    // select operation.
    await rowCheckbox(page, row1!).click();
    await expect(rowCheckbox(page, row1!)).toBeChecked();

    // Shift-click row N: the anchor's last operation was select, so the
    // whole anchor..target range gets selected.
    await rowCheckbox(page, rowN!).click({ modifiers: ['Shift'] });
    for (const name of names) {
      await expect(rowCheckbox(page, name), `row "${name}" after select-range`).toBeChecked();
    }
    await expect(bulkCountText(page)).toHaveText(`${names.length} selected`);
    await expect(page.locator('.contact-list .row-check:checked')).toHaveCount(names.length);

    // Plain click row 2, which is currently checked -- a deselecting
    // click -- moves the anchor to row 2 with a deselect operation.
    await rowCheckbox(page, row2!).click();
    await expect(rowCheckbox(page, row2!)).not.toBeChecked();

    // Shift-click row N again: the anchor's last operation was now
    // deselect, so the whole row2..rowN range gets deselected, leaving
    // row 1 (outside that range) checked.
    await rowCheckbox(page, rowN!).click({ modifiers: ['Shift'] });

    await expect(
      rowCheckbox(page, row1!),
      'row 1 (outside the deselect range) stays checked',
    ).toBeChecked();
    for (const name of names.slice(1)) {
      await expect(rowCheckbox(page, name), `row "${name}" after deselect-range`).not.toBeChecked();
    }
    await expect(bulkCountText(page)).toHaveText('1 selected');
    await expect(page.locator('.contact-list .row-check:checked')).toHaveCount(1);
  });

  test('a pruned anchor stops applying its stale snapshot after the anchor row itself is deleted (re #202 follow-up)', async ({
    page,
    request,
  }) => {
    await login(page);
    await page.goto('/#/contacts');
    await clearContacts(page, request);

    const names = ['Prune Alpha', 'Prune Bravo', 'Prune Charlie', 'Prune Delta', 'Prune Echo'];
    await seedContacts(page, request, names);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.contact-list li')).toHaveCount(names.length, { timeout: 15_000 });
    const [row1, row2, row3] = names;

    // Two sequential plain selects: anchor=row2, base snapshot={row1,row2}.
    await rowCheckbox(page, row1!).click();
    await rowCheckbox(page, row2!).click();
    await expect(bulkCountText(page)).toHaveText('2 selected');

    // Bulk-delete the current selection via the SPA's own Delete button --
    // an ordinary user action that removes the anchor (row2) itself, not
    // just a bystander id in its snapshot. The fix under test has to drop
    // the anchor's operation and base-selection snapshot outright when the
    // anchor no longer renders, since neither applies to anything on
    // screen any more.
    await page.locator('.bulk-toolbar button.icon-btn.danger').click();
    await page.locator('[data-testid="confirm-dialog-confirm"]').click();
    await expect(page.locator('.contact-list li')).toHaveCount(names.length - 2, {
      timeout: 15_000,
    });
    await expect(bulkCountText(page)).toHaveCount(0);

    // An ordinary shift-click now has no valid anchor to extend from -- it
    // must fall back to selecting only the clicked row, not resurrect row1
    // or row2 from the anchor's now-stale snapshot.
    await rowCheckbox(page, row3!).click({ modifiers: ['Shift'] });

    await expect(rowCheckbox(page, row3!)).toBeChecked();
    await expect(bulkCountText(page)).toHaveText('1 selected');
    await expect(page.locator('.contact-list .row-check:checked')).toHaveCount(1);
  });
});
