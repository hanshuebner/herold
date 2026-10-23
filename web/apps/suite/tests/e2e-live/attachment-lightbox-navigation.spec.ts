/**
 * attachment-lightbox-navigation.spec.ts (re #518)
 *
 * The attachment preview lightbox (REQ-MAIL-23) is opened from a message's
 * attachment card strip knowing the ordered list of that message's
 * viewable attachments (image and PDF parts) and the index of the one
 * that was clicked. ArrowRight/ArrowLeft and the on-screen prev/next
 * controls step through the siblings; a "N of M" position indicator
 * tracks the current position; the controls disable at the ends instead
 * of wrapping.
 *
 * This drives the real issue #518 reproduction shape end to end against a
 * live backend: a message with nine image/jpeg attachments (PC1.jpg
 * through PC9.jpg), delivered by raw SMTP so the attachment metadata
 * rendered by the Suite comes from the server's own Email/get, exactly as
 * production does.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with the multi-attachment message
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts \
 *       tests/e2e-live/attachment-lightbox-navigation.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend and real
 * SMTP delivery of a multipart message, not a page.route() mock.
 */

import { test, expect } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, ALICE } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;

// A real (if tiny) 20x20 JPEG. Content is irrelevant to this spec; only
// the image/jpeg MIME type and the nine-attachment shape matter.
const TINY_JPEG_BASE64 =
  '/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDABALDA4MChAODQ4SERATGCgaGBYWGDEjJR0oOjM9PDkzODdASFxOQERXRTc4UG1RV19iZ2hnPk1xeXBkeFxlZ2P/2wBDARESEhgVGC8aGi9jQjhCY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2P/wAARCAAUABQDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwClRRRXIfQhRRRQAUUUUAFFFFAH/9k=';

/** RFC 2045 requires base64 bodies wrapped at <=76 chars per line. */
function wrapBase64(b64: string): string {
  const lines: string[] = [];
  for (let i = 0; i < b64.length; i += 76) lines.push(b64.slice(i, i + 76));
  return lines.join('\r\n');
}

/** Build a multipart/mixed RFC 5322 message with N image attachments. */
function buildMultiAttachmentMime(opts: {
  from: string;
  to: string;
  subject: string;
  attachmentNames: string[];
}): string {
  const boundary = `e2e518boundary${Math.random().toString(36).slice(2)}`;
  const lines: string[] = [
    `From: ${opts.from}`,
    `To: ${opts.to}`,
    `Subject: ${opts.subject}`,
    `Date: ${new Date().toUTCString()}`,
    `Message-ID: <${Math.random().toString(36).slice(2)}@test.local>`,
    `MIME-Version: 1.0`,
    `Content-Type: multipart/mixed; boundary="${boundary}"`,
    ``,
    `--${boundary}`,
    `Content-Type: text/plain; charset=utf-8`,
    ``,
    `see attached`,
  ];
  for (const name of opts.attachmentNames) {
    lines.push(`--${boundary}`);
    lines.push(`Content-Type: image/jpeg; name="${name}"`);
    lines.push(`Content-Disposition: attachment; filename="${name}"`);
    lines.push(`Content-Transfer-Encoding: base64`);
    lines.push(``);
    lines.push(wrapBase64(TINY_JPEG_BASE64));
  }
  lines.push(`--${boundary}--`);
  return lines.join('\r\n');
}

/**
 * Minimal SMTP client sending a pre-built raw RFC 5322 message: EHLO,
 * MAIL FROM, RCPT TO, DATA, QUIT. SMTP transparency (RFC 5321 SS4.5.2)
 * requires doubling any line that starts with a literal "." before the
 * terminating "\r\n.\r\n" -- applied here even though base64 lines are
 * very unlikely to start with a dot, so the helper is correct in general.
 */
async function sendSmtpRaw(addr: string, from: string, to: string, rawMessage: string): Promise<void> {
  const [host, portStr] = addr.split(':');
  const port = Number(portStr);
  const stuffed = rawMessage
    .split('\r\n')
    .map((line) => (line.startsWith('.') ? `.${line}` : line))
    .join('\r\n');

  await new Promise<void>((resolve, reject) => {
    const socket = net.createConnection({ host, port });
    let buf = '';
    const steps = [`EHLO test.local\r\n`, `MAIL FROM:<${from}>\r\n`, `RCPT TO:<${to}>\r\n`, `DATA\r\n`];
    let stepIdx = 0;
    let inData = false;

    socket.setEncoding('utf8');
    socket.on('error', reject);
    socket.on('data', (chunk: string) => {
      buf += chunk;
      if (!buf.endsWith('\r\n')) return;
      const lastLine = buf.trim().split('\r\n').pop() ?? '';
      buf = '';
      const code = lastLine.slice(0, 3);

      if (inData) {
        if (code === '250') {
          socket.write('QUIT\r\n');
          socket.end();
          resolve();
        } else {
          reject(new Error(`SMTP DATA rejected: ${lastLine}`));
        }
        return;
      }

      if (!code.startsWith('2') && !code.startsWith('3')) {
        reject(new Error(`SMTP error at step ${stepIdx}: ${lastLine}`));
        return;
      }

      if (stepIdx < steps.length) {
        socket.write(steps[stepIdx]!);
        stepIdx++;
      } else if (!inData) {
        inData = true;
        socket.write(`${stuffed}\r\n.\r\n`);
      }
    });
  });
}

