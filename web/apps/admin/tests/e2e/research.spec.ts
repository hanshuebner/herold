/**
 * research.spec.ts — Message-research view (re #143)
 *
 * Covers:
 *   - Page renders with heading "Message research" and filter form,
 *     with no subject filter field (re #143, maintainer finding #4)
 *   - "received" source entries render source badge, envelope, ingest
 *     path, delivered-to principal, disposition, spam verdict
 *   - "received" entries list every current mailbox with Junk-attributed
 *     ones marked (re #143, maintainer finding #1)
 *   - "received" entries with an unrecorded disposition show an explicit
 *     reason: the ingest source when known, "row predates recording"
 *     otherwise
 *   - "smtp_event" reject/defer entries render action, outcome badge,
 *     message, and the message reference (never under "Recipient")
 *   - "smtp.accept" entries render sender/recipients from metadata under
 *     labelled fields, and the blob reference under "Message reference"
 *     (re #143, maintainer finding #3)
 *   - "send_outcome" entries render with mail_from/rcpt_to, state badge,
 *     attempts/error
 *   - Sender filter sends the sender query param
 *   - Recipient filter sends the recipient query param
 *   - Date-range filters send date_from / date_to
 *   - Load-more sends before_us cursor and appends entries
 *   - Reset clears filters and reloads without params
 */

import { test, expect } from '@playwright/test';
import { installAdminSession } from './fixtures/auth';

// Sample data covering all three source types.

const RECEIVED_HIT = {
  source: 'received',
  at: new Date(Date.now() - 120_000).toISOString(),
  principal_id: 1,
  principal_email: 'alice@example.local',
  disposition: 'delivered_inbox',
  ingest_source: 'smtp',
  ingest_source_ref: '',
  mailboxes: [{ name: 'INBOX', is_junk: false }],
  mailbox_name: 'INBOX',
  is_junk: false,
  spam_verdict: 'ham',
  spam_confidence: 0.95,
  envelope: {
    from: 'kunde@example.com',
    to: 'alice@example.local',
    cc: '',
    bcc: '',
    reply_to: '',
    message_id: '<msg-001@example.com>',
    in_reply_to: '',
    references: '',
    date: new Date(Date.now() - 120_000).toISOString(),
  },
};

// Import-path received hit: several current mailboxes (one Junk-attributed),
// disposition not recorded (import path does not classify yet), ingest
// source imap-import with the import account as ref.
const MULTI_MAILBOX_HIT = {
  source: 'received',
  at: new Date(Date.now() - 180_000).toISOString(),
  principal_id: 1,
  principal_email: 'vorsitz@classic-computing.de',
  disposition: '',
  ingest_source: 'imap-import',
  ingest_source_ref: 'mail.classic-computing.de',
  mailboxes: [
    { name: 'Archive', is_junk: false },
    { name: 'Spam', is_junk: true },
    { name: 'vorsitz@classic-computing.de', is_junk: false },
  ],
  mailbox_name: 'Archive',
  is_junk: true,
  envelope: {
    from: 'sender@copperalliance.org.uk',
    to: 'vorsitz@classic-computing.de',
    cc: '',
    bcc: '',
    reply_to: '',
    message_id: '<4b0f742404bb4031b92faf582a8a49df@copperalliance.org.uk>',
    in_reply_to: '',
    references: '',
  },
};

// Row predating the disposition/ingest-source columns: neither is recorded.
const PREDATES_HIT = {
  source: 'received',
  at: new Date(Date.now() - 200_000).toISOString(),
  principal_id: 1,
  principal_email: 'alice@example.local',
  disposition: '',
  ingest_source: '',
  ingest_source_ref: '',
  mailboxes: [{ name: 'INBOX', is_junk: false }],
  mailbox_name: 'INBOX',
  is_junk: false,
  envelope: {
    from: 'old@example.com',
    to: 'alice@example.local',
    cc: '',
    bcc: '',
    reply_to: '',
    message_id: '<old-001@example.com>',
    in_reply_to: '',
    references: '',
  },
};

