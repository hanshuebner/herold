/**
 * e2e-live: the Sieve-filter settings panel (SieveForm.svelte) loads and
 * saves against a real herold backend (re #463). The regression this
 * guards against is a client/server JMAP method-name mismatch (the client
 * dispatched `SieveScript/get` and `SieveScript/set`; the server registers
 * `Sieve/get` and `Sieve/set`) -- a mocked-transport unit test would
 * happily assert against whichever name the client used and never catch
 * that the server does not recognise it. Driving the real dispatcher
 * through scripts/dev-instance.sh is the cheapest level that actually
 * exercises the method-name and response-shape contract between the two
 * sides.
 *
 * This does not assert that a saved script's body comes back into the
 * editor on a later visit -- that leg is blocked by a separate, already
 * broken-independently-of-this-fix server defect (#465: Sieve/get's
 * blobId does not resolve through the blob-download endpoint at all, for
 * any account, from the very first save onward). Persistence is checked
 * directly against Sieve/get instead of through the UI's (currently
 * unusable) reload path.
 */

import { test, expect } from '@playwright/test';
import { login, jmapSession, jmapCall } from './live-helpers';

test('Sieve-filter panel loads and saves a script', async ({ page, request }) => {
  await login(page);

  await page.goto('/#/settings/mail');

  // The panel must reach its loaded state without an unknownMethod (or
  // any other) error -- this is the symptom from the bug report.
  const scriptField = page.getByTestId('sieve-script');
  await expect(scriptField).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId('sieve-error')).toHaveCount(0);

  const marker = `# e2e marker ${Date.now()}`;
  const script = `require ["fileinto"];\n${marker}\nif header :contains "subject" "e2e" {\n  fileinto "INBOX";\n}\n`;
  await scriptField.fill(script);
  await page.getByTestId('sieve-save').click();

  await expect(page.getByText('Sieve script saved')).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId('sieve-error')).toHaveCount(0);

  // Confirm the save actually persisted server-side (not just local
  // component state) via a direct Sieve/get call -- Sieve/get's response
  // carries no body field, so this checks presence/isActive/state rather
  // than the script text itself.
  const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
  const getBody = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:sieve'],
    [['Sieve/get', { accountId: mailAccountId, ids: null }, 'g']],
  );
  const [, getArgs] = (getBody.methodResponses as [string, { list: { isActive: boolean }[] }, string][])[0]!;
  expect(getArgs.list).toHaveLength(1);
  expect(getArgs.list[0]!.isActive).toBe(true);
});
