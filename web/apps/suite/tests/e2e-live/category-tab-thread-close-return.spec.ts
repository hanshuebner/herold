/**
 * category-tab-thread-close-return.spec.ts (re #495)
 *
 * Archiving a thread opened from a non-Primary category tab (e.g.
 * Promotions, Forums) must return the user to that tab (`/mail?tab=...`),
 * not drop back to bare `/mail` (Primary).
 *
 * Root cause (confirmed by a live `hashchange` trace during triage):
 * `ThreadToolbar.archive()` -> `leaveThread()` -> `navigateBackFromThread()`
 * correctly pushes the tab-qualified `router.lastListPath`. `MailView`'s
 * thread-membership auto-navigate-away `$effect` (the `re #29`/`re #294`
 * guard) then reruns -- via a microtask scheduled by the archive's own
 * optimistic store mutation, ahead of the `hashchange` task that would
 * update `router.current` -- and fires a second, redundant navigation to a
 * bare `folderHref(currentFolder)` with no `tab` parameter. That second
 * write lands last and wins. This spec is the acceptance test the ticket
 * asked for; the transition itself cannot be driven through a mocked
 * vitest harness (`MailView.navigate-away.test.ts`'s own docstring), so it
 * needs a real backend and real Svelte effect-flush timing.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to
 *                 deliver a message the dev instance's fake classifier
 *                 plugin (heroldfakeclassify) files under Promotions
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts \
 *       tests/e2e-live/category-tab-thread-close-return.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane, matching every other
 * spec in this directory: this needs the real classifier plugin, real JMAP
 * delivery and a real EventSource-driven client to exercise the exact
 * reactivity race the bug depends on.
 */

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, ALICE } from './live-helpers';

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

/** Log in, wipe the mailbox, seed a message whose subject the dev
 *  instance's fake classifier (`+promo` substring, see web/CLAUDE.md)
 *  files under the Promotions category, then reload. */
async function loginWithPromotionsMessage(
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
}

test.describe('closing a thread opened from a non-Primary category tab', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('archiving from the Promotions tab returns to /mail?tab=promotions, not bare /mail (re #495)', async ({
    page,
    request,
  }) => {
    const subject = 'Weekly deals +promo (category tab archive test)';
    await loginWithPromotionsMessage(page, request, subject, 'Category tab archive test body.');

    // The classifier plugin runs at delivery time; the Promotions tab
    // (with its unread badge) appears once categorisation has landed and
    // the client has synced it.
    const promotionsTab = page.locator('.tab-strip .tab', { hasText: 'Promotions' });
    await expect(promotionsTab).toBeVisible({ timeout: 15_000 });
    await promotionsTab.click();
    await expect(page).toHaveURL(/#\/mail\?tab=promotions$/);

    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
    await page.locator('.thread-list .thread-row .row-activate').first().click();
    await expect(page).toHaveURL(/#\/mail\/thread\//);
    await expect(page.locator('.thread-frame h1')).toHaveText(subject);

    await page.getByRole('button', { name: 'Archive' }).click();

    // The bug: the auto-navigate-away effect's redundant, bare-folder
    // navigation clobbers the tab-preserving one and the URL settles on
    // plain /mail (Primary) instead.
    await expect(page).toHaveURL(/#\/mail\?tab=promotions$/);
    await expect(promotionsTab).toHaveAttribute('aria-current', 'page');
  });
});