test.describe('attachment lightbox navigation across a message (issue #518)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('ArrowRight, the next control, and the boundary case all step through the nine attachments', async ({
    page,
    request,
  }) => {
    test.setTimeout(45_000);

    const attachmentNames = Array.from({ length: 9 }, (_, i) => `PC${i + 1}.jpg`);
    const subject = `attachment lightbox nav e2e ${Date.now()}`;
    const raw = buildMultiAttachmentMime({
      from: 'sender@example.com',
      to: ALICE,
      subject,
      attachmentNames,
    });

    await login(page);
    await clearMailbox(page, request);
    await sendSmtpRaw(SMTP_ADDR!, 'sender@example.com', ALICE, raw);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });

    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);

    // All nine attachment cards render before any lightbox interaction.
    const cards = page.locator('.attachments .card');
    await expect(cards).toHaveCount(9, { timeout: 15_000 });

    // Open the first image -- the acceptance shape named in the issue.
    await cards.filter({ hasText: 'PC1.jpg' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC1.jpg');
    await expect(dialog.locator('.lightbox-position')).toHaveText('1 of 9');
    await expect(dialog.getByRole('button', { name: 'Previous attachment' })).toBeDisabled();

    // ArrowRight steps to the second attachment -- the issue's named
    // acceptance check.
    await page.keyboard.press('ArrowRight');
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC2.jpg');
    await expect(dialog.locator('.lightbox-position')).toHaveText('2 of 9');

    // The on-screen next control steps forward too, and works via a real
    // mouse click (not just the keyboard).
    await dialog.getByRole('button', { name: 'Next attachment' }).click();
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC3.jpg');
    await expect(dialog.locator('.lightbox-position')).toHaveText('3 of 9');

    // ArrowLeft steps back.
    await page.keyboard.press('ArrowLeft');
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC2.jpg');
    await expect(dialog.locator('.lightbox-position')).toHaveText('2 of 9');

    // Walk forward to the last attachment and confirm the end-of-list
    // case: the next control disables and a further ArrowRight is a
    // no-op rather than wrapping back to the first attachment.
    for (let i = 0; i < 7; i++) {
      await page.keyboard.press('ArrowRight');
    }
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC9.jpg');
    await expect(dialog.locator('.lightbox-position')).toHaveText('9 of 9');
    await expect(dialog.getByRole('button', { name: 'Next attachment' })).toBeDisabled();
    await page.keyboard.press('ArrowRight');
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC9.jpg');
    await expect(dialog.locator('.lightbox-position')).toHaveText('9 of 9');

    // Escape still closes the lightbox from mid-navigation.
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });

  test('stepping shows a spinner at once instead of the stale image, and neighbours are prefetched', async ({
    page,
    request,
  }) => {
    test.setTimeout(60_000);

    const attachmentNames = ['PC1.jpg', 'PC2.jpg', 'PC3.jpg', 'PC4.jpg'];
    const subject = `attachment lightbox loading e2e ${Date.now()}`;
    const raw = buildMultiAttachmentMime({
      from: 'sender@example.com',
      to: ALICE,
      subject,
      attachmentNames,
    });

    await login(page);
    await clearMailbox(page, request);
    await sendSmtpRaw(SMTP_ADDR!, 'sender@example.com', ALICE, raw);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
    const cards = page.locator('.attachments .card');
    await expect(cards).toHaveCount(4, { timeout: 15_000 });

    // Simulate slow downloads: every inline blob download is held for
    // 1.5 s. The request log shows which URLs were requested.
    const requested: string[] = [];
    await page.route('**/*disposition=inline*', async (route) => {
      requested.push(route.request().url());
      await new Promise((r) => setTimeout(r, 1500));
      await route.continue();
    });

    await cards.filter({ hasText: 'PC1.jpg' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC1.jpg');
    await expect(dialog.locator('.lightbox-spinner')).toBeVisible();

    // Once the first image has arrived, the spinner is gone and the
    // neighbour has been requested ahead of any stepping.
    await expect(dialog.locator('.lightbox-spinner')).toHaveCount(0, { timeout: 10_000 });
    await expect.poll(() => requested.length, { timeout: 10_000 }).toBeGreaterThan(1);

    // Step to the last (not yet prefetched) attachment by jumping:
    // the position indicator and name update immediately and the
    // stale image is replaced by the spinner.
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await expect(dialog.locator('.lightbox-position')).toHaveText('4 of 4');
    await expect(dialog.locator('.lightbox-name')).toHaveText('PC4.jpg');
    await expect(dialog.locator('.lightbox-spinner')).toBeVisible();
    await expect(dialog.locator('img.lightbox-img:not(.pending)')).toHaveCount(0);

    await expect(dialog.locator('.lightbox-spinner')).toHaveCount(0, { timeout: 10_000 });
    await expect(dialog.locator('img.lightbox-img:not(.pending)')).toHaveCount(1);

    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });
});
