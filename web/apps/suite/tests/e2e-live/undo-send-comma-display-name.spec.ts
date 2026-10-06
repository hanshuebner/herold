/**
 * e2e-live (re #510): a recipient display name containing a comma must
 * survive the Undo re-open round trip. Pre-fix, `recipientToString` /
 * `addressToString` rendered the chip's name unquoted into the
 * compose.to string field; Undo's callback re-feeds that string through
 * `openWith` -> `tryCommit`, which treats the comma as a separator and
 * either drops the display name or splits it into a bare, domain-less
 * token. Sent through the external-relay identity, that bare token
 * reached the real wire as a malformed `RCPT TO` with no `@domain` --
 * exactly the production report (message 4181, principal
 * hans@netzhansa.com): `rcptTo: ["Surname","user@example.org"]`,
 * rejected by the relay with "recipient address must contain a domain".
 *
 * Requires the dev instance started with
 * HEROLD_DEV_EXTERNAL_SUBMISSION=1 (FAKESMTP_HTTP_ADDR set) so the
 * resend's envelope is observable on the real wire via the fake SMTP
 * sink's `/messages` status API, not just inferred from JMAP state.
 */

import { test, expect, type Page } from '@playwright/test';
import { login, clearMailbox } from './live-helpers';

const FAKESMTP_HTTP_ADDR = process.env.FAKESMTP_HTTP_ADDR;
const EXTERNAL_IDENTITY_EMAIL = 'alice-work@foreign.example';

test.skip(
  !FAKESMTP_HTTP_ADDR,
  'requires the dev instance started with HEROLD_DEV_EXTERNAL_SUBMISSION=1 (FAKESMTP_HTTP_ADDR not set)',
);

async function clickUndo(page: Page): Promise<void> {
  const toast = page.locator('.toast', { hasText: 'Message sent' });
  await expect(toast).toBeVisible({ timeout: 5_000 });
  await toast.getByRole('button', { name: 'Undo' }).click();
}

test.describe('Undo re-open preserves a comma display name through a resend (re #510)', () => {
  test.beforeEach(async ({ page, request }) => {
    await login(page);
    await clearMailbox(page, request);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  });

  test('resend after Undo submits the one correct address, not a split bare-name token', async ({
    page,
    request,
  }) => {
    test.setTimeout(45_000);

    const recipient = `undo-comma-${Date.now()}@remote.test`;
    const subject = `undo comma display name e2e ${Date.now()}`;
    const displayName = 'Surname, Firstname';

    await page.locator('button.compose').first().click();
    await page.getByTestId('from-picker-trigger').click();
    await page
      .getByTestId('from-picker-row')
      .filter({ hasText: EXTERNAL_IDENTITY_EMAIL })
      .click();

    // Type the recognized quoted-name angle-address form (REQ-MAIL-11a)
    // directly -- the same literal shape a user would type or paste.
    const recipientInput = page.locator('[placeholder="recipient@example.com"]').first();
    await recipientInput.fill(`"${displayName}" <${recipient}>`);
    const subjectInput = page.locator('label', { hasText: 'Subject' }).locator('input');
    await subjectInput.fill(subject); // blurs the recipient field, committing the chip
    await page.locator('.ProseMirror').fill('undo comma display name e2e body');

    const toChip = page.locator('.recipient-field .chip-label', { hasText: displayName });
    await expect(toChip).toBeVisible();
    await expect(page.locator('.recipient-field .chip')).toHaveCount(1);

    await page.getByTestId('compose-send').click();
    const composeDialog = page.locator('div.modal[role="dialog"]');
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    await clickUndo(page);
    await expect(composeDialog).toBeVisible({ timeout: 5_000 });

    // The re-opened composer's To chip must still carry the one correct
    // recipient -- not two chips (one bare "Surname", one the dropped-name
    // address), not zero.
    await expect(composeDialog.locator('.recipient-field .chip')).toHaveCount(1);
    await expect(
      composeDialog.locator('.recipient-field .chip-label', { hasText: displayName }),
    ).toBeVisible();

    const countBefore = (
      (await (await request.get(`http://${FAKESMTP_HTTP_ADDR}/count`)).json()) as { count: number }
    ).count;

    await page.getByTestId('compose-send').click();
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    // The resend must reach the sink exactly once, with the envelope
    // RCPT TO carrying only the one real address -- pre-fix this carried
    // an extra ["Surname", recipient] pair, and "Surname" alone (no
    // @domain) is exactly the malformed RCPT TO the relay rejected in
    // the production report.
    await expect
      .poll(
        async () => {
          const resp = await request.get(`http://${FAKESMTP_HTTP_ADDR}/messages`);
          const messages = (await resp.json()) as Array<{
            mail_from: string;
            rcpt_to: string[];
          }>;
          return messages.filter(
            (m) => m.mail_from === EXTERNAL_IDENTITY_EMAIL && m.rcpt_to.includes(recipient),
          ).length;
        },
        { timeout: 10_000, message: 'the resend must reach the external relay sink exactly once' },
      )
      .toBe(1);

    const resp = await request.get(`http://${FAKESMTP_HTTP_ADDR}/messages`);
    const messages = (await resp.json()) as Array<{ mail_from: string; rcpt_to: string[] }>;
    const delivered = messages.find(
      (m) => m.mail_from === EXTERNAL_IDENTITY_EMAIL && m.rcpt_to.includes(recipient),
    )!;
    expect(
      delivered.rcpt_to,
      'the envelope RCPT TO must carry only the real address, never a split bare-name token',
    ).toEqual([recipient]);

    await expect
      .poll(
        async () =>
          ((await (await request.get(`http://${FAKESMTP_HTTP_ADDR}/count`)).json()) as {
            count: number;
          }).count,
        { timeout: 10_000 },
      )
      .toBe(countBefore + 1);
  });
});
