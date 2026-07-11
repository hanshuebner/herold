/**
 * research.spec.ts — Message-research view (re #143)
 *
 * Covers:
 *   - Page renders with heading "Nachrichtenrecherche" and filter form
 *   - "received" source entries render with source badge, envelope, mailbox, Junk
 *   - "smtp_event" entries render with action, outcome badge, message
 *   - "send_outcome" entries render with mail_from/rcpt_to, state badge, attempts/error
 *   - Sender filter sends the sender query param
 *   - Recipient filter sends the recipient query param
 *   - Date-range filters send date_from / date_to
 *   - Subject filter sends the subject query param
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
  mailbox_name: 'INBOX',
  is_junk: false,
  spam_verdict: 'ham',
  spam_confidence: 0.95,
  envelope: {
    subject: 'Bestellung #42',
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

const JUNK_RECEIVED_HIT = {
  source: 'received',
  at: new Date(Date.now() - 180_000).toISOString(),
  principal_id: 1,
  mailbox_name: 'Junk',
  is_junk: true,
  spam_verdict: 'spam',
  spam_confidence: 0.99,
  envelope: {
    subject: 'Gewinner!!!',
    from: 'spammer@evil.example',
    to: 'alice@example.local',
    cc: '',
    bcc: '',
    reply_to: '',
    message_id: '<spam-001@evil.example>',
    in_reply_to: '',
    references: '',
  },
};

const SMTP_EVENT_HIT = {
  source: 'smtp_event',
  at: new Date(Date.now() - 300_000).toISOString(),
  action: 'smtp.rcpt.resolve',
  actor_id: 'alice@example.local',
  subject: 'unknown@nosuchwhere.example',
  remote_addr: '203.0.113.42',
  outcome: 'failure',
  message: 'User unknown',
  domain: 'example.local',
};

const SMTP_SUCCESS_HIT = {
  source: 'smtp_event',
  at: new Date(Date.now() - 360_000).toISOString(),
  action: 'smtp.rcpt.resolve',
  actor_id: 'alice@example.local',
  subject: 'alice@example.local',
  remote_addr: '203.0.113.42',
  outcome: 'success',
  message: '',
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
  JUNK_RECEIVED_HIT,
  SMTP_EVENT_HIT,
  SMTP_SUCCESS_HIT,
  SEND_OUTCOME_HIT,
  SEND_OUTCOME_DONE,
];

const PAGE2_ITEMS = [
  {
    source: 'received',
    at: new Date(Date.now() - 900_000).toISOString(),
    principal_id: 2,
    mailbox_name: 'INBOX',
    is_junk: false,
    envelope: {
      subject: 'Seite 2 Nachricht',
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

  test('page renders heading and filter form fields', async ({ page }) => {
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

    // All filter inputs should be present.
    await expect(page.getByLabel('Filter by sender')).toBeVisible();
    await expect(page.getByLabel('Filter by recipient')).toBeVisible();
    await expect(page.getByLabel('Filter by subject')).toBeVisible();
    await expect(page.getByLabel('Filter by message ID')).toBeVisible();
    await expect(page.getByLabel('From date')).toBeVisible();
    await expect(page.getByLabel('To date')).toBeVisible();

    // Action buttons.
    await expect(page.getByRole('button', { name: 'Search', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Clear' })).toBeVisible();
  });

  test('received hit renders source badge, envelope fields, mailbox and spam verdict', async ({ page }) => {
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

    // Source badge "Empfangen" (CSS text-transform: uppercase; DOM text is "Empfangen").
    await expect(page.locator('.source-badge').filter({ hasText: 'Received' })).toBeVisible();

    // Envelope fields.
    await expect(page.getByText('kunde@example.com')).toBeVisible();
    await expect(page.getByText('alice@example.local')).toBeVisible();
    await expect(page.getByText('Bestellung #42')).toBeVisible();
    await expect(page.getByText('INBOX')).toBeVisible();

    // Spam verdict badge.
    await expect(page.getByText('ham')).toBeVisible();
  });

  test('junk received hit shows Junk badge and spam verdict', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [JUNK_RECEIVED_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // The Junk chip and mailbox name both contain "Junk" — use the chip locator.
    await expect(page.locator('.chip').filter({ hasText: 'Junk' }).first()).toBeVisible();
    await expect(page.locator('.chip').filter({ hasText: 'spam' })).toBeVisible();
    await expect(page.getByText('spammer@evil.example')).toBeVisible();
  });

  test('smtp_event hit renders action, outcome badge, and message', async ({ page }) => {
    await page.route('/api/v1/admin/message-research*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [SMTP_EVENT_HIT], next: null }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Message research' }).click();
    await expect(page.getByRole('heading', { name: 'Message research' })).toBeVisible();

    // Source badge "SMTP" — use the badge locator to avoid substring matches on action names.
    await expect(page.locator('.source-badge').filter({ hasText: 'SMTP' })).toBeVisible();

    // Action, outcome, message.
    await expect(page.getByText('smtp.rcpt.resolve')).toBeVisible();
    await expect(page.getByText('failure')).toBeVisible();
    await expect(page.getByText('User unknown')).toBeVisible();
    await expect(page.getByText('unknown@nosuchwhere.example')).toBeVisible();
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

    // Source badge "AUSGANG".
    await expect(page.locator('.source-badge').filter({ hasText: 'OUTGOING' })).toBeVisible();

    // Addresses, state, attempts, error.
    await expect(page.getByText('alice@example.local')).toBeVisible();
    await expect(page.getByText('bob@remote.example')).toBeVisible();
    await expect(page.getByText('deferred')).toBeVisible();
    // Attempts count "3" — use exact match to avoid ambiguity with substrings.
    await expect(page.getByText('3', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('Connection timeout after 30s')).toBeVisible();
  });

  test('all three source types render together in a single results list', async ({ page }) => {
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

  test('subject filter sends subject query parameter', async ({ page }) => {
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

    await page.getByLabel('Filter by subject').fill('Bestellung');
    await page.getByRole('button', { name: 'Search', exact: true }).click();

    await page.waitForTimeout(200);
    const subjectRequests = requests.filter((u) => u.includes('subject='));
    expect(subjectRequests.length).toBeGreaterThan(0);
    expect(subjectRequests[subjectRequests.length - 1]).toContain('subject=Bestellung');
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

    // "Weitere laden" visible when next cursor is set.
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
