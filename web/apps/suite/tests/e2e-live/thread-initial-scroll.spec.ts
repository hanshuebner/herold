/**
 * thread-initial-scroll.spec.ts (re #525)
 *
 * Opening a thread rendered every accordion from the top of the scroll
 * container with no scroll-into-view call of its own. A thread whose
 * unread messages sit near the end therefore left the viewport showing
 * the earliest, already-read messages, with the unread ones below the
 * fold. The fix scrolls the first message of `pickInitialExpanded`'s
 * result into view, aligned to the start of the scroll container, once
 * per thread open.
 *
 * This drives the exact repro shape from the report: a ten-message
 * thread with the last two left unread, opened via the thread list.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the ten-message thread
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts \
 *       tests/e2e-live/thread-initial-scroll.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend and real
 * server-side threading (In-Reply-To/References), not a page.route() mock.
 */

import { test, expect, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, jmapSession, jmapCall, ALICE } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;
const MESSAGE_COUNT = 10;
const UNREAD_TAIL = 2;

/** Minimal SMTP client: EHLO, MAIL FROM, RCPT TO, DATA, QUIT. `headers` are
 *  appended verbatim so a caller can thread deliveries via
 *  Message-ID/In-Reply-To/References. */
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

/** Deliver a thread of `count` messages via SMTP, each In-Reply-To the
 *  previous one. Returns the stable (first-message) subject. */
async function seedThread(count: number, subjectBase: string): Promise<string> {
  const stamp = Date.now();
  const subject = `${subjectBase} ${stamp}`;
  let prevMsgId: string | null = null;
  const refs: string[] = [];
  for (let i = 1; i <= count; i++) {
    const msgId = `issue525-${stamp}-${i}@test.local`;
    const headers: Record<string, string> = { 'Message-ID': `<${msgId}>` };
    if (prevMsgId) {
      headers['In-Reply-To'] = `<${prevMsgId}>`;
      refs.push(`<${prevMsgId}>`);
      headers['References'] = refs.join(' ');
    }
    const s = i === 1 ? subject : `Re: ${subject}`;
    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, s, `Message number ${i} in the thread.`, headers);
    prevMsgId = msgId;
    // receivedAt has whole-second granularity; without a gap, every
    // message in a tight SMTP loop lands in the same second, which
    // makes markUnreadFromHere's receivedAt >= anchor.receivedAt test
    // (REQ-MAIL-133a) flip every same-second message regardless of
    // anchor -- the real repro has replies minutes/hours apart.
    await new Promise((r) => setTimeout(r, 1100));
  }
  return subject;
}

async function markSeen(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  emailIds: string[],
): Promise<void> {
  const update: Record<string, Record<string, unknown>> = {};
  for (const id of emailIds) update[id] = { 'keywords/$seen': true };
  await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/set', { accountId: mailAccountId, update }, 's']],
  );
}

test.describe('Thread initial scroll (issue #525)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('opening a thread scrolls the first unread message into view, leaving the first message out of the viewport', async ({
    page,
    request,
  }) => {
    await login(page);
    await clearMailbox(page, request);

    const subject = await seedThread(MESSAGE_COUNT, 'Issue525');

    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    // The FTS index that backs the `subject` filter lags SMTP delivery
    // (see findEmailIdsBySubject's own comment in live-helpers.ts); poll
    // until all MESSAGE_COUNT members of the thread are indexed, sorted
    // by receivedAt so the array order matches thread order.
    let allIds: string[] = [];
    await expect
      .poll(
        async () => {
          const qBody = await jmapCall(
            request,
            apiUrl,
            cookieHeader,
            ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
            [
              [
                'Email/query',
                {
                  accountId: mailAccountId,
                  filter: { subject: subject.split(' ')[0] },
                  sort: [{ property: 'receivedAt', isAscending: true }],
                  limit: 50,
                },
                'q',
              ],
            ],
          );
          allIds = (qBody.methodResponses as [string, { ids: string[] }, string][])[0]![1].ids;
          return allIds.length;
        },
        { timeout: 15_000, message: 'not all thread members were indexed in time' },
      )
      .toBe(MESSAGE_COUNT);

    // Mark every message but the last UNREAD_TAIL as seen, out-of-band via
    // JMAP directly (not through the page), BEFORE ever opening the
    // thread -- the inbox listing that follows login still sees the final
    // state in one fetch, avoiding a seen-state race against the page's
    // own SSE-fed cache.
    const seenIds = allIds.slice(0, allIds.length - UNREAD_TAIL);
    await markSeen(request, apiUrl, cookieHeader, mailAccountId, seenIds);

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });

    const row = page.locator('.thread-list .thread-row', { hasText: subject.split(' ')[0]! });
    await expect(row).toHaveCount(1, { timeout: 15_000 });
    await row.locator('.row-activate').click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);

    const firstUnreadId = allIds[allIds.length - UNREAD_TAIL]!;
    const firstMessageId = allIds[0]!;
    const firstUnreadBox = page.locator(`[data-email-id="${firstUnreadId}"]`);
    await expect(firstUnreadBox).toBeVisible({ timeout: 15_000 });

    // The scroll runs after two animation frames; give the browser a
    // moment to settle, then assert the visibility split via the DOM's
    // own geometry rather than Playwright's isIntersectingViewport (which
    // measures the browser viewport, not the inner `.scroll` container).
    await expect
      .poll(
        async () =>
          page.evaluate((id) => {
            const container = document.querySelector<HTMLElement>('.scroll');
            const el = document.querySelector<HTMLElement>(`[data-email-id="${id}"]`);
            if (!container || !el) return null;
            const top = el.offsetTop;
            return top >= container.scrollTop && top < container.scrollTop + container.clientHeight;
          }, firstUnreadId),
        { timeout: 10_000 },
      )
      .toBe(true);

    const firstMessageVisible = await page.evaluate((id) => {
      const container = document.querySelector<HTMLElement>('.scroll');
      const el = document.querySelector<HTMLElement>(`[data-email-id="${id}"]`);
      if (!container || !el) return null;
      const top = el.offsetTop;
      return top >= container.scrollTop && top < container.scrollTop + container.clientHeight;
    }, firstMessageId);
    expect(firstMessageVisible).toBe(false);
  });
});
