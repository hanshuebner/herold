/**
 * bulk-archive-undo.spec.ts (re #515)
 *
 * bulkArchive/bulkDelete previously called `#summarizeBulk` with no undo
 * callback, so archiving/deleting from the list-selection toolbar, the
 * thread toolbar, or send-and-archive showed a toast with no Undo action
 * even though the single-message path (archiveEmail/deleteEmail) always
 * offered one. This spec exercises the ticket's Acceptance directly
 * against a real herold backend: archive/delete, click Undo, confirm the
 * row is back in the UI AND that the server itself (via `Email/get`) shows
 * the prior mailbox membership restored.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with a deterministic message before each test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/bulk-archive-undo.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every
 * other spec in this directory: this needs a real JMAP backend so the
 * server-side mailbox membership after Undo is what the assertion
 * actually checks, not a page.route() mock's echo of the request.
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

/** The current `mailboxIds` membership set for one Email, via `Email/get`. */
async function emailMailboxIds(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  emailId: string,
): Promise<Record<string, boolean>> {
  const body = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [
      [
        'Email/get',
        { accountId: mailAccountId, ids: [emailId], properties: ['mailboxIds'] },
        'g',
      ],
    ],
  );
  const list = (body.methodResponses as [string, { list: { mailboxIds: Record<string, boolean> }[] }, string][])[0]![1]
    .list;
  if (list.length === 0) throw new Error(`Email/get returned no record for ${emailId}`);
  return list[0]!.mailboxIds;
}

/** Seed one message to Alice's Inbox with a unique subject and return its
 *  JMAP Email id. */
async function seedInboxMessage(
  page: Page,
  request: APIRequestContext,
  subject: string,
  body: string,
): Promise<{ emailId: string; mailAccountId: string; apiUrl: string; cookieHeader: string }> {
  await sendSmtp(SMTP_ADDR!, 'sender@example.local', ALICE, subject, body);
  const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
  const [emailId] = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, subject);
  return { emailId: emailId!, mailAccountId, apiUrl, cookieHeader };
}

/** The toast's Undo button, scoped to a toast whose body contains `text`. */
function undoButton(page: Page, text: string) {
  return page.locator('.toast', { hasText: text }).getByRole('button', { name: 'Undo' });
}

test.describe('bulk archive/delete Undo (re #515)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('list-selection archive: Undo restores the row and the server shows Inbox membership again', async ({
    page,
    request,
  }) => {
    const subject = `Issue515 list-archive-undo ${Date.now()}`;
    await login(page);
    await clearMailbox(page, request);
    const { emailId, mailAccountId, apiUrl, cookieHeader } = await seedInboxMessage(
      page,
      request,
      subject,
      'List-selection archive-undo e2e body.',
    );
    const inboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'inbox');
    const archiveId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'archive');

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    const row = page.locator('.thread-list .thread-row', { hasText: subject });
    await expect(row).toHaveCount(1, { timeout: 15_000 });

    await row.locator('.row-check').click();
    await page.getByRole('button', { name: 'Archive', exact: true }).click();

    await expect(page.locator('.toast', { hasText: 'archived' })).toBeVisible({ timeout: 5_000 });
    await expect
      .poll(
        async () => emailMailboxIds(request, apiUrl, cookieHeader, mailAccountId, emailId),
        { timeout: 10_000, message: 'archive must move the Email to Archive-only' },
      )
      .toEqual({ [archiveId]: true });
    await expect(row).toHaveCount(0);

    await undoButton(page, 'archived').click();

    await expect(row).toHaveCount(1, { timeout: 5_000 });
    await expect
      .poll(
        async () => emailMailboxIds(request, apiUrl, cookieHeader, mailAccountId, emailId),
        { timeout: 10_000, message: 'undo must restore Inbox-only membership on the server' },
      )
      .toEqual({ [inboxId]: true });
  });

  test('thread-toolbar archive: Undo restores the row and the server shows Inbox membership again', async ({
    page,
    request,
  }) => {
    const subject = `Issue515 thread-archive-undo ${Date.now()}`;
    await login(page);
    await clearMailbox(page, request);
    const { emailId, mailAccountId, apiUrl, cookieHeader } = await seedInboxMessage(
      page,
      request,
      subject,
      'Thread-toolbar archive-undo e2e body.',
    );
    const inboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'inbox');
    const archiveId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'archive');

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    const row = page.locator('.thread-list .thread-row', { hasText: subject });
    await expect(row).toHaveCount(1, { timeout: 15_000 });
    await row.locator('.row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);

    await page.getByRole('button', { name: 'Archive', exact: true }).click();

    // ThreadToolbar's archive handler navigates back to the list
    // immediately after calling bulkArchive (navigateBackFromThread()), so
    // the toast and its Undo button render over the list, not the thread.
    await expect(page).not.toHaveURL(/#\/mail\/thread\//, { timeout: 5_000 });
    await expect(page.locator('.toast', { hasText: 'archived' })).toBeVisible({ timeout: 5_000 });
    await expect
      .poll(
        async () => emailMailboxIds(request, apiUrl, cookieHeader, mailAccountId, emailId),
        { timeout: 10_000, message: 'archive must move the Email to Archive-only' },
      )
      .toEqual({ [archiveId]: true });

    await undoButton(page, 'archived').click();

    await expect(row).toHaveCount(1, { timeout: 5_000 });
    await expect
      .poll(
        async () => emailMailboxIds(request, apiUrl, cookieHeader, mailAccountId, emailId),
        { timeout: 10_000, message: 'undo must restore Inbox-only membership on the server' },
      )
      .toEqual({ [inboxId]: true });
  });

  test('list bulk delete: Undo drops Trash and restores the previous membership', async ({
    page,
    request,
  }) => {
    const subject = `Issue515 list-delete-undo ${Date.now()}`;
    await login(page);
    await clearMailbox(page, request);
    const { emailId, mailAccountId, apiUrl, cookieHeader } = await seedInboxMessage(
      page,
      request,
      subject,
      'List bulk-delete undo e2e body.',
    );
    const inboxId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'inbox');
    const trashId = await mailboxIdByRole(request, apiUrl, cookieHeader, mailAccountId, 'trash');

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    const row = page.locator('.thread-list .thread-row', { hasText: subject });
    await expect(row).toHaveCount(1, { timeout: 15_000 });

    await row.locator('.row-check').click();
    await page.getByRole('button', { name: 'Delete', exact: true }).click();

    await expect(page.locator('.toast', { hasText: 'deleted' })).toBeVisible({ timeout: 5_000 });
    await expect
      .poll(
        async () => emailMailboxIds(request, apiUrl, cookieHeader, mailAccountId, emailId),
        { timeout: 10_000, message: 'delete must move the Email to Trash-only' },
      )
      .toEqual({ [trashId]: true });
    await expect(row).toHaveCount(0);

    await undoButton(page, 'deleted').click();

    await expect(row).toHaveCount(1, { timeout: 5_000 });
    await expect
      .poll(
        async () => emailMailboxIds(request, apiUrl, cookieHeader, mailAccountId, emailId),
        { timeout: 10_000, message: 'undo must drop Trash and restore the prior Inbox-only membership' },
      )
      .toEqual({ [inboxId]: true });
  });
});
