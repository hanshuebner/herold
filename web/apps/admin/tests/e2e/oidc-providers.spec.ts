/**
 * oidc-providers.spec.ts (epic #188, REQ-AC-60..70, re #239)
 *
 * Covers the admin SPA screens wired to internal/protoadmin/claim_mapping.go:
 *   - Provider list renders with the trusted/untrusted badge
 *   - Provider detail: the authz_trusted toggle is interactive for a
 *     superadmin and is REPLACED BY A READ-ONLY BADGE for a plain admin --
 *     the UI gate matching the server's requireSuperAdmin gate is the
 *     security-critical behaviour this spec pins
 *   - Claim allowlist add/remove issues the right POST/DELETE
 *   - Claim-mapping rule table renders the orphaned / author_authority_valid
 *     status badges (REQ-AC-68), the create form never offers
 *     resource_kind=server (REQ-AC-64), and create/delete issue the right
 *     requests and re-fetch the rule list afterwards (no client-only state)
 *
 * Every route fixture returns the real server DTO shape from
 * internal/protoadmin/claim_mapping.go and types.go's oidcProviderDTO,
 * wrapped in the pageDTO {items,next} envelope for every collection
 * endpoint, matching the convention the other admin e2e specs use.
 */

import { test, expect } from '@playwright/test';
import { installAdminSession, ADMIN_PRINCIPAL_ID } from './fixtures/auth';

const NOW = '2026-07-14T04:39:38.793884Z';

function providerDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 'fakeoidc',
    name: 'fakeoidc',
    issuer: 'http://127.0.0.1:63428',
    client_id: 'fakeoidc-client',
    scopes: ['openid', 'email', 'profile'],
    auto_provision: true,
    auto_provision_domain: 'example.local',
    authz_trusted: false,
    created_at: NOW,
    ...overrides,
  };
}

function ruleDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    provider: 'fakeoidc',
    claim: 'groups',
    match_value: 'mail-admins',
    resource_kind: 'domain',
    resource_id: 'example.local',
    level: 'operator',
    created_by: 1,
    orphaned: false,
    author_authority_valid: true,
    created_at: NOW,
    ...overrides,
  };
}

