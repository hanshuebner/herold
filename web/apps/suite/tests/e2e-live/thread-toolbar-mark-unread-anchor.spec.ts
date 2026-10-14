/**
 * thread-toolbar-mark-unread-anchor.spec.ts (re #526)
 *
 * The thread toolbar's Mark unread button called `markThreadSeen`, which
 * flips every message in the thread regardless of which ones are shown.
 * The fix anchors it on the first currently-expanded message instead
 * (the same `markUnreadFromHere` operation the per-message "Mark unread
 * from here" kebab item uses), so collapsed earlier messages stay read.
 *
 * This drives the exact repro shape from the report: a five-message
 * thread where only the last two are expanded (unread); clicking the
 * toolbar's Mark unread must leave the first three read and flip only
 * the last two back to unread.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the five-message thread
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts \
 *       tests/e2e-live/thread-toolbar-mark-unread-anchor.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend and real
 * server-side threading (In-Reply-To/References), not a page.route() mock.
 */

import { test, expect, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, jmapSession, jmapCall, ALICE } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;
const MESSAGE_COUNT = 5;
const UNREAD_TAIL = 2;

/** Minimal SMTP client: EHLO, MAIL FROM, RCPT TO, DATA, QUIT. */
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
    const msgId = `issue526-${stamp}-${i}@test.local`;
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

async function getSeenStates(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  emailIds: string[],
): Promise<Record<string, boolean>> {
  const body = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/get', { accountId: mailAccountId, ids: emailIds, properties: ['id', 'keywords'] }, 'g']],
  );
  const list = (body.methodResponses as [string, { list: { id: string; keywords: Record<string, boolean> }[] }, string][])[0]![1]
    .list;
  const out: Record<string, boolean> = {};
  for (const e of list) out[e.id] = Boolean(e.keywords.$seen);
  return out;
}

test.describe('Thread toolbar Mark unread anchors on the expanded set (issue #526)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('clicking the toolbar Mark unread flips only the expanded (last two) messages, leaving the first three read', async ({
    page,
    request,
  }) => {
    await login(page);
    await clearMailbox(page, request);

    const subject = await seedThread(MESSAGE_COUNT, 'Issue526');

    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    // The FTS index that backs the `subject` filter lags SMTP delivery;
    // poll until all MESSAGE_COUNT members of the thread are indexed,
    // sorted by receivedAt so the array order matches thread order.
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

    const readIds = allIds.slice(0, allIds.length - UNREAD_TAIL);
    const unreadIds = allIds.slice(allIds.length - UNREAD_TAIL);

    // Mark the first three seen out-of-band, BEFORE ever opening the
    // thread, so the thread's initial expansion (pickInitialExpanded)
    // expands exactly the last two.
    await markSeen(request, apiUrl, cookieHeader, mailAccountId, readIds);

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });

    const row = page.locator('.thread-list .thread-row', { hasText: subject.split(' ')[0]! });
    await expect(row).toHaveCount(1, { timeout: 15_000 });
    await row.locator('.row-activate').click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);

    // Confirm the expanded set matches the anchor the fix is expected to
    // pick: the first two read messages stay collapsed, the last two
    // (unread) are expanded.
    for (const id of readIds) {
      await expect(page.locator(`[data-email-id="${id}"] .header`)).toHaveAttribute('aria-expanded', 'false');
    }
    for (const id of unreadIds) {
      await expect(page.locator(`[data-email-id="${id}"] .header`)).toHaveAttribute('aria-expanded', 'true');
    }

    await page.getByRole('button', { name: 'Mark unread' }).click();

    // The click also navigates back to the list; read the final state
    // directly from the server rather than the page.
    await expect
      .poll(async () => getSeenStates(request, apiUrl, cookieHeader, mailAccountId, allIds), { timeout: 10_000 })
      .toEqual(
        Object.fromEntries([
          ...readIds.map((id) => [id, true]),
          ...unreadIds.map((id) => [id, false]),
        ]),
      );
  });
});
