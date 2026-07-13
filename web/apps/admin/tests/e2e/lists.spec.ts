/**
 * lists.spec.ts (epic #183, REQ-MLIST-42)
 *
 * Covers:
 *   - Mailing list table renders unwrapped from the {items,next} envelope
 *   - Create list modal posts createMailingListRequest and navigates to the
 *     new list's detail page
 *   - Detail page shows config + roster (also unwrapped from {items,next})
 *   - Config edit PATCHes patchMailingListRequest with CSRF token
 *   - Add member (external address) posts createMemberRequest
 *   - Member remove two-stage confirm DELETEs the member row
 *
 * Every route fixture below returns the REAL server DTO shape
 * (internal/protoadmin/mlist_dto.go's mailingListDTO / mailingListMemberDTO,
 * wrapped in the pageDTO {items,next} envelope for every collection
 * endpoint) -- never a guessed shape (re #215/#216/#218).
 */

import { test, expect } from '@playwright/test';
import { installAdminSession } from './fixtures/auth';

const NOW = '2026-07-12T18:39:05.400041Z';

function mailingListDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    principal_id: 100,
    posting_address: 'announce@example.local',
    domain: 'example.local',
    display_name: 'Announcements',
    owner_principal_id: 2,
    arc_seal: true,
    posting_policy: 'open',
    subscribe_policy: 'closed',
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  };
}

function memberDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 10,
    list_id: 1,
    external_address: 'someone@gmail.com',
    state: 'active',
    delivery_mode: 'each',
    added_at: NOW,
    ...overrides,
  };
}

const LISTS_RESP = { items: [mailingListDTO()], next: null };

