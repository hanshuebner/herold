/**
 * Shared helpers for e2e-live specs (re #202): specs that need a real
 * herold backend (scripts/dev-instance.sh) instead of page.route() mocks.
 * See playwright.live.config.ts for how to run these.
 */

import { expect, type Page, type APIRequestContext } from '@playwright/test';

export const ALICE = 'alice@example.local';
export const ALICE_PASSWORD = 'testpass123...';

export async function login(page: Page): Promise<void> {
  await page.goto('/#/mail');
  await page.locator('#email').fill(ALICE);
  await page.locator('#password').fill(ALICE_PASSWORD);
  await page.locator('.submit-btn').click();
  // Wait for the authenticated shell (compose button) rather than for rows
  // -- the inbox may be empty right after login (fresh account, or a prior
  // test's cleanup).
  await page.locator('button.compose').first().waitFor({ timeout: 15_000 });
}

/**
 * Fetch the current JMAP session + a cookie header usable for direct API
 * calls alongside the page's own authenticated requests (used to seed/clear
 * data out-of-band, simulating another client/device touching the account).
 */
export async function jmapSession(
  page: Page,
  request: APIRequestContext,
): Promise<{
  cookieHeader: string;
  mailAccountId: string;
  contactsAccountId: string;
  apiUrl: string;
}> {
  const cookies = await page.context().cookies();
  const cookieHeader = cookies.map((c) => `${c.name}=${c.value}`).join('; ');
  const base = new URL(page.url()).origin;
  const resp = await request.get(`${base}/.well-known/jmap`, {
    headers: { cookie: cookieHeader },
  });
  const session = await resp.json();
  return {
    cookieHeader,
    mailAccountId: session.primaryAccounts['urn:ietf:params:jmap:mail'],
    contactsAccountId: session.primaryAccounts['urn:ietf:params:jmap:contacts'],
    apiUrl: session.apiUrl,
  };
}

export async function jmapCall(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  using: string[],
  methodCalls: unknown[],
): Promise<Record<string, unknown>> {
  const resp = await request.post(apiUrl, {
    headers: { cookie: cookieHeader, 'content-type': 'application/json' },
    data: { using, methodCalls },
  });
  return resp.json();
}

/** Destroy every Email in the account so each test starts from an empty
 *  mailbox regardless of what earlier tests (or manual seeding) left behind. */
export async function clearMailbox(page: Page, request: APIRequestContext): Promise<void> {
  const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);
  const queryBody = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/query', { accountId: mailAccountId, filter: null, limit: 1000 }, 'q']],
  );
  const responses = queryBody.methodResponses as [string, { ids: string[] }, string][];
  const ids = responses[0]![1].ids;
  if (ids.length === 0) return;
  await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/set', { accountId: mailAccountId, destroy: ids }, 'd']],
  );
}

/** Destroy every Contact in the account so each test starts from an empty
 *  address book regardless of what earlier tests left behind. */
export async function clearContacts(page: Page, request: APIRequestContext): Promise<void> {
  const { cookieHeader, contactsAccountId, apiUrl } = await jmapSession(page, request);
  const queryBody = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:contacts'],
    [['Contact/query', { accountId: contactsAccountId, limit: 1000 }, 'q']],
  );
  const responses = queryBody.methodResponses as [string, { ids: string[] }, string][];
  const ids = responses[0]![1].ids;
  if (ids.length === 0) return;
  await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:contacts'],
    [['Contact/set', { accountId: contactsAccountId, destroy: ids }, 'd']],
  );
}

/** Create N deterministic contacts, in order, via JMAP `Contact/set`. */
export async function seedContacts(
  page: Page,
  request: APIRequestContext,
  names: string[],
): Promise<void> {
  const { cookieHeader, contactsAccountId, apiUrl } = await jmapSession(page, request);
  const create: Record<string, unknown> = {};
  names.forEach((name, i) => {
    create[`c${i}`] = { name: { full: name } };
  });
  await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:contacts'],
    [['Contact/set', { accountId: contactsAccountId, create }, 'c']],
  );
}

export function bulkCountText(page: Page) {
  return page.locator('.bulk-count');
}

/**
 * Find Email ids by an `Email/query` `subject` filter, retrying until at
 * least one match shows up. `subject` (like every other text-bearing
 * predicate) is routed through the server's FTS index
 * (`internal/protojmap/mail/email/query.go`'s `gatherCandidatesRaw`),
 * which indexes asynchronously after a message lands via SMTP -- a message
 * already rendered in the SPA's own (non-filtered, non-FTS) folder list can
 * still miss a `subject:` query issued moments later. Poll instead of
 * querying once so a live spec doesn't flake on that indexing race.
 */
export async function findEmailIdsBySubject(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  subject: string,
): Promise<string[]> {
  let ids: string[] = [];
  await expect
    .poll(
      async () => {
        const body = await jmapCall(
          request,
          apiUrl,
          cookieHeader,
          ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
          [['Email/query', { accountId: mailAccountId, filter: { subject } }, 'q']],
        );
        ids = (body.methodResponses as [string, { ids: string[] }, string][])[0]![1].ids;
        return ids.length;
      },
      { timeout: 10_000, message: `Email/query subject:"${subject}" never returned a match` },
    )
    .toBeGreaterThan(0);
  return ids;
}
