/**
 * snooze-banner.spec.ts (re #469, work item 1)
 *
 * The while-snoozed banner reads `Email.snoozedUntil` and
 * `Email.snoozeWakeMailboxId`, already on the wire since issue #274, so
 * this drives the whole round trip against a real backend: snooze a
 * message from the thread reader, open it from the Snoozed view, read
 * the banner's due time, cancel it, and confirm via raw JMAP that the
 * server has cleared both the deadline and the `$snoozed` keyword (the
 * rule from #360 -- a cancelled snooze must not leave a wake time behind).
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with a deterministic message before the test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/snooze-banner.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend and real
 * SMTP delivery.
 */

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import {
  login,
  clearMailbox,
  jmapSession,
  jmapCall,
  findEmailIdsBySubject,
  ALICE,
} from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;

/** Minimal SMTP client: EHLO, MAIL FROM, RCPT TO, DATA, QUIT. */
async function sendSmtp(
  addr: string,
  from: string,
  to: string,
  subject: string,
  body: string,
): Promise<void> {
  const [host, portStr] = addr.split(':');
  const port = Number(portStr);

  await new Promise<void>((resolve, reject) => {
    const socket = net.createConnection({ host, port });
    let buf = '';
    const steps = [
      `EHLO test.local\r\n`,
      `MAIL FROM:<${from}>\r\n`,
      `RCPT TO:<${to}>\r\n`,
      `DATA\r\n`,
    ];
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
        const msg =
          `From: ${from}\r\n` +
          `To: ${to}\r\n` +
          `Subject: ${subject}\r\n` +
          `Date: ${new Date().toUTCString()}\r\n` +
          `Message-ID: <${Math.random().toString(36).slice(2)}@test.local>\r\n` +
          `\r\n` +
          `${body}\r\n` +
          `.\r\n`;
        inData = true;
        socket.write(msg);
      }
    });
  });
}

/** Log in, wipe the mailbox, seed one fresh deterministic message, then
 *  reload so the seeded message is what's rendered on screen. */
async function loginWithFreshInbox(
  page: Page,
  request: APIRequestContext,
  subject: string,
  body: string,
): Promise<void> {
  await login(page);
  await clearMailbox(page, request);
  await sendSmtp(SMTP_ADDR!, 'sender@example.com', ALICE, subject, body);
  await page.reload();
  await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
}

/** `datetime-local` input value for `date`, in the browser's local time. */
function datetimeLocalValue(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
}

test.describe('while-snoozed banner (issue #469)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('snoozing a message shows the banner with its due time; Cancel clears it on the server', async ({
    page,
    request,
  }) => {
    const subject = 'Snooze banner test ' + Math.random().toString(36).slice(2);
    const body = 'Snooze banner test body.';
    await loginWithFreshInbox(page, request, subject, body);

    // Open the thread from the Inbox and confirm no banner shows yet --
    // the message was never snoozed.
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
    await expect(page.locator('.snooze-banner')).toHaveCount(0);

    // Snooze it a few minutes into the future via the toolbar's Snooze
    // action and the picker's custom-datetime field, so the due time is
    // deterministic and always lands on today's date (dayDiff 0 in
    // formatWakeTime, i.e. a plain time-of-day string in the banner).
    const wakeAt = new Date(Date.now() + 5 * 60 * 1000);
    await page.getByRole('button', { name: 'Snooze', exact: true }).click();
    await page.locator('.modal input[type="datetime-local"]').fill(datetimeLocalValue(wakeAt));
    await page.locator('.modal button.commit').click();
    await expect(page.locator('.modal')).toHaveCount(0);

    // The banner appears in the still-open reader (the message's
    // mailboxIds do not change on snooze, so ThreadReader is not bounced
    // away by the auto-navigate-away effect -- re #29/#294 -- the way an
    // actual Move would). It names a due time in the usual HH:MM shape.
    const banner = page.locator('.snooze-banner');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(/\d{1,2}:\d{2}/);

    // Re-open the same message from the Snoozed view (re #469's acceptance:
    // "opened through search or the Snoozed view"). Only the URL fragment
    // changes, so the browser treats this as a same-document navigation
    // that the SPA's own router drives, same as clicking the sidebar link.
    await page.goto('/#/mail/folder/snoozed');
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
    await expect(page.locator('.snooze-banner')).toBeVisible();
    await expect(page.locator('.snooze-banner')).toContainText(/\d{1,2}:\d{2}/);

    // Cancel the reminder from the banner.
    await page.locator('.snooze-banner button').click();
    await expect(page.locator('.snooze-banner')).toHaveCount(0, { timeout: 10_000 });

    // Server-side: a raw Email/get shows snoozedUntil null and no
    // $snoozed keyword -- the deliberate-cancel contract from #360, which
    // a merely-hidden wake time would violate.
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const emailIds = await findEmailIdsBySubject(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      subject,
    );
    const getBody = await jmapCall(
      request,
      apiUrl,
      cookieHeader,
      ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
      [
        [
          'Email/get',
          {
            accountId: mailAccountId,
            ids: [emailIds[0]!],
            properties: ['snoozedUntil', 'keywords'],
          },
          'g',
        ],
      ],
    );
    const got = (
      getBody.methodResponses as [
        string,
        { list: { snoozedUntil: string | null; keywords: Record<string, boolean> }[] },
        string,
      ][]
    )[0]![1].list[0]!;
    expect(got.snoozedUntil).toBeNull();
    expect(got.keywords['$snoozed']).toBeFalsy();

    // The message is back in the Inbox listing.
    await page.goto('/#/mail');
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
  });
});
