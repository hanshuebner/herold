/**
 * e2e-live (re #507): a send cancelled with Undo must not leave a copy in
 * Sent. EmailSubmission/create applies the Drafts -> Sent move and clears
 * $draft as an implicit onSuccessUpdateEmail patch at submission-creation
 * time (REQ-MAIL-14, REQ-OPT-11), before the undo window elapses, so the
 * pre-fix undo callback (which only destroyed the EmailSubmission) left
 * the Email orphaned in Sent -- a later re-send then produced a second,
 * separate Sent copy.
 *
 * Covers both the local-identity path (the production report, principal
 * hans@netzhansa.com sending under the default identity) and the
 * external-relay identity path via the in-tree fake SMTP sink (the
 * Acceptance's explicit ask) -- selecting alice-work@foreign.example as
 * the compose's From through the FromPicker needs no sub-account
 * separation (unlike sub-account-external-submission.spec.ts's undo test,
 * which covers the separated-identity variant of the same mechanism).
 */

import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { login, jmapSession, jmapCall, clearMailbox } from './live-helpers';

const FAKESMTP_HTTP_ADDR = process.env.FAKESMTP_HTTP_ADDR;
const EXTERNAL_IDENTITY_EMAIL = 'alice-work@foreign.example';

/** Resolve a role-mailbox's id via `Mailbox/query`. */
async function mailboxIdByRole(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  role: string,
): Promise<string> {
  const body = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Mailbox/query', { accountId: mailAccountId, filter: { role } }, 'q']],
  );
  const ids = (body.methodResponses as [string, { ids: string[] }, string][])[0]![1].ids;
  if (ids.length === 0) throw new Error(`no mailbox with role "${role}"`);
  return ids[0]!;
}

/** Email ids currently filed under `mailboxId` carrying `subject`. */
async function emailIdsInMailboxBySubject(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  mailboxId: string,
  subject: string,
): Promise<string[]> {
  const body = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/query', { accountId: mailAccountId, filter: { inMailbox: mailboxId, subject } }, 'q']],
  );
  return (body.methodResponses as [string, { ids: string[] }, string][])[0]![1].ids;
}

async function fillAndSend(page: Page, recipient: string, subject: string, body: string): Promise<void> {
  await page.locator('button.compose').first().click();
  await page.locator('[placeholder="recipient@example.com"]').first().fill(recipient);
  const subjectInput = page.locator('label', { hasText: 'Subject' }).locator('input');
  await subjectInput.fill(subject);
  await page.locator('.ProseMirror').fill(body);
  await expect(
    page.locator('.recipient-field .chip-label', { hasText: recipient }),
  ).toBeVisible();
  await page.getByTestId('compose-send').click();
}

async function clickUndo(page: Page): Promise<void> {
  const toast = page.locator('.toast', { hasText: 'Message sent' });
  await expect(toast).toBeVisible({ timeout: 5_000 });
  await toast.getByRole('button', { name: 'Undo' }).click();
}

