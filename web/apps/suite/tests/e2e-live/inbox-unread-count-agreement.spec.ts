/**
 * inbox-unread-count-agreement.spec.ts (issue #494)
 *
 * The sidebar Inbox badge and the document title both read the server's
 * `Mailbox.unreadThreads`, while the category-tab badges are derived
 * client-side from `mail.listEmailIds`, which already excludes a
 * "$snoozed" member (issue #468). Before the #494 fix, an unread message
 * snoozed in the Inbox was still counted by `Mailbox.unreadThreads`
 * (`CountThreads` had no snooze awareness), so the sidebar/title figure
 * read one higher than the sum of the category-tab badges. This drives
 * the exact scenario from the report: one ordinary unread message and
 * one unread message that gets snoozed, and asserts every indicator
 * agrees once the snooze takes effect.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with deterministic messages before the test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/inbox-unread-count-agreement.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend so the
 * server's `CountThreads` snooze exclusion is what the Suite is
 * actually exercising, not a page.route() mock.
 */

import { test, expect } from '@playwright/test';
import net from 'node:net';
import {
  login,
  clearMailbox,
  jmapSession,
  jmapCall,
  findEmailIdsBySubject,
  tabBadgeSum,
  sidebarInboxCount,
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

test.describe('Inbox unread-count agreement (issue #494)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('sidebar badge, document title and category-tab sum all agree with a snoozed unread member excluded', async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const ordinarySubject = `Issue494 ordinary ${stamp}`;
    // "+updates" drives the dev instance's fake classifier
    // (internal/testfakes/fakeclassify) into the "updates" category, so
    // this message renders under its own category tab -- the same shape
    // as the reported bug (an "Updates" tab distinct from Primary).
    const snoozeSubject = `Issue494 +updates ${stamp}`;

    await login(page);
    await clearMailbox(page, request);

    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, ordinarySubject, 'Stays unread in Primary.');
    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, snoozeSubject, 'Gets snoozed while unread.');

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    // The default view is the Primary tab; the ordinary message lands
    // there, the "+updates" one under its own "Updates" tab.
    await expect(page.locator('.thread-list .thread-row', { hasText: ordinarySubject })).toHaveCount(1, {
      timeout: 15_000,
    });
    await page.locator('.tab-strip .tab', { hasText: 'Updates' }).click();
    await expect(page.locator('.thread-list .thread-row', { hasText: snoozeSubject })).toHaveCount(1, {
      timeout: 15_000,
    });

    // Baseline: both messages unread, nothing snoozed yet -- every
    // indicator already agrees at 2.
    await expect.poll(() => sidebarInboxCount(page), { timeout: 15_000 }).toBe(2);
    await expect.poll(() => page.title(), { timeout: 15_000 }).toBe('(2) Herold');
    await expect.poll(() => tabBadgeSum(page), { timeout: 15_000 }).toBe(2);

    // Snooze the second message via raw JMAP (mirrors the "set a
    // reminder" action) to a far-future wake time so the snooze worker
    // does not release it during this test.
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const [snoozeEmailId] = await findEmailIdsBySubject(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      snoozeSubject,
    );
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
            update: { [snoozeEmailId!]: { snoozedUntil: '2099-01-01T00:00:00Z' } },
          },
          's',
        ],
      ],
    );

    // After the snooze, every indicator agrees at 1 -- the snoozed
    // message is excluded from the sidebar/title count (the #494 fix)
    // exactly as it is already excluded from the category-tab list
    // (the #468 fix). None of this requires a reload: the store's own
    // Email/changes-driven refresh (store.svelte.ts #onEmailStateChange)
    // picks up the mutation via the page's live EventSource connection.
    await expect(page.locator('.thread-list .thread-row', { hasText: snoozeSubject })).toHaveCount(0, {
      timeout: 15_000,
    });
    await expect.poll(() => sidebarInboxCount(page), { timeout: 15_000 }).toBe(1);
    await expect.poll(() => page.title(), { timeout: 15_000 }).toBe('(1) Herold');
    await expect.poll(() => tabBadgeSum(page), { timeout: 15_000 }).toBe(1);

    await page.screenshot({ path: 'test-results/issue-494-unread-count-agreement.png' });
  });
});