// SMTP-time reject/defer trail entry: no metadata, the ref is a
// "rcpt:<addr>" subject-of-record, never rendered under "Recipient".
const SMTP_REJECT_HIT = {
  source: 'smtp_event',
  at: new Date(Date.now() - 300_000).toISOString(),
  action: 'smtp.rcpt.resolve',
  actor_id: 'alice@example.local',
  ref: 'rcpt:unknown@nosuchwhere.example',
  remote_addr: '203.0.113.42',
  outcome: 'failure',
  message: 'User unknown',
  domain: 'example.local',
};

// smtp.accept entry: metadata carries the envelope, ref is a blob
// reference ("message:<hash>").
const SMTP_ACCEPT_HIT = {
  source: 'smtp_event',
  at: new Date(Date.now() - 360_000).toISOString(),
  action: 'smtp.accept',
  actor_id: '',
  ref: 'message:abcd1234efgh5678',
  metadata: {
    mail_from: 'ext@sender.test',
    rcpt_to: 'alice@example.local,dana@example.local',
  },
  remote_addr: '203.0.113.77',
  outcome: 'success',
  message: 'session=abc123 recipients=2 size=4096',
  domain: 'example.local',
};

const SEND_OUTCOME_HIT = {
  source: 'send_outcome',
  at: new Date(Date.now() - 600_000).toISOString(),
  queue_id: 1001,
  mail_from: 'alice@example.local',
  rcpt_to: 'bob@remote.example',
  envelope_id: 'env-abc-123',
  state: 'deferred',
  attempts: 3,
  last_error: 'Connection timeout after 30s',
  last_attempt_at: new Date(Date.now() - 300_000).toISOString(),
};

const SEND_OUTCOME_DONE = {
  source: 'send_outcome',
  at: new Date(Date.now() - 700_000).toISOString(),
  queue_id: 1002,
  mail_from: 'alice@example.local',
  rcpt_to: 'carol@remote.example',
  envelope_id: 'env-def-456',
  state: 'done',
  attempts: 1,
};

const ALL_ITEMS = [
  RECEIVED_HIT,
  MULTI_MAILBOX_HIT,
  SMTP_REJECT_HIT,
  SMTP_ACCEPT_HIT,
  SEND_OUTCOME_HIT,
  SEND_OUTCOME_DONE,
];

const PAGE2_ITEMS = [
  {
    source: 'received',
    at: new Date(Date.now() - 900_000).toISOString(),
    principal_id: 2,
    principal_email: 'filip@example.local',
    disposition: 'delivered_inbox',
    ingest_source: 'smtp',
    ingest_source_ref: '',
    mailboxes: [{ name: 'INBOX', is_junk: false }],
    mailbox_name: 'INBOX',
    is_junk: false,
    envelope: {
      from: 'page2sender@example.com',
      to: 'filip@example.local',
      cc: '',
      bcc: '',
      reply_to: '',
      message_id: '<page2-001@example.com>',
      in_reply_to: '',
      references: '',
    },
  },
];