test.describe('undo-cancelled send leaves no copy in Sent (re #507)', () => {
  test.beforeEach(async ({ page, request }) => {
    await login(page);
    await clearMailbox(page, request);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  });

  test('local identity: Undo moves the Email back to Drafts; a re-send yields exactly one Sent copy', async ({
    page,
    request,
  }) => {
    test.setTimeout(45_000);
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const sentId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'sent');
    const draftsId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'drafts');

    const recipient = `undo-local-${Date.now()}@remote.test`;
    const subject = `undo send local e2e ${Date.now()}`;
    await fillAndSend(page, recipient, subject, 'undo send local e2e body');

    const composeDialog = page.locator('div.modal[role="dialog"]');
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    await clickUndo(page);
    await expect(composeDialog).toBeVisible({ timeout: 5_000 });
    await expect(
      composeDialog.locator('.recipient-field .chip-label', { hasText: recipient }),
    ).toBeVisible();

    // Sent holds no copy for this subject; Drafts holds exactly one.
    await expect
      .poll(
        async () =>
          (await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, sentId, subject))
            .length,
        { timeout: 10_000, message: 'the cancelled send must not leave a copy in Sent' },
      )
      .toBe(0);
    await expect
      .poll(
        async () =>
          (
            await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, draftsId, subject)
          ).length,
        { timeout: 10_000, message: 'the cancelled send must restore the Email to Drafts' },
      )
      .toBe(1);

    // Re-send from the re-opened composer.
    await page.getByTestId('compose-send').click();
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    // Exactly one Sent copy -- not the cancelled copy alongside a fresh one.
    await expect
      .poll(
        async () =>
          (await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, sentId, subject))
            .length,
        { timeout: 10_000, message: 'the re-send must produce exactly one Sent copy' },
      )
      .toBe(1);
    await expect
      .poll(
        async () =>
          (
            await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, draftsId, subject)
          ).length,
        { timeout: 10_000, message: 'the draft row must not remain in Drafts after the re-send' },
      )
      .toBe(0);
  });

  test('external-relay identity: Undo leaves no leak and no Sent copy; a re-send yields exactly one Sent copy', async ({
    page,
    request,
  }) => {
    test.skip(
      !FAKESMTP_HTTP_ADDR,
      'requires the dev instance started with HEROLD_DEV_EXTERNAL_SUBMISSION=1 (FAKESMTP_HTTP_ADDR not set)',
    );
    test.setTimeout(45_000);
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const sentId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'sent');
    const draftsId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'drafts');

    const countBefore = (
      (await (await request.get(`http://${FAKESMTP_HTTP_ADDR}/count`)).json()) as { count: number }
    ).count;

    await page.locator('button.compose').first().click();
    await page.getByTestId('from-picker-trigger').click();
    await page
      .getByTestId('from-picker-row')
      .filter({ hasText: EXTERNAL_IDENTITY_EMAIL })
      .click();

    const recipient = `undo-ext-${Date.now()}@remote.test`;
    const subject = `undo send external e2e ${Date.now()}`;
    await page.locator('[placeholder="recipient@example.com"]').first().fill(recipient);
    await page.locator('label', { hasText: 'Subject' }).locator('input').fill(subject);
    await page.locator('.ProseMirror').fill('undo send external e2e body');
    await expect(
      page.locator('.recipient-field .chip-label', { hasText: recipient }),
    ).toBeVisible();
    await page.getByTestId('compose-send').click();

    const composeDialog = page.locator('div.modal[role="dialog"]');
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    await clickUndo(page);
    await expect(composeDialog).toBeVisible({ timeout: 5_000 });

    // No leak to the external relay.
    await page.waitForTimeout(2_000);
    const countAfterUndo = (
      (await (await request.get(`http://${FAKESMTP_HTTP_ADDR}/count`)).json()) as { count: number }
    ).count;
    expect(countAfterUndo, 'the undone send must never reach the external relay sink').toBe(
      countBefore,
    );

    // Sent holds no copy; Drafts holds exactly one.
    await expect
      .poll(
        async () =>
          (await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, sentId, subject))
            .length,
        { timeout: 10_000, message: 'the cancelled external send must not leave a copy in Sent' },
      )
      .toBe(0);
    await expect
      .poll(
        async () =>
          (
            await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, draftsId, subject)
          ).length,
        { timeout: 10_000, message: 'the cancelled external send must restore the Email to Drafts' },
      )
      .toBe(1);

    // Re-send; regardless of which identity the re-opened composer ends up
    // using (re #508 -- a separate, already-filed gap: Undo does not
    // restore the selected From identity), the acceptance this issue owns
    // is that Sent ends up with exactly one copy, never two.
    await page.getByTestId('compose-send').click();
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    await expect
      .poll(
        async () =>
          (await emailIdsInMailboxBySubject(request, apiUrl, cookieHeader, mailAccountId, sentId, subject))
            .length,
        { timeout: 10_000, message: 'the re-send must produce exactly one Sent copy' },
      )
      .toBe(1);
  });
});
