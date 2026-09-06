/**
 * thread-back-navigation.spec.ts (re #294, re #29)
 *
 * The missing end-to-end acceptance test for MailView's thread-reader
 * auto-navigate-away `$effect`. That effect has now been patched three
 * times against a mocked/vitest-only harness that cannot exercise real
 * Svelte reactivity timing against a live backend (re #29 the original
 * bounce, re #88 the `confirmedFolderKey` cold-load guard in 6cf80678,
 * re #294 the reset-on-leave that keeps a Back-reached archived thread
 * from bouncing straight back). Per CLAUDE.md's fix-on-fix cap, the third
 * follow-up on one flow gets its acceptance test written before further
 * symptom-chasing; this spec is that test.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with deterministic messages before each test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/thread-back-navigation.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane (those run against
 * page.route() mocks with no backend), matching every other spec in this
 * directory: this needs a real JMAP backend so the archive action, the
 * EventSource-driven live sync, and the browser's own history stack all
 * behave the way they do for a real user.
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

test.describe('thread reader browser-history navigation', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('browser Back after archiving a thread returns to that thread\'s reader and it stays put (re #294)', async ({
    page,
    request,
  }) => {
    const subject = 'Archive back-nav test';
    const body = 'Archive back-nav test body.';
    await loginWithFreshInbox(page, request, subject, body);

    // Open the thread from the Inbox list.
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
    const threadUrl = page.url();

    // Wait for the message body to render -- the subject above comes from
    // list-view metadata already cached before the thread route ever
    // mounts, but the body comes from ThreadReader's own `loadThread`
    // Email/get fetch. A real user reads the body before deciding to
    // archive, so by the time they click Archive that fetch has long
    // settled; waiting for it here avoids archiving (and racing Back)
    // while it is still in flight, which would let its stale response
    // overwrite the archive's optimistic mailboxIds patch when it lands.
    await expect(page.locator('.thread-frame .body')).toContainText(body);

    // Archive it from the reader toolbar. Suite navigates back to the list.
    await page.getByRole('button', { name: 'Archive' }).click();
    await expect(page).toHaveURL(/#\/mail$/);

    // Press the browser's own Back button.
    await page.goBack();

    // Back must land on the archived thread's own route and render its
    // reader -- not skip past it (the history-stack half of re #294) and
    // not bounce straight back to the list the instant the route re-enters
    // the thread (the auto-navigate-away confirmedFolderKey half of #294).
    await expect(page).toHaveURL(threadUrl);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);

    // Give the auto-navigate-away effect a moment to (mis)fire on the
    // now-stillInFolder=false thread, then confirm the reader is still
    // showing -- a stale confirmedFolderKey guard bounces within a single
    // hashchange cycle, well under this margin.
    await page.waitForTimeout(500);
    await expect(page).toHaveURL(threadUrl);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);
  });

  test('a thread that leaves the current folder while being viewed still bounces to the list (re #29)', async ({
    page,
    request,
  }) => {
    const subject = 'Live-viewing bounce test';
    await loginWithFreshInbox(page, request, subject, 'Live-viewing bounce test body.');

    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);

    // Move the email out of the Inbox out-of-band (simulating another
    // client/device archiving it) while the reader stays open, mirroring
    // the original re #29 scenario (a Restore-from-Trash / Move-To-Mailbox
    // operation removing the thread from the folder being viewed).
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
    const inboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'inbox');
    const archiveId = await mailboxIdByRole(
      request,
      apiUrl,
      cookieHeader,
      mailAccountId,
      'archive',
    );
    // subject: queries route through the server's FTS index, which indexes
    // asynchronously after SMTP delivery -- poll rather than query once
    // (findEmailIdsBySubject's own doc comment; matches shift-click-
    // selection.spec.ts's usage).
    const emailIds = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, subject);
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
              [emailIds[0]!]: {
                [`mailboxIds/${inboxId}`]: null,
                [`mailboxIds/${archiveId}`]: true,
              },
            },
          },
          'u',
        ],
      ],
    );

    // The SPA's own EventSource push notices the mailbox change and
    // refreshes the open thread; the auto-navigate-away effect then fires
    // because it had already confirmed (on the initial render above) that
    // the thread WAS in the Inbox. Generous timeout for EventSource
    // propagation, not for a UI action.
    await expect(page).toHaveURL(/#\/mail$/, { timeout: 15_000 });
  });
});
