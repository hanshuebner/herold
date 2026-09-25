/**
 * toc-fragment-anchor.spec.ts (re #490, re #293)
 *
 * End-to-end acceptance spec for in-document TOC fragment links inside a
 * rendered HTML mail body (`sanitize.ts`'s `fragmentDocumentUrl` rewrite,
 * `HtmlBody.svelte`'s `srcdoc` iframe). #490 was filed against a real
 * bulk-newsletter whose "Themen im Newsletter" table of contents links to
 * `href="#2"`/`#3"`/`#4"`/`#6"` (four items) and `href="#8"`/`#9"`/`#10"`
 * (three items) -- but the sender's own body only carries seven legacy
 * `<a name="1">` .. `<a name="7">` targets, no `id` attributes at all. So
 * `#2`/`#3`/`#4`/`#6` resolve via the browser's own "an `<a name>` element
 * is a valid fragment target" fallback, while `#8`/`#9`/`#10` have no
 * target anywhere in the sender's HTML and can never scroll in ANY client
 * -- confirmed against the real message (`t3973.eml`) before writing this
 * spec: every `name`-resolvable link scrolled the reading pane correctly
 * on the current sanitizer, and every unresolvable one correctly did
 * nothing (no scroll, no navigation away from the thread). This spec
 * captures both halves of that observed contract as an automated,
 * CDP-trusted-click regression: a scripted `.click()` or a JS-dispatched
 * `MouseEvent` cannot stand in for it (`sanitize.test.ts` already covers
 * the href-rewrite string transform at the unit level; this is the part
 * that needs a live browser resolving a real anchor fragment).
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR - host:port of the instance's SMTP listener
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/toc-fragment-anchor.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` mocked CI lane, matching
 * every other spec in this directory.
 */

import { test, expect } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, ALICE } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;

test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

/**
 * Minimal SMTP client delivering a single-part `text/html` message: EHLO,
 * MAIL FROM, RCPT TO, DATA, QUIT. Mirrors quoted-history-fold.spec.ts's
 * `sendHtmlSmtp`.
 */
async function sendHtmlSmtp(
  addr: string,
  from: string,
  to: string,
  subject: string,
  html: string,
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
        const stuffed = html.replace(/\r\n\./g, '\r\n..');
        const msg =
          `From: ${from}\r\n` +
          `To: ${to}\r\n` +
          `Subject: ${subject}\r\n` +
          `Date: ${new Date().toUTCString()}\r\n` +
          `Message-ID: <${Math.random().toString(36).slice(2)}@test.local>\r\n` +
          `MIME-Version: 1.0\r\n` +
          `Content-Type: text/html; charset=utf-8\r\n` +
          `\r\n` +
          `${stuffed}\r\n` +
          `.\r\n`;
        inData = true;
        socket.write(msg);
      }
    });
  });
}

const RUN_TOKEN = Math.random().toString(36).slice(2, 8);
const SUBJECT = `TOC anchor corpus ${RUN_TOKEN} (re #490)`;

/**
 * Mirrors the real #490 newsletter's shape (own-numbered TOC hrefs against
 * legacy `<a name>` targets, plus a couple of hrefs with no target
 * anywhere) rather than the tidier `id`-based shape #293's own unit tests
 * use, since that mismatch is exactly what #490 was filed against.
 */
const TOC_HTML = `
<h1>Themen im Newsletter</h1>
<ol>
  <li><a href="#2">Sprung zwei</a></li>
  <li><a href="#3">Sprung drei</a></li>
  <li><a href="#4">Sprung vier</a></li>
  <li><a href="#99">Sprung kaputt</a></li>
</ol>
<div style="height:400px">Vorspann</div>
<a name="2"></a>
<h2>Abschnitt zwei</h2>
<div style="height:1600px">Fuellinhalt zwei</div>
<a name="3"></a>
<h2>Abschnitt drei</h2>
<div style="height:1600px">Fuellinhalt drei</div>
<a name="4"></a>
<h2>Abschnitt vier</h2>
<div style="height:1600px">Fuellinhalt vier</div>
`;

test.describe('TOC fragment anchors resolve through legacy <a name> targets (re #490)', () => {
  test('a name-resolvable link scrolls the reading pane; an unresolvable one does nothing', async ({
    page,
    request,
  }) => {
    test.setTimeout(60_000);

    await login(page);
    await clearMailbox(page, request);
    await sendHtmlSmtp(SMTP_ADDR!, 'sender@example.com', ALICE, SUBJECT, TOC_HTML);

    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
    await expect(page.locator('.thread-list .thread-row')).toHaveCount(1, { timeout: 15_000 });
    await page.locator('.thread-list .thread-row .row-activate').first().click();

    const frame = page.frameLocator('iframe[title="Message body"]');
    await expect(frame.locator('body')).toBeVisible();
    // Wait for the sanitized href rewrite (issue #293's fragmentDocumentUrl
    // path) so the click below lands once the iframe has actually settled,
    // not mid-layout.
    await expect(frame.getByRole('link', { name: 'Sprung zwei' })).toHaveAttribute(
      'href',
      'about:srcdoc#2',
    );

    const scrollContainer = page.locator('.scroll');

    // Resolvable case: href="#2" has no id="2" anywhere in the sender's
    // HTML, but the browser's own fragment-resolution algorithm falls back
    // to the legacy <a name="2"> target -- the click must scroll the
    // reading pane down to it via #293's native cross-frame mechanism.
    await expect(scrollContainer).toHaveJSProperty('scrollTop', 0);
    await frame.getByRole('link', { name: 'Sprung zwei' }).click();
    await expect
      .poll(async () => scrollContainer.evaluate((el) => el.scrollTop), { timeout: 5_000 })
      .toBeGreaterThan(100);

    // Unresolvable case: href="#99" matches neither an id nor an <a name>
    // anywhere in the sender's own HTML (mirroring #490's real #8/#9/#10
    // links) -- clicking it must neither scroll the reading pane nor
    // navigate the Suite away from the open thread.
    await scrollContainer.evaluate((el) => {
      el.scrollTop = 0;
    });
    await expect(scrollContainer).toHaveJSProperty('scrollTop', 0);
    const threadUrlBeforeClick = page.url();
    await frame.getByRole('link', { name: 'Sprung kaputt' }).click();
    await page.waitForTimeout(500);
    expect(await scrollContainer.evaluate((el) => el.scrollTop)).toBe(0);
    expect(page.url()).toBe(threadUrlBeforeClick);
    await expect(page.locator('.thread-frame h1')).toHaveText(SUBJECT);

    // The other two name-resolvable links scroll just as reliably.
    for (const [linkText, sectionText] of [
      ['Sprung drei', 'Abschnitt drei'],
      ['Sprung vier', 'Abschnitt vier'],
    ] as const) {
      await scrollContainer.evaluate((el) => {
        el.scrollTop = 0;
      });
      await frame.getByRole('link', { name: linkText }).click();
      await expect
        .poll(async () => scrollContainer.evaluate((el) => el.scrollTop), { timeout: 5_000 })
        .toBeGreaterThan(100);
      await expect(frame.getByRole('heading', { name: sectionText })).toBeInViewport();
    }
  });
});