test.describe('mailing lists', () => {
  test.beforeEach(async ({ page }) => {
    installAdminSession(page);
  });

  test('list table renders', async ({ page }) => {
    await page.route('/api/v1/lists*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTS_RESP) }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Mailing lists' }).click();
    await expect(page.getByRole('heading', { name: 'Mailing lists' })).toBeVisible();
    await expect(page.getByText('Announcements')).toBeVisible();
    await expect(page.getByText('announce@example.local')).toBeVisible();
  });

  test('create list modal posts and navigates to the new list detail page', async ({ page }) => {
    let createBody: Record<string, unknown> | null = null;

    await page.route(/\/api\/v1\/lists(\?.*)?$/, async (route) => {
      const req = route.request();
      if (req.method() === 'POST') {
        createBody = req.postDataJSON() as Record<string, unknown>;
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(mailingListDTO({ id: 5, posting_address: 'new@example.local', display_name: 'New list' })),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTS_RESP) });
    });
    await page.route('/api/v1/lists/5', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mailingListDTO({ id: 5, posting_address: 'new@example.local', display_name: 'New list' })),
      }),
    );
    await page.route('/api/v1/lists/5/members*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next: null }) }),
    );

    await page.context().addCookies([
      { name: 'herold_public_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Mailing lists' }).click();
    await page.getByRole('button', { name: 'New list' }).click();
    await expect(page.getByRole('dialog', { name: 'New mailing list' })).toBeVisible();

    await page.locator('#cl-address').fill('new@example.local');
    await page.locator('#cl-name').fill('New list');
    await page.getByRole('button', { name: 'Create list' }).click();

    expect(createBody).not.toBeNull();
    expect(createBody!.posting_address).toBe('new@example.local');
    expect(createBody!.display_name).toBe('New list');

    // Navigated to the new list's detail page.
    await expect(page.getByRole('heading', { name: 'New list' })).toBeVisible();
  });

  test('detail page shows config and roster unwrapped from the envelope', async ({ page }) => {
    await page.route('/api/v1/lists*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTS_RESP) }),
    );
    await page.route('/api/v1/lists/1', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mailingListDTO()) }),
    );
    await page.route('/api/v1/lists/1/members*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [memberDTO()], next: null }) }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Mailing lists' }).click();
    await page.getByText('Announcements').click();

    await expect(page.getByRole('heading', { name: 'List configuration' })).toBeVisible();
    await expect(page.locator('#ml-name')).toHaveValue('Announcements');
    await expect(page.getByText('someone@gmail.com')).toBeVisible();
  });

  test('config edit PATCHes with CSRF token', async ({ page }) => {
    let patchHeaders: Record<string, string | undefined> | null = null;
    let patchBody: Record<string, unknown> | null = null;

    await page.route('/api/v1/lists*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTS_RESP) }),
    );
    await page.route('/api/v1/lists/1', async (route) => {
      const req = route.request();
      if (req.method() === 'PATCH') {
        patchHeaders = req.headers();
        patchBody = req.postDataJSON() as Record<string, unknown>;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(mailingListDTO({ display_name: (patchBody as Record<string, unknown>).display_name })),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mailingListDTO()) });
    });
    await page.route('/api/v1/lists/1/members*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next: null }) }),
    );

    await page.context().addCookies([
      { name: 'herold_public_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Mailing lists' }).click();
    await page.getByText('Announcements').click();

    await page.locator('#ml-name').fill('Renamed list');
    await page.getByRole('button', { name: 'Save' }).click();

    await expect(page.getByText('Saved')).toBeVisible();
    expect(patchHeaders).not.toBeNull();
    expect(patchHeaders!['x-csrf-token']).toBe('test-csrf-token');
    expect(patchBody).not.toBeNull();
    expect((patchBody as Record<string, unknown>).display_name).toBe('Renamed list');
  });

  test('add external member posts createMemberRequest', async ({ page }) => {
    let addBody: Record<string, unknown> | null = null;

    await page.route('/api/v1/lists*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTS_RESP) }),
    );
    await page.route('/api/v1/lists/1', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mailingListDTO()) }),
    );
    await page.route(/\/api\/v1\/lists\/1\/members(\?.*)?$/, async (route) => {
      const req = route.request();
      if (req.method() === 'POST') {
        addBody = req.postDataJSON() as Record<string, unknown>;
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(memberDTO({ id: 20, external_address: 'newmember@example.com' })),
        });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next: null }) });
    });

    await page.context().addCookies([
      { name: 'herold_public_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Mailing lists' }).click();
    await page.getByText('Announcements').click();

    await page.getByRole('button', { name: 'Add member' }).click();
    await expect(page.getByRole('dialog', { name: 'Add member' })).toBeVisible();
    await page.getByRole('radio', { name: 'External address' }).click();
    await page.locator('#am-external').fill('newmember@example.com');
    await page.getByRole('dialog', { name: 'Add member' }).getByRole('button', { name: 'Add member' }).click();

    expect(addBody).not.toBeNull();
    expect(addBody!.external_address).toBe('newmember@example.com');
  });

  test('member remove two-stage confirm DELETEs the row', async ({ page }) => {
    let deletedId: string | null = null;

    await page.route('/api/v1/lists*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LISTS_RESP) }),
    );
    await page.route('/api/v1/lists/1', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mailingListDTO()) }),
    );
    await page.route(/\/api\/v1\/lists\/1\/members/, (route) => {
      const req = route.request();
      if (req.method() === 'DELETE') {
        deletedId = req.url().split('/').pop() ?? null;
        return route.fulfill({ status: 204 });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [memberDTO()], next: null }) });
    });

    await page.context().addCookies([
      { name: 'herold_public_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'Mailing lists' }).click();
    await page.getByText('Announcements').click();

    await expect(page.getByText('someone@gmail.com')).toBeVisible();
    await page.getByRole('button', { name: 'Remove' }).click();
    await expect(page.getByText('Remove?')).toBeVisible();
    await page.getByRole('button', { name: 'Confirm' }).click();

    expect(deletedId).toBe('10');
  });
});
