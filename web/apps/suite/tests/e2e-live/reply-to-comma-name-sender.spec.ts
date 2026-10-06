/**
 * e2e-live (re #510): replying to a message whose From display name
 * contains a comma must populate the To field with that one recipient,
 * not leave it empty.
 *
 * Pre-fix, `computeReplyTo`'s sender fallback rendered the From address
 * via `addressToString` without RFC 5322 quoting the display name, then
 * fed the resulting string through `tryCommit` (the same tokenizer the
 * chip fields use) to seed the reply composer's To chips. `tryCommit`
 * treats an unquoted comma as a separator and stops at the unparseable
 * leading token, so the reply opened with zero To chips -- exactly the
 * production report (message 4217, From `"Surname, Firstname"
 * <User@example.org>`): replying opened the composer with an empty To
 * field.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL   - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR   - host:port of the instance's SMTP listener, used to
 *                 deliver the comma-display-name sender's message
 */

import { test, expect } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, jmapSession, findEmailIdsBySubject } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;

/**
 * Minimal SMTP client: EHLO, MAIL FROM, RCPT TO, DATA, QUIT. Takes the
 * envelope addresses and the header `From`/`To` values separately so a
 * display name (which may contain RFC 5322 specials) can be placed in
 * the header without corrupting the bare-address envelope commands.
 */
async function sendSmtp(
  addr: string,
  envelopeFrom: string,
  envelopeTo: string,
  headerFrom: string,
  headerTo: string,
  subject: string,
  body: string,
): Promise<void> {
  const [host, portStr] = addr.split(':');
  const port = Number(portStr);

  await new Promise<void>((resolve, reject) => {
    const socket = net.createConnection({ host, port: Number(port) });
    let buf = '';
    const steps = [
      `EHLO test.local\r\n`,
      `MAIL FROM:<${envelopeFrom}>\r\n`,
      `RCPT TO:<${envelopeTo}>\r\n`,
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
          `From: ${headerFrom}\r\n` +
          `To: ${headerTo}\r\n` +
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

test.describe('Reply to a comma-display-name sender populates To (re #510)', () => {
  test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

  test.beforeEach(async ({ page, request }) => {
    await login(page);
    await clearMailbox(page, request);
    await page.reload();
    await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
  });

  test('Reply pre-fills the To chip with the sender name and address intact', async ({
    page,
    request,
  }) => {
    test.setTimeout(30_000);
    const { mailAccountId, apiUrl, cookieHeader } = await jmapSession(page, request);

    const senderEmail = 'surname@foreign.example';
    const senderName = 'Surname, Firstname';
    const subject = `reply comma name sender e2e ${Date.now()}`;

    await sendSmtp(
      SMTP_ADDR!,
      senderEmail,
      'alice@example.local',
      `"${senderName}" <${senderEmail}>`,
      'alice@example.local',
      subject,
      'This is a test message for the reply-to-comma-name regression.',
    );

    const ids = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, subject);
    expect(ids.length).toBeGreaterThan(0);

    await page.reload();
    await expect(page.locator('.thread-list .thread-row', { hasText: subject })).toHaveCount(1, {
      timeout: 15_000,
    });
    await page
      .locator('.thread-list .thread-row', { hasText: subject })
      .locator('.row-activate')
      .click();
    // The always-visible Reply/Reply All/Forward strip pinned to the
    // bottom of the thread reader (ThreadReplyBar.svelte) -- not the
    // per-message hover-reveal "Reply" control, whose aria-label also
    // matches a loose substring search.
    await page.locator('.reply-bar').getByRole('button', { name: 'Reply', exact: true }).click();

    // Reply opens the inline composer within the thread view (not the
    // floating modal, which is reserved for a pop-out / fresh compose).
    const composeArea = page.getByTestId('inline-composer');
    await expect(composeArea).toBeVisible({ timeout: 10_000 });

    // Pre-fix this was empty -- zero chips and an empty recipient buffer.
    await expect(composeArea.locator('.recipient-field .chip')).toHaveCount(1);
    const chip = composeArea.locator('.recipient-field .chip-label', { hasText: senderName });
    await expect(chip).toBeVisible();
    await expect(chip).toHaveAttribute('title', senderEmail);
  });
});