test.describe('message-research', () => {
  test.beforeEach(async ({ page }) => {
    installAdminSession(page);
  });

  test('page renders heading and filter form fields, no subject filter', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ALL_ITEMS, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();

    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // Remaining filter inputs are present.
    await expect(page.getByLabel('Filter by sender')).toBeVisible();
    await expect(page.getByLabel('Filter by recipient')).toBeVisible();
    await expect(page.getByLabel('Filter by message ID')).toBeVisible();
    await expect(page.getByLabel('From date')).toBeVisible();
    await expect(page.getByLabel('To date')).toBeVisible();

    // No subject filter anywhere in the form (re #143, maintainer finding #4).
    await expect(page.getByLabel('Filter by subject')).toHaveCount(0);

    // Action buttons.
    await expect(page.getByRole('button', { name: 'Search', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Clear' })).toBeVisible();
  });

  test('received hit renders source badge, envelope, ingest path, and disposition', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [RECEIVED_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // Source badge "Received".
    await expect(page.locator('.source-badge').filter({ hasText: 'Received' })).toBeVisible();

    // Envelope fields.
    await expect(page.getByText('kunde@example.com')).toBeVisible();
    await expect(page.getByText('alice@example.local').first()).toBeVisible();

    // Ingest path field: smtp ingest renders "SMTP".
    const ingestRow = page.locator('.entry-row').filter({ hasText: 'Ingest path' });
    await expect(ingestRow.locator('.entry-val')).toHaveText('SMTP');

    // Disposition chip: recorded inbox delivery.
    const dispositionRow = page.locator('.entry-row').filter({ hasText: 'Delivery disposition' });
    await expect(dispositionRow.locator('.chip')).toHaveText('Inbox');

    // Mailbox list.
    await expect(page.locator('.mailbox-list').getByText('INBOX', { exact: true })).toBeVisible();

    // Spam verdict badge.
    await expect(page.getByText('ham')).toBeVisible();
  });

  test('received hit lists every mailbox and marks Junk-attributed ones', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [MULTI_MAILBOX_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    const mailboxList = page.locator('.mailbox-list');
    await expect(mailboxList.getByText('Archive', { exact: true })).toBeVisible();
    await expect(mailboxList.getByText('Spam (Junk)')).toBeVisible();
    await expect(mailboxList.getByText('vorsitz@classic-computing.de', { exact: true })).toBeVisible();

    // Ingest path shows the import account.
    const ingestRow = page.locator('.entry-row').filter({ hasText: 'Ingest path' });
    await expect(ingestRow.locator('.entry-val')).toHaveText('IMAP import mail.classic-computing.de');

    // Unrecorded disposition gives an explicit reason naming the ingest path.
    const dispositionRow = page.locator('.entry-row').filter({ hasText: 'Delivery disposition' });
    await expect(dispositionRow.locator('.chip')).toHaveText(
      'Not recorded (imap-import mail.classic-computing.de)',
    );
  });

  test('received hit with no ingest source shows the row-predates-recording reason', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [PREDATES_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    const ingestRow = page.locator('.entry-row').filter({ hasText: 'Ingest path' });
    await expect(ingestRow.locator('.entry-val')).toHaveText('not recorded');

    const dispositionRow = page.locator('.entry-row').filter({ hasText: 'Delivery disposition' });
    await expect(dispositionRow.locator('.chip')).toHaveText('Not recorded (row predates recording)');
  });

  test('smtp reject/defer event renders action, outcome, message, and reference (never Recipient)', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [SMTP_REJECT_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // Source badge "SMTP".
    await expect(page.locator('.source-badge').filter({ hasText: 'SMTP' })).toBeVisible();

    // Action, outcome, message.
    await expect(page.getByText('smtp.rcpt.resolve')).toBeVisible();
    await expect(page.getByText('failure')).toBeVisible();
    await expect(page.getByText('User unknown')).toBeVisible();

    // The ref is labelled "Message reference", never "Recipient".
    const refRow = page.locator('.entry-row').filter({ hasText: 'rcpt:unknown@nosuchwhere.example' });
    await expect(refRow.locator('.entry-key')).toHaveText('Message reference');
    await expect(page.locator('.entry-row').filter({ hasText: 'Recipient' })).toHaveCount(0);
  });

  test('smtp.accept event renders sender and recipients from metadata, reference labelled separately', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [SMTP_ACCEPT_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    await expect(page.getByText('smtp.accept')).toBeVisible();

    // Sender labelled "From".
    const fromRow = page.locator('.entry-row').filter({ hasText: 'ext@sender.test' });
    await expect(fromRow.locator('.entry-key')).toHaveText('From');

    // Recipients labelled "Recipient".
    const recipientRow = page.locator('.entry-row').filter({ hasText: 'alice@example.local,dana@example.local' });
    await expect(recipientRow.locator('.entry-key')).toHaveText('Recipient');

    // The blob reference is labelled "Message reference", never "Recipient".
    const refRow = page.locator('.entry-row').filter({ hasText: 'message:abcd1234efgh5678' });
    await expect(refRow.locator('.entry-key')).toHaveText('Message reference');
  });

  test('send_outcome hit renders addresses, state badge, attempts, and last_error', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [SEND_OUTCOME_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // Source badge "OUTGOING".
    await expect(page.locator('.source-badge').filter({ hasText: 'OUTGOING' })).toBeVisible();

    // Addresses, state, attempts, error.
    await expect(page.getByText('alice@example.local')).toBeVisible();
    await expect(page.getByText('bob@remote.example')).toBeVisible();
    await expect(page.getByText('deferred')).toBeVisible();
    // Attempts count "3" — use exact match to avoid ambiguity with substrings.
    await expect(page.getByText('3', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('Connection timeout after 30s')).toBeVisible();
  });

  test('all source types render together in a single results list', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ALL_ITEMS, next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // All three badge types visible.
    await expect(page.locator('.source-badge').filter({ hasText: 'RECEIVED' }).first()).toBeVisible();
    await expect(page.locator('.source-badge').filter({ hasText: 'SMTP' }).first()).toBeVisible();
    await expect(page.locator('.source-badge').filter({ hasText: 'OUTGOING' }).first()).toBeVisible();
  });

  test('sender filter sends sender query parameter', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/message-research*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [RECEIVED_HIT], next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    await page.getByLabel('Filter by sender').fill('kunde@example.com');
    await page.getByRole('button', { name: 'Search', exact: true }).click();

    await page.waitForTimeout(200);
    const senderRequests = requests.filter((u) => u.includes('sender='));
    expect(senderRequests.length).toBeGreaterThan(0);
    expect(senderRequests[senderRequests.length - 1]).toContain('sender=kunde%40example.com');
  });

  test('recipient filter sends recipient query parameter', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/message-research*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [RECEIVED_HIT], next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    await page.getByLabel('Filter by recipient').fill('alice@example.local');
    await page.getByRole('button', { name: 'Search', exact: true }).click();

    await page.waitForTimeout(200);
    const recipientRequests = requests.filter((u) => u.includes('recipient='));
    expect(recipientRequests.length).toBeGreaterThan(0);
    expect(recipientRequests[recipientRequests.length - 1]).toContain('recipient=alice%40example.local');
  });

  test('date-range filters send date_from and date_to parameters', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/message-research*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [], next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    await page.locator('#mr-date-from').fill('2024-06-01T00:00');
    await page.locator('#mr-date-to').fill('2024-06-02T00:00');
    await page.getByRole('button', { name: 'Search', exact: true }).click();

    await page.waitForTimeout(200);
    const fromRequests = requests.filter((u) => u.includes('date_from='));
    const toRequests = requests.filter((u) => u.includes('date_to='));
    expect(fromRequests.length).toBeGreaterThan(0);
    expect(toRequests.length).toBeGreaterThan(0);
  });

  test('load-more sends before_us cursor and appends entries', async ({ page }) => {
    let callCount = 0;
    const loadMoreUrls: string[] = [];

    await page.route('/api/v1/admin/message-research*', (route) => {
      callCount++;
      const url = new URL(route.request().url());
      const hasCursor = url.searchParams.has('before_us');
      if (hasCursor) loadMoreUrls.push(url.toString());

      if (!hasCursor) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ items: ALL_ITEMS, next: '1718000000000000' }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: PAGE2_ITEMS, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // First page results visible.
    await expect(page.getByText('kunde@example.com')).toBeVisible();

    // "Load more" visible when next cursor is set.
    const loadMoreBtn = page.getByRole('button', { name: 'Load more' });
    await expect(loadMoreBtn).toBeVisible();
    await loadMoreBtn.click();

    // Second page results appended.
    await expect(page.getByText('page2sender@example.com')).toBeVisible();
    expect(loadMoreUrls.length).toBeGreaterThan(0);
    expect(loadMoreUrls[0]).toContain('before_us=');
  });

  test('reset clears filters and reloads without filter params', async ({ page }) => {
    const requests: string[] = [];

    await page.route('/api/v1/admin/message-research*', (route) => {
      requests.push(route.request().url());
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: ALL_ITEMS, next: null }),
      });
    });

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // Apply a sender filter.
    await page.getByLabel('Filter by sender').fill('test@example.com');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await page.waitForTimeout(100);

    // Reset.
    await page.getByRole('button', { name: 'Clear' }).click();
    await page.waitForTimeout(100);

    // Filter input should be cleared.
    await expect(page.getByLabel('Filter by sender')).toHaveValue('');

    // The last request should not contain the sender param.
    const lastRequest = requests[requests.length - 1];
    expect(lastRequest).not.toContain('sender=test%40example.com');
  });

  test('empty result set shows "No messages found"', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    await expect(page.getByText('No messages found.')).toBeVisible();
  });
});
