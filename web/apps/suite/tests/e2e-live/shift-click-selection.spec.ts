/**
 * shift-click-selection.spec.ts (re #202)
 *
 * The browser-level acceptance gate that four prior fix attempts skipped:
 * every previous pass verified the selection Set (a vitest mock of the
 * store, or the store's own unit tests) and never asserted what a real
 * user's shift-click actually leaves checked in the DOM. This spec drives
 * a genuine herold backend (no page.route() mocks) and a genuine,
 * CDP-trusted Shift-modified click, then reads the `.checked` DOM
 * property of every row checkbox plus the rendered toolbar count text --
 * the two planes every prior attempt left unasserted.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to seed
 *                 the inbox with deterministic messages before each test
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` CI lane (those run against
 * page.route() mocks with no backend); this spec is a manual/agent gate
 * run against an ephemeral instance because it needs a real JMAP backend
 * and real SMTP delivery.
 */

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, jmapSession, jmapCall, bulkCountText, ALICE } from './live-helpers';

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
        // Response to the terminating "." after DATA payload.
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
        // This is the 354 response to DATA; send the message body.
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

/** Seed N deterministic inbox messages, newest-subject-last (SMTP order is
 *  preserved by receivedAt so the last-sent message renders as row 0 / top
 *  of the folder list, newest-first). */
async function seedInbox(names: string[]): Promise<void> {
  if (!SMTP_ADDR) throw new Error('SMTP_ADDR env var is required for this spec');
  for (let i = 0; i < names.length; i++) {
    await sendSmtp(
      SMTP_ADDR,
      `sender${i}@example.com`,
      ALICE,
      `Shift-click test ${i + 1} - ${names[i]}`,
      `Body of message ${i + 1}.`,
    );
    // Small delay so receivedAt ordering is stable and distinct.
    await new Promise((r) => setTimeout(r, 50));
  }
}

/** Log in, wipe the mailbox, seed fresh deterministic messages, then reload
 *  the folder list so the seeded messages are what's on screen. */
async function loginWithFreshInbox(
  page: Page,
  request: APIRequestContext,
  names: string[],
): Promise<void> {
  await login(page);
  await clearMailbox(page, request);
  await seedInbox(names);
  await page.reload();
  await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  await expect(page.locator('.thread-list .thread-row')).toHaveCount(names.length, {
    timeout: 15_000,
  });
}

function rowCheckbox(page: Page, name: string) {
  return page.locator('.thread-list .thread-row', { hasText: name }).locator('.row-check');
}

test.describe('shift-click range selection (mail list)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('plain click + real Shift-modified click checks the inclusive range and the count matches', async ({
    page,
    request,
  }) => {
    const names = ['Alpha', 'Bravo', 'Charlie', 'Delta', 'Echo'];
    await loginWithFreshInbox(page, request, names);

    // Plain click on the top row (Echo, most recently sent) sets the anchor.
    await rowCheckbox(page, 'Echo').click();

    // Real Shift-modified click on the bottom row (Alpha). Playwright's
    // `modifiers` option dispatches via CDP Input.dispatchMouseEvent with
    // the Shift bit set -- a trusted event, not a page-context synthetic
    // MouseEvent -- so it exercises the browser's native checkbox
    // activation/click pipeline the way a real user's shift-click would.
    await rowCheckbox(page, 'Alpha').click({ modifiers: ['Shift'] });

    // Every row from Echo (top, anchor) to Alpha (bottom, shift-clicked)
    // inclusive must render a genuinely checked checkbox. `toBeChecked()`
    // polls the live `.checked` DOM property (Playwright's auto-retrying
    // assertion) rather than reading it once -- the fix settles the
    // checkbox's state a macrotask after the click (see
    // `handleRowCheckboxClick`), imperceptible to a real user but a race
    // against a single unretried read.
    for (const name of names) {
      await expect(rowCheckbox(page, name), `row "${name}" checked state`).toBeChecked();
    }

    // The toolbar count must equal the number of rendered checked rows --
    // not merely the size of an internal Set that may have drifted from
    // what's on screen.
    await expect(bulkCountText(page)).toHaveText(`${names.length} selected`);

    await expect(page.locator('.thread-list .row-check:checked')).toHaveCount(names.length);
  });

  test('rows outside the shift-click range stay unchecked', async ({ page, request }) => {
    const names = ['One', 'Two', 'Three', 'Four', 'Five', 'Six'];
    await loginWithFreshInbox(page, request, names);

    // Anchor at "Five" (second from bottom), shift-click "Two" (second
    // from top) -- range excludes "Six" (top) and "One" (bottom).
    await rowCheckbox(page, 'Five').click();
    await rowCheckbox(page, 'Two').click({ modifiers: ['Shift'] });

    for (const name of ['Two', 'Three', 'Four', 'Five']) {
      await expect(rowCheckbox(page, name), `row "${name}"`).toBeChecked();
    }
    for (const name of ['Six', 'One']) {
      await expect(rowCheckbox(page, name), `row "${name}"`).not.toBeChecked();
    }

    await expect(bulkCountText(page)).toHaveText('4 selected');
    await expect(page.locator('.thread-list .row-check:checked')).toHaveCount(4);
  });

  test('a background refresh that drops a selected id reconciles the toolbar count', async ({
    page,
    request,
  }) => {
    const names = ['Uno', 'Dos', 'Tres', 'Cuatro', 'Cinco'];
    await loginWithFreshInbox(page, request, names);

    await rowCheckbox(page, 'Cinco').click();
    await rowCheckbox(page, 'Uno').click({ modifiers: ['Shift'] });
    await expect(bulkCountText(page)).toHaveText(`${names.length} selected`);

    // Delete one of the selected messages out-of-band (a second JMAP
    // session, simulating another client/device) without going through
    // the SPA's own bulk-action path -- the store's `listSelectedIds`
    // never learns about this until the next refresh.
    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);

    const queryBody = await jmapCall(
      request,
      apiUrl,
      cookieHeader,
      ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
      [['Email/query', { accountId: mailAccountId, filter: { subject: 'Tres' } }, 'q']],
    );
    const idsToDelete: string[] = (
      queryBody.methodResponses as [string, { ids: string[] }, string][]
    )[0]![1].ids;
    expect(idsToDelete.length).toBeGreaterThan(0);

    await jmapCall(
      request,
      apiUrl,
      cookieHeader,
      ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
      [['Email/set', { accountId: mailAccountId, destroy: idsToDelete }, 'd']],
    );

    // Trigger the SPA's own refresh button (`button.refresh`, the
    // circular-arrow icon in the list header) -- an ordinary user action,
    // not a store-internal hack -- so listEmailIds drops the deleted id
    // while listSelectedIds (populated before the deletion) still holds it
    // unless the fix reconciles it.
    await page.locator('button.refresh').click();

    await expect(page.locator('.thread-list .thread-row')).toHaveCount(names.length - 1, {
      timeout: 15_000,
    });

    const renderedChecked = await page.locator('.thread-list .row-check:checked').count();
    const countText = await bulkCountText(page).textContent();
    const countNum = Number((countText ?? '').match(/\d+/)?.[0] ?? '-1');

    expect(countNum, 'toolbar count after background refresh').toBe(renderedChecked);
    expect(countNum).toBeLessThanOrEqual(names.length - 1);
  });
});
