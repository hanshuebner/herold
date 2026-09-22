/**
 * whole-mailbox-offer-scope.spec.ts (re #255)
 *
 * Browser-level acceptance gate for the whole-mailbox-selection banner
 * while a category tab (REQ-CAT-10..14) is active: no vitest mock of
 * `categorySettings`/`mail` can exercise the real classifier round trip
 * that populates `derivedCategories` and drives `showTabs`, so this
 * needs a live instance with the dev classifier fake to see the tab
 * strip and the banner's absence together.
 *
 * Fixed contract: while a category tab narrows the rendered list, the
 * whole-mailbox-selection banner (offer and active state alike) does not
 * appear -- no per-category conversation total or category-scoped
 * `Email/setByQuery` filter exists to honour a category-scoped offer, so
 * checking every rendered row of the Primary tab must not present a
 * "select all N in the mailbox" affordance that spans categories the
 * list does not show.
 *
 * Note on this spec's reach: in this scenario the checkbox only ever
 * selects the active tab's rendered ids (`effectiveListEmailIds`, re
 * #202), so the pre-#255 code's `shouldOfferWholeSet(mail.listEmails,
 * selected, ...)` check already failed its own "every loaded id is
 * selected" test once a second category held a message -- it happened to
 * withhold the banner here for the wrong reason (an incidental id
 * mismatch) rather than the right one (view-scoping). This spec pins the
 * *correct* reason going forward -- `!showTabs` gates the banner
 * directly -- and is the browser-level companion to
 * `MailView.tab-aware-select-all.test.ts`, which stubs
 * `listWholeMailboxSelected: true` directly to exercise the path this
 * live scenario cannot reach through ordinary clicks and does fail on
 * pre-#255 code.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts \
 *       tests/e2e-live/whole-mailbox-offer-scope.spec.ts
 */

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, ALICE } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;

/** Minimal SMTP client: EHLO, MAIL FROM, RCPT TO, DATA, QUIT. Accepts
 *  extra headers (In-Reply-To/References) so a reply can be threaded
 *  onto a prior Message-ID. Returns the Message-ID it sent. */
async function sendSmtp(
  addr: string,
  from: string,
  to: string,
  subject: string,
  body: string,
  extraHeaders: Record<string, string> = {},
): Promise<string> {
  const [host, portStr] = addr.split(':');
  const port = Number(portStr);
  const messageId = `<${Math.random().toString(36).slice(2)}@test.local>`;

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
        const extra = Object.entries(extraHeaders)
          .map(([k, v]) => `${k}: ${v}\r\n`)
          .join('');
        const msg =
          `From: ${from}\r\n` +
          `To: ${to}\r\n` +
          `Subject: ${subject}\r\n` +
          `Date: ${new Date().toUTCString()}\r\n` +
          `Message-ID: ${messageId}\r\n` +
          extra +
          `\r\n` +
          `${body}\r\n` +
          `.\r\n`;
        inData = true;
        socket.write(msg);
      }
    });
  });

  return messageId;
}

function rowCheckbox(page: Page) {
  return page.locator('.thread-list .thread-row .row-check');
}

test.describe('whole-mailbox-selection banner withheld on a category-scoped view (re #255)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test('Primary tab, one thread selected: no "select all in mailbox" offer, no active-state banner', async ({
    page,
    request,
  }) => {
    await login(page);
    await clearMailbox(page, request);

    // A two-message thread with no trigger word -- the dev classifier
    // fake (internal/testfakes/fakeclassify) assigns it Primary.
    const rootId = await sendSmtp(
      SMTP_ADDR!,
      'sender@example.com',
      ALICE,
      'Primary thread root',
      'First message of the thread.',
    );
    await sendSmtp(
      SMTP_ADDR!,
      'sender2@example.com',
      ALICE,
      'Re: Primary thread root',
      'Reply, folding into the same thread.',
      { 'In-Reply-To': rootId, References: rootId },
    );

    // A standalone message in a different category: "+promo" in the
    // subject makes the classifier fake assign $category-promotions.
    await sendSmtp(
      SMTP_ADDR!,
      'sender3@example.com',
      ALICE,
      'Deal of the day +promo',
      'A promotional message in a different category.',
    );

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });

    // Wait for the category tab strip to appear -- the classifier fake's
    // responses populate `derivedCategories` asynchronously after
    // delivery, which is what drives `showTabs`.
    await expect(page.locator('.tab-strip .tab', { hasText: 'Promotions' })).toBeVisible({
      timeout: 15_000,
    });

    // Primary is the default tab (no ?tab= param): exactly one thread row
    // (the two-message thread), the promo message hidden behind its own tab.
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row .thread-count')).toHaveText('2');

    // Select the one rendered conversation.
    await rowCheckbox(page).click();
    await expect(page.locator('.bulk-count')).toHaveText('1 selected');

    // The whole-mailbox-selection banner must not appear at all: no
    // "select all N in the mailbox" offer, no active-state banner --
    // there is no way to scope either to the Primary tab alone.
    await expect(page.locator('.whole-mailbox-banner')).toHaveCount(0);
    await expect(page.getByText(/in the mailbox/i)).toHaveCount(0);
  });

  test('a fully-loaded page (no further page) never offers "select all in the mailbox", even though the raw message total exceeds the conversation count', async ({
    page,
    request,
  }) => {
    // The reported defect's actual live trigger, reproduced without any
    // category tab: a two-message thread plus a standalone message give
    // Inbox a raw message total (3) larger than its conversation count
    // (2), while both conversations are already fully loaded
    // (`listHasMore` false -- no next page exists to reveal a third
    // conversation). Selecting every rendered row must not offer "select
    // all 3 messages in the mailbox": there is no further conversation
    // for that link to reveal.
    await login(page);
    await clearMailbox(page, request);

    const rootId = await sendSmtp(
      SMTP_ADDR!,
      'sender@example.com',
      ALICE,
      'Clean thread root',
      'First message.',
    );
    await sendSmtp(
      SMTP_ADDR!,
      'sender2@example.com',
      ALICE,
      'Re: Clean thread root',
      'Reply, folding into the same thread.',
      { 'In-Reply-To': rootId, References: rootId },
    );
    await sendSmtp(SMTP_ADDR!, 'sender3@example.com', ALICE, 'Standalone message', 'Body.');

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(2, { timeout: 15_000 });

    await page.locator('.list-toolbar .chooser .check-btn').click();
    await expect(page.locator('.bulk-count')).toHaveText('2 selected');

    await expect(page.locator('.whole-mailbox-banner')).toHaveCount(0);
    await expect(page.getByText(/in the mailbox/i)).toHaveCount(0);
  });
});
