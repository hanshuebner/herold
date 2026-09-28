/**
 * thread-unread-member-reachability.spec.ts (issue #497)
 *
 * The sidebar Inbox unread count, the document title, and any
 * category-tab badge all read (or derive from) the server's
 * `Mailbox.unreadThreads`, which counts a thread once when ANY of its
 * members is unread. Before this fix, the collapsed list's row state and
 * the tab badge read only the collapsed-thread representative's `$seen`
 * (the newest message by the list sort) -- so a thread whose sole unread
 * message was an OLDER one showed as read, with no tab badge, while the
 * sidebar and title still counted it. The count pointed at a row the user
 * had no way to find (REQ-UI-13o).
 *
 * This drives the exact repro from the report: two messages in one
 * thread, the newer one marked seen while the older stays unread.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the two-message thread and a "+updates" message that
 *                 forces the category-tab strip to render
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts \
 *       tests/e2e-live/thread-unread-member-reachability.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend and real
 * server-side threading (In-Reply-To/References), not a page.route() mock.
 */

import { test, expect, type APIRequestContext } from '@playwright/test';
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

/** Minimal SMTP client: EHLO, MAIL FROM, RCPT TO, DATA, QUIT. `headers` are
 *  appended verbatim after the standard From/To/Subject/Date headers, so
 *  a caller can thread two deliveries via Message-ID/In-Reply-To/References. */
async function sendSmtp(
  addr: string,
  from: string,
  to: string,
  subject: string,
  body: string,
  headers: Record<string, string> = {},
): Promise<void> {
  const [host, portStr] = addr.split(':');
  const port = Number(portStr);

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
        const extraHeaders = Object.entries(headers)
          .map(([k, v]) => `${k}: ${v}\r\n`)
          .join('');
        const msg =
          `From: ${from}\r\n` +
          `To: ${to}\r\n` +
          `Subject: ${subject}\r\n` +
          `Date: ${new Date().toUTCString()}\r\n` +
          extraHeaders +
          `\r\n` +
          `${body}\r\n` +
          `.\r\n`;
        inData = true;
        socket.write(msg);
      }
    });
  });
}

async function markSeen(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  emailId: string,
): Promise<void> {
  await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/set', { accountId: mailAccountId, update: { [emailId]: { 'keywords/$seen': true } } }, 's']],
  );
}

test.describe('Thread unread-member reachability (issue #497)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('a thread whose older member is unread renders unread, badges agree, and opening it clears every indicator', async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const subject = `Issue497 thread ${stamp}`;
    const replySubject = `Re: ${subject}`;
    // "+updates" forces the dev instance's fake classifier
    // (internal/testfakes/fakeclassify) to file this message under its own
    // category tab, so the tab strip renders (matching the reported shape:
    // a badge distinct from Primary).
    const updatesSubject = `Issue497 +updates ${stamp}`;
    const msgId1 = `issue497-1-${stamp}@test.local`;
    const msgId2 = `issue497-2-${stamp}@test.local`;

    await login(page);
    await clearMailbox(page, request);

    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, subject, 'First message in the thread.', {
      'Message-ID': `<${msgId1}>`,
    });
    await sendSmtp(
      SMTP_ADDR!,
      'sender@example.local',
      ALICE,
      replySubject,
      'Second message, a reply -- this one gets marked seen.',
      { 'Message-ID': `<${msgId2}>`, 'In-Reply-To': `<${msgId1}>`, References: `<${msgId1}>` },
    );
    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, updatesSubject, 'Seeds the Updates tab.');

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.tab-strip .tab', { hasText: 'Updates' })).toBeVisible({ timeout: 15_000 });
    // The row shows the thread's stable (original) subject, not the
    // representative message's own "Re: " subject (see MailView.svelte's
    // rowSubject()).
    const row = page.locator('.thread-list .thread-row', { hasText: subject });
    await expect(row).toHaveCount(1, { timeout: 15_000 });

    // Baseline: both threads (the repro thread and the "Updates" seed) are
    // fully unread -- every indicator agrees at 2, and the row is unread
    // (trivially true at this point; the real assertion is that it STAYS
    // unread once the representative alone is marked seen, below).
    await expect.poll(() => sidebarInboxCount(page), { timeout: 15_000 }).toBe(2);
    await expect.poll(() => page.title(), { timeout: 15_000 }).toBe('(2) Herold');
    await expect.poll(() => tabBadgeSum(page), { timeout: 15_000 }).toBe(2);
    await expect(row).toHaveClass(/unread/, { timeout: 15_000 });

    // Mark only the newer message (the collapsed-thread representative)
    // seen, out of band, leaving the older one unread -- the exact shape
    // from the report (thread 3994: representative seen, older unread).
    // Also mark the "Updates" seed message seen, so it no longer
    // contributes to the unread counts this test asserts on -- it exists
    // only to force the tab strip to render.
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const [replyId] = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, replySubject);
    const [updatesId] = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, updatesSubject);
    await markSeen(request, apiUrl, cookieHeader, mailAccountId, replyId!);
    await markSeen(request, apiUrl, cookieHeader, mailAccountId, updatesId!);

    // Transition: the sidebar/title (server truth, unaffected by the
    // client-side fix) drop from 2 to 1 once the mutations have propagated
    // through the live EventSource connection -- this is the signal that
    // the client has actually observed the mark-seen, not merely that the
    // row's initial (pre-mutation) unread class happened to already be
    // present. Only once that transition has landed do the row and the
    // tab badge get checked: before the fix, both read only the
    // representative's `$seen` and would have gone to "read" / badge-less
    // here, even though the thread's older member is still unread.
    await expect.poll(() => sidebarInboxCount(page), { timeout: 15_000 }).toBe(1);
    await expect.poll(() => page.title(), { timeout: 15_000 }).toBe('(1) Herold');
    await expect.poll(() => tabBadgeSum(page), { timeout: 15_000 }).toBe(1);
    await expect(row).toHaveClass(/unread/, { timeout: 15_000 });

    await row.locator('.row-activate').click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);

    // Opening the thread expands every unread message (pickInitialExpanded)
    // -- including the collapsed older one -- and MessageAccordion's
    // auto-read effect marks each expanded-and-unread message seen, so
    // every indicator reads 0 without a reload.
    await expect.poll(() => sidebarInboxCount(page), { timeout: 15_000 }).toBe(0);
    await expect.poll(() => page.title(), { timeout: 15_000 }).toBe('Herold');
    await expect.poll(() => tabBadgeSum(page), { timeout: 15_000 }).toBe(0);

    await page.screenshot({ path: 'test-results/issue-497-thread-unread-member-reachability.png' });
  });
});
