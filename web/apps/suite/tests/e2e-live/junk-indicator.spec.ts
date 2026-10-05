/**
 * junk-indicator.spec.ts (re #509)
 *
 * A message filed to Junk, opened from search (not from the Spam folder
 * view), must still show the Not spam action, hide Report spam / Report
 * phishing, and carry the thread-view Junk indicator chip -- regardless
 * of which folder/search/notification it was opened from.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with a deterministic message before the test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/junk-indicator.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend driving
 * Email/query's `inMailbox`/`notInMailbox` filters, not a page.route() mock.
 */

import { test, expect, type APIRequestContext } from '@playwright/test';
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

test.describe('thread-view Junk indicator opened from search (re #509)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('a message filed to Junk, opened from search, shows Not spam, hides Report spam/phishing, and shows the Junk chip', async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    // Single-word subject: the Suite's search tokenizer (tokenize() in
    // store.svelte.ts's search-query.ts) only treats a "quoted phrase" as
    // one token when the whole token starts with the opening quote --
    // `subject:"multi word"` splits back into separate words at the
    // spaces. A single-word subject sidesteps that entirely.
    const subject = `Issue509JunkIndicator${stamp}`;

    await login(page);
    await clearMailbox(page, request);

    await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, subject, 'Files to Junk for re #509.');

    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const [emailId] = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, subject);
    const junkMailboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'junk');

    // File the message to Junk (re #509's "files a message to Junk"):
    // replace mailboxIds wholesale, matching reportSpam's own patch shape
    // (store.svelte.ts's reportSpam), and set the $junk keyword.
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
            update: {
              [emailId!]: { mailboxIds: { [junkMailboxId]: true }, 'keywords/$junk': true },
            },
          },
          'u',
        ],
      ],
    );

    // Wait for the move to be reflected in the FTS/query index before
    // driving the UI (REQ-SRC-06's `in:junk` scoping combined with a
    // `subject:` match both resolve through the same asynchronously-
    // indexed path findEmailIdsBySubject above already had to poll for,
    // and the move to Junk is itself a fresh write the index must catch
    // up with). Polling the JMAP filter directly -- the same shape the
    // Suite's own search builds for `in:junk subject:<word>` -- is a
    // reliable, fast wait; looping the UI's Enter-driven navigation
    // instead is unreliable because re-submitting an unchanged query can
    // be a router no-op.
    await expect
      .poll(
        async () => {
          const body = await jmapCall(
            request,
            apiUrl,
            cookieHeader,
            ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
            [
              [
                'Email/query',
                {
                  accountId: mailAccountId,
                  filter: {
                    operator: 'AND',
                    conditions: [{ inMailbox: junkMailboxId }, { subject }],
                  },
                },
                'q',
              ],
            ],
          );
          const ids = (body.methodResponses as [string, { ids: string[] }, string][])[0]![1].ids;
          return ids.length;
        },
        { timeout: 20_000, message: `index never caught up with the Junk move for "${subject}"` },
      )
      .toBeGreaterThan(0);

    // Open it from search, not from the Spam folder view. REQ-SRC-06: the
    // default search scope excludes Junk (and Trash), so the query needs
    // the explicit `in:junk` opt-in -- the same reason this ticket exists
    // at all (a Junk-filed message is easy to lose track of).
    const searchQuery = `in:junk subject:${subject}`;
    await page.locator('input[type="search"]').fill(searchQuery);
    await page.locator('input[type="search"]').press('Enter');
    const row = page.locator('.thread-list .thread-row', { hasText: subject });
    await expect(row).toHaveCount(1, { timeout: 15_000 });
    await row.click();

    await expect(page.locator('[data-testid="thread-junk-indicator"]')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('button[aria-label="Not spam"]')).toBeVisible();
    await expect(page.locator('button[aria-label="Report spam"]')).toHaveCount(0);
    await expect(page.locator('button[aria-label="Report phishing"]')).toHaveCount(0);
  });
});