/** Mocks GET /api/v1/principals/{self} with the given flags array. */
function installSelfPrincipal(
  page: import('@playwright/test').Page,
  flags: string[],
): void {
  void page.route(`/api/v1/principals/${ADMIN_PRINCIPAL_ID}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: Number(ADMIN_PRINCIPAL_ID),
        kind: 'user',
        canonical_email: 'admin@example.com',
        quota_bytes: 0,
        flags,
        totp_enabled: true,
        seen_addresses_enabled: true,
        created_at: NOW,
        updated_at: NOW,
      }),
    }),
  );
}

/** Mocks the provider detail's three GET endpoints with static bodies. */
function installProviderDetailGets(
  page: import('@playwright/test').Page,
  opts: { provider?: Record<string, unknown>; allowlist?: Array<{ claim: string }>; rules?: Array<Record<string, unknown>> } = {},
): void {
  const provider = opts.provider ?? providerDTO();
  const allowlist = opts.allowlist ?? [];
  const rules = opts.rules ?? [];
  void page.route('/api/v1/oidc/providers/fakeoidc', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(provider) });
    }
    return route.continue();
  });
  void page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-allowlist$/, (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: allowlist, next: null }) });
    }
    return route.continue();
  });
  void page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-mapping-rules$/, (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: rules, next: null }) });
    }
    return route.continue();
  });
}

test.describe('OIDC providers', () => {
  test.beforeEach(async ({ page }) => {
    installAdminSession(page);
    await page.context().addCookies([
      { name: 'herold_public_csrf', value: 'test-csrf-token', domain: 'localhost', path: '/' },
    ]);
  });

  test('provider list renders with trusted/untrusted badges', async ({ page }) => {
    await page.route('/api/v1/oidc/providers', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          items: [providerDTO(), providerDTO({ id: 'trusted-idp', name: 'trusted-idp', authz_trusted: true })],
          next: null,
        }),
      }),
    );

    await page.goto('/admin/');
    await page.getByRole('button', { name: 'OIDC providers' }).click();

    await expect(page.getByRole('heading', { name: 'OIDC providers' })).toBeVisible();
    const fakeoidcRow = page.locator('tr.table-row').filter({ hasText: 'fakeoidc' });
    await expect(fakeoidcRow.getByText('Untrusted', { exact: true })).toBeVisible();
    const trustedRow = page.locator('tr.table-row').filter({ hasText: 'trusted-idp' });
    await expect(trustedRow.getByText('Trusted', { exact: true })).toBeVisible();
  });

  test('authz_trusted toggle is interactive for a superadmin and PUTs + re-fetches', async ({ page }) => {
    installSelfPrincipal(page, ['admin', 'super_admin']);
    await page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-allowlist$/, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next: null }) }),
    );
    await page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-mapping-rules$/, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next: null }) }),
    );

    // A GET handler on the bare provider resource plus a separate PUT
    // handler on its /authz-trusted sub-path (the real endpoint,
    // PUT /api/v1/oidc/providers/{id}/authz-trusted per
    // internal/protoadmin/routes.go), sharing mutable server-side state --
    // GET always reflects the current `trusted` value, PUT updates it. The
    // PUT response deliberately reports the OLD (pre-mutation) value, so the
    // assertions below on the rendered state can only pass if the component
    // re-fetches via a follow-up GET rather than trusting the PUT response.
    let trusted = false;
    let putBody: Record<string, unknown> | null = null;
    let getCount = 0;
    await page.route('/api/v1/oidc/providers/fakeoidc', (route) => {
      getCount += 1;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(providerDTO({ authz_trusted: trusted })) });
    });
    await page.route('/api/v1/oidc/providers/fakeoidc/authz-trusted', async (route) => {
      putBody = route.request().postDataJSON() as Record<string, unknown>;
      const staleBody = providerDTO({ authz_trusted: trusted });
      trusted = putBody.authz_trusted as boolean;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(staleBody) });
    });

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await expect(page.getByRole('heading', { name: 'fakeoidc' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Untrusted', exact: true, checked: true })).toBeVisible();
    const getCountBeforeClick = getCount;

    await page.getByRole('radio', { name: 'Trusted', exact: true }).click();

    expect(putBody).toEqual({ authz_trusted: true });
    await expect(page.getByRole('radio', { name: 'Trusted', exact: true, checked: true })).toBeVisible();
    expect(getCount).toBeGreaterThan(getCountBeforeClick);
  });

  test('authz_trusted control is read-only for a plain admin', async ({ page }) => {
    installSelfPrincipal(page, ['admin']);
    installProviderDetailGets(page, { provider: providerDTO({ authz_trusted: true }) });

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await expect(page.getByRole('heading', { name: 'fakeoidc' })).toBeVisible();

    // No interactive toggle for a non-superadmin -- only the read-only badge.
    await expect(page.getByRole('radio', { name: 'Trusted' })).toHaveCount(0);
    await expect(page.getByRole('radio', { name: 'Untrusted' })).toHaveCount(0);
    await expect(page.getByText('Only a super-administrator may change this setting.')).toBeVisible();
    await expect(page.locator('.trust-readonly').getByText('Trusted', { exact: true })).toBeVisible();
  });

  test('claim allowlist add issues POST and remove issues DELETE', async ({ page }) => {
    installSelfPrincipal(page, ['admin', 'super_admin']);
    installProviderDetailGets(page, { allowlist: [] });
    let postBody: Record<string, unknown> | null = null;
    let allowlistState: Array<{ claim: string }> = [];
    await page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-allowlist$/, async (route) => {
      const req = route.request();
      if (req.method() === 'POST') {
        postBody = req.postDataJSON() as Record<string, unknown>;
        allowlistState = [{ claim: postBody.claim as string }];
        return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ claim: postBody.claim }) });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: allowlistState, next: null }) });
    });
    let deletedPath: string | null = null;
    await page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-allowlist\/groups$/, (route) => {
      deletedPath = new URL(route.request().url()).pathname;
      allowlistState = [];
      return route.fulfill({ status: 204 });
    });

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await expect(page.getByRole('heading', { name: 'fakeoidc' })).toBeVisible();

    await page.getByPlaceholder('Claim name, e.g. groups').fill('groups');
    await page.getByRole('button', { name: 'Add' }).click();

    expect(postBody).toEqual({ claim: 'groups' });
    await expect(page.locator('.claim-chip').filter({ hasText: 'groups' })).toBeVisible();

    await page.getByRole('button', { name: 'Remove claim groups from allowlist' }).click();

    expect(deletedPath).toBe('/api/v1/oidc/providers/fakeoidc/claim-allowlist/groups');
    await expect(page.locator('.claim-chip').filter({ hasText: 'groups' })).toHaveCount(0);
  });

  test('claim-mapping rule table renders orphaned and author-authority-lost badges', async ({ page }) => {
    installSelfPrincipal(page, ['admin', 'super_admin']);
    installProviderDetailGets(page, {
      rules: [
        ruleDTO({ id: 1, match_value: 'orphan-testers', orphaned: true, author_authority_valid: false, created_by: 0 }),
        ruleDTO({ id: 2, match_value: 'authority-lost', orphaned: false, author_authority_valid: false }),
        ruleDTO({ id: 3, match_value: 'still-active', orphaned: false, author_authority_valid: true }),
      ],
    });

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await expect(page.getByRole('heading', { name: 'fakeoidc' })).toBeVisible();

    const orphanedRow = page.locator('tr.table-row').filter({ hasText: 'orphan-testers' });
    await expect(orphanedRow.getByText('Orphaned author')).toBeVisible();

    const authorityLostRow = page.locator('tr.table-row').filter({ hasText: 'authority-lost' });
    await expect(authorityLostRow.getByText('Author authority lost')).toBeVisible();

    const activeRow = page.locator('tr.table-row').filter({ hasText: 'still-active' });
    await expect(activeRow.getByText('Active', { exact: true })).toBeVisible();
  });

  test('new rule dialog does not offer resource_kind=server', async ({ page }) => {
    installSelfPrincipal(page, ['admin', 'super_admin']);
    installProviderDetailGets(page);

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await page.getByRole('button', { name: 'New rule' }).click();

    await expect(page.getByRole('dialog', { name: 'New claim-mapping rule' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Domain' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Mailing list' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Mailbox' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Server', exact: true })).toHaveCount(0);
    await expect(page.getByText('Server', { exact: true })).toHaveCount(0);
  });

  test('create rule POSTs the payload and the view re-fetches the rule list', async ({ page }) => {
    installSelfPrincipal(page, ['admin', 'super_admin']);
    installProviderDetailGets(page, { rules: [] });
    let postBody: Record<string, unknown> | null = null;
    let rulesGetCount = 0;
    await page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-mapping-rules$/, async (route) => {
      const req = route.request();
      if (req.method() === 'POST') {
        postBody = req.postDataJSON() as Record<string, unknown>;
        // Deliberately different match_value than the follow-up GET, so the
        // rendered row can only match if it came from the re-fetch.
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(ruleDTO({ id: 5, match_value: 'stale-from-post' })),
        });
      }
      rulesGetCount += 1;
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [ruleDTO({ id: 5, match_value: 'mail-admins' })], next: null }),
      });
    });

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await page.getByRole('button', { name: 'New rule' }).click();
    await expect(page.getByRole('dialog', { name: 'New claim-mapping rule' })).toBeVisible();

    await page.locator('#rule-claim').fill('groups');
    await page.locator('#rule-match').fill('mail-admins');
    await page.getByRole('radio', { name: 'Domain' }).click();
    await page.locator('#rule-resource-id').fill('example.local');
    await page.locator('#rule-level').selectOption('operator');
    await page.getByRole('dialog', { name: 'New claim-mapping rule' }).getByRole('button', { name: 'Create rule' }).click();

    expect(postBody).toEqual({
      claim: 'groups',
      match_value: 'mail-admins',
      resource_kind: 'domain',
      resource_id: 'example.local',
      level: 'operator',
    });
    expect(rulesGetCount).toBeGreaterThan(0);
    // The rendered row reflects the re-fetched GET's value, not the POST's.
    await expect(page.getByText('stale-from-post')).toHaveCount(0);
    await expect(page.locator('tr.table-row').filter({ hasText: 'mail-admins' })).toBeVisible();
  });

  test('delete rule DELETEs the rule id and the view re-fetches the rule list', async ({ page }) => {
    installSelfPrincipal(page, ['admin', 'super_admin']);
    installProviderDetailGets(page);
    let deletedPath: string | null = null;
    let rulesGetCount = 0;
    // Mutable server-side state: starts with the one seeded rule, becomes
    // empty after DELETE. The GET branch reflects current state -- if the
    // view rendered the initial snapshot forever instead of re-fetching
    // after the delete, the final toHaveCount(0) assertion below would fail.
    let rulesState = [ruleDTO({ id: 7, match_value: 'mail-admins' })];
    await page.route(/\/api\/v1\/oidc\/providers\/fakeoidc\/claim-mapping-rules(\/7)?$/, (route) => {
      const req = route.request();
      if (req.method() === 'DELETE') {
        deletedPath = new URL(req.url()).pathname;
        rulesState = [];
        return route.fulfill({ status: 204 });
      }
      rulesGetCount += 1;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: rulesState, next: null }) });
    });

    await page.goto('/admin/#/oidc-providers/fakeoidc');
    await expect(page.locator('tr.table-row').filter({ hasText: 'mail-admins' })).toBeVisible();

    await page.locator('tr.table-row').filter({ hasText: 'mail-admins' }).getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByText('Delete this rule?')).toBeVisible();
    await page.getByRole('button', { name: 'Delete' }).last().click();

    expect(deletedPath).toBe('/api/v1/oidc/providers/fakeoidc/claim-mapping-rules/7');
    expect(rulesGetCount).toBeGreaterThan(0);
    await expect(page.locator('tr.table-row').filter({ hasText: 'mail-admins' })).toHaveCount(0);
  });
});
