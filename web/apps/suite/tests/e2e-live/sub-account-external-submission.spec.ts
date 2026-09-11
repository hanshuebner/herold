/**
 * e2e-live: a separated identity configured for external SMTP submission
 * sends through that endpoint from within its sub-account scope (issue
 * #212 acceptance bullet 4, REQ-MAIL-SUB-08).
 *
 * Requires the dev instance to have been started with BOTH
 * HEROLD_DEV_SUB_ACCOUNTS=1 (seeds the deterministic identity id 800101,
 * vorsitz@classic-computing.example, ready to separate) AND
 * HEROLD_DEV_EXTERNAL_SUBMISSION=1 (starts heroldfakesmtp and passes
 * its address to `dev seed-separable-identity --sink-addr`, so the
 * seeded identity's external submission config points at the sink).
 * Skips itself when FAKESMTP_HTTP_ADDR is not set in the environment
 * (scripts/dev-instance.sh prints it to stdout only when the sink
 * started) rather than failing, so this spec is safe to include in a
 * run against an instance that did not enable those flags.
 */

import { test, expect } from '@playwright/test';
import { login } from './live-helpers';

const FAKESMTP_HTTP_ADDR = process.env.FAKESMTP_HTTP_ADDR;
const SEPARABLE_IDENTITY_ID = '800101';
const SEPARABLE_IDENTITY_EMAIL = 'vorsitz@classic-computing.example';

test.skip(
  !FAKESMTP_HTTP_ADDR,
  'requires the dev instance started with HEROLD_DEV_SUB_ACCOUNTS=1 HEROLD_DEV_EXTERNAL_SUBMISSION=1 (FAKESMTP_HTTP_ADDR not set)',
);

test('separated identity with external submission sends through the sink from its sub-account scope', async ({
  page,
  request,
}) => {
  // Several real round trips (separate, session-descriptor poll, scoped
  // navigation, compose, send, sink poll) push past the 30s default.
  test.setTimeout(60_000);
  await login(page);

  // Separate the seeded identity.
  await page.goto('/#/settings/identities/' + SEPARABLE_IDENTITY_ID);
  await page.getByTestId('identity-edit-separate-btn').click();
  await page.getByRole('button', { name: 'Separate identity' }).click();
  // The separate action navigates back to /settings/account on success --
  // wait for that as the signal the server-side move committed before
  // discovering the new sub-account id below.
  await expect(page).toHaveURL(/#\/settings\/account/);

  // Discover the new sub-account id from the session descriptor (the
  // switcher/Accounts section derive it the same way).
  const cookies = await page.context().cookies();
  const cookieHeader = cookies.map((c) => `${c.name}=${c.value}`).join('; ');
  const base = new URL(page.url()).origin;
  let subAccountId = '';
  await expect
    .poll(
      async () => {
        const resp = await request.get(`${base}/.well-known/jmap`, {
          headers: { cookie: cookieHeader },
        });
        const session = await resp.json();
        const primary = new Set(Object.values(session.primaryAccounts) as string[]);
        const ids = Object.keys(session.accounts).filter((id) => !primary.has(id));
        subAccountId = ids[0] ?? '';
        return subAccountId;
      },
      { timeout: 15_000, message: 'the new sub-account never appeared in the session descriptor' },
    )
    .not.toBe('');

  // Scope into the sub-account and compose from there.
  await page.goto(`/#/account/${subAccountId}`);
  await page.getByTestId('scoped-account-title').waitFor({ timeout: 15_000 });
  const recipient = `dest-${Date.now()}@remote.test`;
  await page.locator('button.compose-btn').click();
  await page.locator('[placeholder="recipient@example.com"]').first().fill(recipient);
  await page.locator('label:has-text("Subject") input').fill('sub-account external submission e2e');
  await page.locator('.ProseMirror').fill('sub-account e2e body');
  await page.getByTestId('compose-send').click();

  // Send() either closes the compose window (success) or leaves it open
  // with a visible error banner (e.g. a serverFail from a mis-scoped
  // identity, re #212 CI flake job 12019, root-caused to the compose
  // store's identity fallback and now guarded there). Assert this
  // explicitly, via Playwright's own retrying assertion (bounded, no
  // fixed sleep), so a regression here fails fast with an attributable
  // message instead of the opaque "message never arrived at the sink"
  // timeout below.
  const composeDialog = page.locator('div.modal[role="dialog"]');
  try {
    await expect(composeDialog).toHaveCount(0, { timeout: 10_000 });
  } catch (err) {
    const alert = composeDialog.locator('p.error[role="alert"]');
    if ((await alert.count()) > 0) {
      throw new Error(`compose send failed: ${await alert.first().innerText()}`);
    }
    throw err;
  }

  // Confirm arrival at the fake SMTP sink, sent from the separated
  // identity's own address (not this principal's own primary address --
  // that distinction is the acceptance criterion).
  await expect
    .poll(
      async () => {
        const resp = await request.get(`http://${FAKESMTP_HTTP_ADDR}/messages`);
        const messages = (await resp.json()) as Array<{ mail_from: string; rcpt_to: string[] }>;
        return messages.some(
          (m) => m.mail_from === SEPARABLE_IDENTITY_EMAIL && m.rcpt_to.includes(recipient),
        );
      },
      { timeout: 15_000, message: 'message never arrived at the fake SMTP sink' },
    )
    .toBe(true);
});
