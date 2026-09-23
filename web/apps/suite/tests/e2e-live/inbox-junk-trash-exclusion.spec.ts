/**
 * inbox-junk-trash-exclusion.spec.ts (issue #467)
 *
 * The inbox and folder views exclude Junk via the herold `notInMailbox`
 * filter condition; they never exclude Trash. A message that a classifier
 * verdict (or another client) has filed to Junk stays out of the Inbox
 * view even while it keeps its Inbox membership; a message that also sits
 * in Trash is listed normally.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with deterministic messages before the test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/inbox-junk-trash-exclusion.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend so the
 * server's `notInMailbox` filter (both fast and slow query paths) is
 * what the Suite is actually exercising, not a page.route() mock.
 */

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, jmapSession, jmapCall, findEmailIdsBySubject, ALICE } from './live-helpers';

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

/** Add `mailboxId` to `emailId`'s membership set without dropping any
 *  existing membership -- simulates another client/device (a classifier
 *  verdict filing to Junk, a move to Trash) touching the account while
 *  the Suite's own Inbox membership stays put. */
async function addMailboxMembership(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  emailId: string,
  mailboxId: string,
): Promise<void> {
  await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [
      [
        'Email/set',
        { accountId: mailAccountId, update: { [emailId]: { [`mailboxIds/${mailboxId}`]: true } } },
        'u',
      ],
    ],
  );
}

test.describe('inbox Junk/Trash exclusion (issue #467)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('Inbox+Junk is hidden from the inbox and listed in Spam; Inbox+Trash stays in the inbox', async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const junkSubject = `Issue467 Inbox+Junk ${stamp}`;
    const trashSubject = `Issue467 Inbox+Trash ${stamp}`;

    await login(page);
    await clearMailbox(page, request);

    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, junkSubject, 'Gains a Junk membership.');
    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, trashSubject, 'Gains a Trash membership.');

    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const [junkEmailId] = await findEmailIdsBySubject(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      junkSubject,
    );
    const [trashEmailId] = await findEmailIdsBySubject(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      trashSubject,
    );
    const junkMailboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'junk');
    const trashMailboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'trash');

    // A second session touches the account out-of-band: files one message
    // to Junk and one to Trash, in both cases keeping the Inbox membership
    // the SMTP delivery gave it.
    await addMailboxMembership(request, apiUrl, cookieHeader, mailAccountId, junkEmailId!, junkMailboxId);
    await addMailboxMembership(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      trashEmailId!,
      trashMailboxId,
    );

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });

    // The Inbox+Trash message is listed; the Inbox+Junk message is not.
    await expect(page.locator('.thread-list .thread-row', { hasText: trashSubject })).toHaveCount(1, {
      timeout: 15_000,
    });
    await expect(page.locator('.thread-list .thread-row', { hasText: junkSubject })).toHaveCount(0);

    // The Junk message is present in Spam (the herold role="junk" folder,
    // rendered as "Spam" -- see App.sidebar-junk.test.ts).
    await page.goto('/#/mail/folder/junk');
    await expect(page.locator('.thread-list .thread-row', { hasText: junkSubject })).toHaveCount(1, {
      timeout: 15_000,
    });
  });
});
