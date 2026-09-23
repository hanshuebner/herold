/**
 * snooze-wake.spec.ts (re #469, work item 3)
 *
 * The on-wake banner and list-row marker read `Email.snoozeWokeAt` and
 * `Email.snoozeWokeFor`, set by the snooze worker (`internal/snooze`) the
 * moment it releases a due reminder and cleared again once the message
 * gains `$seen`. This drives the whole round trip against a real backend:
 * snooze a message to a due time a few seconds out, wait for the dev
 * instance's snooze worker (polling every `HEROLD_DEV_SNOOZE_POLL`,
 * 5s by default -- see scripts/dev-instance.sh) to release it, confirm
 * the banner and row marker appear without a reload (they follow the
 * store's `Email/changes` fold, not a fetch triggered by this test), then
 * read the message and confirm both disappear and the server has cleared
 * the two properties.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with a deterministic message before the test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/snooze-wake.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend, real SMTP
 * delivery, and the live snooze worker's own polling cadence.
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

type EmailGetProps = {
  snoozedUntil: string | null;
  snoozeWokeAt: string | null;
  snoozeWokeFor: string | null;
  keywords: Record<string, boolean>;
};

async function getEmail(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  emailId: string,
): Promise<EmailGetProps> {
  const body = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [
      [
        'Email/get',
        {
          accountId: mailAccountId,
          ids: [emailId],
          properties: ['snoozedUntil', 'snoozeWokeAt', 'snoozeWokeFor', 'keywords'],
        },
        'g',
      ],
    ],
  );
  return (body.methodResponses as [string, { list: EmailGetProps[] }, string][])[0]![1].list[0]!;
}

test.describe('on-wake banner and list-row marker (issue #469)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('a released reminder shows why the message is back, then clears on read', async ({
    page,
    request,
  }) => {
    const subject = 'Snooze wake test ' + Math.random().toString(36).slice(2);
    const body = 'Snooze wake test body.';
    await loginWithFreshInbox(page, request, subject, body);

    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const emailIds = await findEmailIdsBySubject(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      subject,
    );
    const emailId = emailIds[0]!;

    // No indication on a message that was never snoozed.
    await expect(page.locator('.thread-list .thread-row .woke-badge')).toHaveCount(0);

    // Set the reminder a few seconds out via raw JMAP -- second-level
    // precision the UI's minute-granularity datetime-local picker (used
    // by snooze-banner.spec.ts, work item 1) cannot express, and this
    // spec is about the wake half of the flow, not the picker.
    const wakeAt = new Date(Date.now() + 6000);
    await jmapCall(
      request,
      apiUrl,
      cookieHeader,
      ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
      [
        [
          'Email/set',
          {
            accountId: mailAccountId,
            update: { [emailId]: { snoozedUntil: wakeAt.toISOString() } },
          },
          's',
        ],
      ],
    );

    // Wait for the dev instance's snooze worker to release it: the
    // server records the wake marker on the message and clears
    // snoozedUntil, each its own Email change-feed row.
    await expect
      .poll(
        async () => {
          const got = await getEmail(request, apiUrl, cookieHeader, mailAccountId, emailId);
          return got.snoozeWokeAt;
        },
        { timeout: 20_000, message: 'snooze worker never released the reminder' },
      )
      .not.toBeNull();

    // The row marker appears without a reload -- the store's own
    // Email/changes handling picks up the release and folds the new
    // properties into the cached Email.
    const rowMarker = page.locator('.thread-list .thread-row .woke-badge');
    await expect(rowMarker).toBeVisible({ timeout: 15_000 });
    await expect(rowMarker).toContainText('Reminder');

    // Opening the message shows the on-wake banner naming the due time,
    // and not the while-snoozed banner (mutually exclusive by contract).
    // The reader's own auto-read-on-expand effect marks $seen the instant
    // the accordion mounts expanded, which -- per the wire contract --
    // clears snoozeWokeAt/snoozeWokeFor in the store the same tick; the
    // banner is snapshotted at mount so it still shows for this viewing
    // session instead of disappearing before it can be read.
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
    const wokeBanner = page.locator('.snooze-banner.woke');
    await expect(wokeBanner).toBeVisible();
    await expect(wokeBanner).toContainText(/\d{1,2}:\d{2}/);
    await expect(wokeBanner.locator('button')).toHaveCount(0);

    // Back in the list -- without a reload -- the row marker is already
    // gone: the store's own optimistic clear (mirroring the server's
    // side effect of gaining $seen) applied the moment the reader opened.
    await page.goto('/#/mail');
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row .woke-badge')).toHaveCount(0, {
      timeout: 15_000,
    });

    // Server-side: $seen is set and both wake properties are cleared.
    const finalState = await getEmail(request, apiUrl, cookieHeader, mailAccountId, emailId);
    expect(finalState.keywords['$seen']).toBeTruthy();
    expect(finalState.snoozeWokeAt).toBeNull();
    expect(finalState.snoozeWokeFor).toBeNull();

    // Re-opening the now-read message is a fresh accordion mount with no
    // wake marker left to snapshot -- the indication is gone for good,
    // matching "stops saying so once it is read".
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
    await expect(page.locator('.snooze-banner')).toHaveCount(0);
  });
});
