/**
 * e2e-live (re #512): an outgoing attachment whose filename contains
 * spaces must round-trip through a real send -- the fix in
 * internal/protojmap/mail/email/bodybuild.go's mediaParam quotes (or RFC
 * 2231-encodes) the Content-Type "name" and Content-Disposition
 * "filename" parameters instead of writing them bare, which made the
 * stored message's own Content-Type header fail to parse and the Suite
 * fall back to "(unnamed)" / application/octet-stream.
 *
 * This drives the real compose -> send -> Sent-copy path through the
 * browser (not a mocked JMAP response) so the attachment metadata the
 * Suite displays comes from the server's own Email/get, exactly as
 * production does.
 */

import { test, expect } from '@playwright/test';
import { login, clearMailbox, jmapSession, jmapCall, findEmailIdsBySubject } from './live-helpers';

const ATTACHMENT_NAME = 'Offer_ Draft with spaces.pdf';

test.describe('attachment name with spaces survives send (re #512)', () => {
  test.beforeEach(async ({ page, request }) => {
    await login(page);
    await clearMailbox(page, request);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  });

  test('Sent copy shows the real filename and application/pdf type', async ({ page, request }) => {
    test.setTimeout(45_000);

    const recipient = `attname-${Date.now()}@remote.test`;
    const subject = `attachment name with spaces e2e ${Date.now()}`;

    await page.locator('button.compose').first().click();
    await page.locator('[placeholder="recipient@example.com"]').first().fill(recipient);
    const subjectInput = page.locator('label', { hasText: 'Subject' }).locator('input');
    await subjectInput.fill(subject);
    await page.locator('.ProseMirror').fill('see attached');

    // Attach a PDF whose name contains spaces and an underscore -- the
    // exact shape reported in #512. Playwright's setInputFiles accepts an
    // in-memory buffer, so no fixture file on disk is needed. The modal
    // footer's hidden file input (no `accept` restriction) is the
    // "Attach" button's target; the toolbar also has an image-only file
    // input (accept="image/*") for inline images, which this is not.
    const fileInput = page.getByRole('contentinfo').locator('input[type="file"]');
    await fileInput.setInputFiles({
      name: ATTACHMENT_NAME,
      mimeType: 'application/pdf',
      buffer: Buffer.from('%PDF-1.4 fake pdf body for e2e test #512\n'),
    });

    // Wait for the upload to finish before sending.
    const chip = page.locator('.attachments-list li', { hasText: ATTACHMENT_NAME });
    await expect(chip).toBeVisible({ timeout: 10_000 });
    await expect(chip.locator('.att-status')).toHaveText('Ready', { timeout: 10_000 });

    await expect(
      page.locator('.recipient-field .chip-label', { hasText: recipient }),
    ).toBeVisible();

    await page.getByTestId('compose-send').click();

    const composeDialog = page.locator('div.modal[role="dialog"]');
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });

    // Confirm via JMAP that the Sent copy carries the real name and type
    // -- this is exactly the Email/get data the thread view's
    // AttachmentList binds, so a correct value here means the UI shows
    // the real name instead of "(unnamed)" / application/octet-stream.
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const ids = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, subject);
    const emailID = ids[0]!;

    const body = await jmapCall(
      request,
      apiUrl,
      cookieHeader,
      ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
      [
        [
          'Email/get',
          { accountId: mailAccountId, ids: [emailID], properties: ['attachments', 'hasAttachment', 'threadId'] },
          'g',
        ],
      ],
    );
    const list = (
      body.methodResponses as [
        string,
        {
          list: {
            hasAttachment: boolean;
            threadId: string;
            attachments: { name: string; type: string }[];
          }[];
        },
        string,
      ][]
    )[0]![1].list;
    expect(list).toHaveLength(1);
    const email = list[0]!;
    expect(email.hasAttachment).toBe(true);
    expect(email.attachments).toHaveLength(1);
    expect(email.attachments[0]!.name).toBe(ATTACHMENT_NAME);
    expect(email.attachments[0]!.type).toBe('application/pdf');

    // Visual confirmation: the thread view's attachment card must show
    // the real name and type, not "(unnamed)" / application/octet-stream
    // (the exact symptom screenshotted in #512).
    await page.goto(`/#/mail/thread/${encodeURIComponent(email.threadId)}`);
    const card = page.locator('.attachments .card', { hasText: ATTACHMENT_NAME });
    await expect(card.locator('.card-name')).toHaveText(ATTACHMENT_NAME, { timeout: 10_000 });
    await expect(card.locator('.card-sub')).toContainText('application/pdf');
  });
});
