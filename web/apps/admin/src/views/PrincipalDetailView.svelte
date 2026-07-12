<script lang="ts">
  import { principalDetail } from '../lib/principals/principal-detail.svelte';
  import { auth } from '../lib/auth/auth.svelte';
  import { router } from '../lib/router/router.svelte';
  import Tabs from '../lib/ui/Tabs.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';
  import { formatAbsolute, DATE_TIME_SHORT } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  // Import qrcode-svg via the CJS interop. Vite handles require-style modules.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  import QRCode from 'qrcode-svg';

  interface Props {
    id: string;
  }
  let { id }: Props = $props();

  let activeTab = $state('profile');

  const tabs = $derived([
    { value: 'profile', label: t('principalDetail.tab.profile') },
    { value: 'password', label: t('principalDetail.tab.password') },
    { value: 'totp', label: t('principalDetail.tab.totp') },
    { value: 'apikeys', label: t('principalDetail.tab.apikeys') },
    { value: 'oidc', label: t('principalDetail.tab.oidc') },
    { value: 'danger', label: t('principalDetail.tab.danger') },
  ]);

  // Load on mount / when id changes.
  $effect(() => {
    void principalDetail.load(id);
  });

  // --- Profile tab ---
  let profileDisplayName = $state('');
  let profileQuota = $state('');
  let profileAdmin = $state(false);
  let profileDisabled = $state(false);
  let profileIgnoreLimits = $state(false);
  let profileSaving = $state(false);
  let profileError = $state<string | null>(null);
  let profileSuccess = $state<string | null>(null);

  // Sync form fields when principal loads.
  $effect(() => {
    if (principalDetail.principal) {
      const p = principalDetail.principal;
      profileDisplayName = p.display_name ?? '';
      profileQuota = p.quota_bytes != null ? String(p.quota_bytes) : '';
      profileAdmin = p.flags.includes('admin');
      profileDisabled = p.flags.includes('disabled');
      profileIgnoreLimits = p.flags.includes('ignore_download_limits');
    }
  });

  async function saveProfile(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (profileSaving || !principalDetail.principal) return;
    profileSaving = true;
    profileError = null;
    profileSuccess = null;

    // Start from the principal's full current flag set (this preserves
    // flags this form does not expose a checkbox for, e.g. totp_enabled
    // and super_admin) and only toggle the three checkbox-controlled
    // flags. Sending a bare number here (the pre-#218 behaviour) does
    // not match the server's `Flags *[]string` PATCH field and either
    // fails to decode or drops every flag the caller did not resend.
    let flags = principalDetail.principal.flags;
    flags = withFlag(flags, 'admin', profileAdmin);
    flags = withFlag(flags, 'disabled', profileDisabled);
    flags = withFlag(flags, 'ignore_download_limits', profileIgnoreLimits);

    const patch: Record<string, unknown> = { display_name: profileDisplayName, flags };
    const quota = Number(profileQuota);
    if (!isNaN(quota) && quota >= 0) {
      patch.quota_bytes = quota;
    }

    const result = await principalDetail.updateProfile(id, patch);
    profileSaving = false;
    if (!result.ok) {
      profileError = result.errorMessage;
    } else {
      profileSuccess = t('principalDetail.profile.updated');
    }
  }

  function withFlag(flags: string[], name: string, on: boolean): string[] {
    const has = flags.includes(name);
    if (on && !has) return [...flags, name];
    if (!on && has) return flags.filter((f) => f !== name);
    return flags;
  }

  // --- Password tab ---
  let pwCurrent = $state('');
  let pwNew = $state('');
  let pwConfirm = $state('');
  let pwSaving = $state(false);
  let pwError = $state<string | null>(null);
  let pwSuccess = $state<string | null>(null);

  // Admin acting on a different principal: hide current_password field.
  const isAdminOverride = $derived(
    auth.principal !== null &&
    auth.principal.id !== id &&
    auth.principal.scopes.includes('admin'),
  );

  async function savePassword(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (pwSaving) return;
    pwError = null;
    pwSuccess = null;

    if (pwNew !== pwConfirm) {
      pwError = t('principalDetail.password.mismatch');
      return;
    }
    if (pwNew.length < 12) {
      pwError = t('principalDetail.password.tooShort');
      return;
    }
    pwSaving = true;

    const payload: { new_password: string; current_password?: string } = { new_password: pwNew };
    if (!isAdminOverride) {
      payload.current_password = pwCurrent;
    }

    const result = await principalDetail.changePassword(id, payload);
    pwSaving = false;
    if (!result.ok) {
      pwError = result.errorMessage;
    } else {
      pwSuccess = t('principalDetail.password.changed');
      pwCurrent = '';
      pwNew = '';
      pwConfirm = '';
    }
  }

  // --- TOTP tab ---
  let totpProvisioningUri = $state<string | null>(null);
  let totpQrSvg = $state<string | null>(null);
  let totpCode = $state('');
  let totpConfirmError = $state<string | null>(null);
  let totpConfirmSuccess = $state<string | null>(null);
  let totpDisablePassword = $state('');
  let totpDisableError = $state<string | null>(null);
  let totpDisableSuccess = $state<string | null>(null);
  let totpLoading = $state(false);

  async function startTOTPEnroll(): Promise<void> {
    totpLoading = true;
    totpProvisioningUri = null;
    totpQrSvg = null;
    totpConfirmError = null;
    const result = await principalDetail.enrollTOTP(id);
    totpLoading = false;
    if (!result.ok) {
      totpConfirmError = result.errorMessage;
      return;
    }
    if (result.provisioning_uri) {
      totpProvisioningUri = result.provisioning_uri;
      // Render QR code as inline SVG.
      try {
        const qr = new QRCode({
          content: result.provisioning_uri,
          width: 200,
          height: 200,
          color: '#000000',
          background: '#ffffff',
          ecl: 'M',
          padding: 2,
        });
        totpQrSvg = qr.svg();
      } catch {
        // QR generation failure is non-fatal; show the URI as text.
        totpQrSvg = null;
      }
    }
  }

  async function confirmTOTP(): Promise<void> {
    if (!totpCode.trim()) return;
    totpLoading = true;
    totpConfirmError = null;
    const result = await principalDetail.confirmTOTP(id, totpCode.trim());
    totpLoading = false;
    if (!result.ok) {
      totpConfirmError = result.errorMessage;
    } else {
      totpConfirmSuccess = t('principalDetail.totp.enabled');
      totpProvisioningUri = null;
      totpQrSvg = null;
      totpCode = '';
    }
  }

  async function disableTOTP(): Promise<void> {
    if (!totpDisablePassword) return;
    totpLoading = true;
    totpDisableError = null;
    const result = await principalDetail.disableTOTP(id, totpDisablePassword);
    totpLoading = false;
    if (!result.ok) {
      totpDisableError = result.errorMessage;
    } else {
      totpDisableSuccess = t('principalDetail.totp.disabled');
      totpDisablePassword = '';
    }
  }

  // --- API keys tab ---
  let keyDialogOpen = $state(false);
  let keyLabel = $state('');
  let keyScopes = $state<string[]>([]);
  let keyCreating = $state(false);
  let keyCreateError = $state<string | null>(null);
  let newKeyPlaintext = $state<string | null>(null);
  let newKeyConfirmed = $state(false);

  const availableScopes = ['read', 'write', 'admin'];

  function openKeyDialog(): void {
    keyLabel = '';
    keyScopes = [];
    keyCreateError = null;
    newKeyPlaintext = null;
    newKeyConfirmed = false;
    keyDialogOpen = true;
  }

  function toggleScope(scope: string): void {
    if (keyScopes.includes(scope)) {
      keyScopes = keyScopes.filter((s) => s !== scope);
    } else {
      keyScopes = [...keyScopes, scope];
    }
  }

  async function createKey(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (keyCreating) return;
    keyCreateError = null;
    keyCreating = true;
    const result = await principalDetail.createAPIKey(id, { label: keyLabel || 'ui-issued', scopes: keyScopes });
    keyCreating = false;
    if (!result.ok) {
      keyCreateError = result.errorMessage;
    } else {
      newKeyPlaintext = result.plaintext ?? null;
    }
  }

  async function revokeKey(keyId: string): Promise<void> {
    await principalDetail.revokeAPIKey(id, keyId);
  }

  function copyKey(): void {
    if (newKeyPlaintext) {
      void navigator.clipboard.writeText(newKeyPlaintext);
    }
  }

  // --- OIDC tab ---
  let oidcLinkError = $state<string | null>(null);

  interface OIDCProvider {
    id: string;
    name: string;
    issuer: string;
  }
  let oidcProviders = $state<OIDCProvider[]>([]);
  let oidcProvidersLoading = $state(false);

  // Fetch available OIDC providers when the OIDC tab becomes active.
  $effect(() => {
    if (activeTab === 'oidc' && oidcProviders.length === 0 && !oidcProvidersLoading) {
      void loadOIDCProviders();
    }
  });

  async function loadOIDCProviders(): Promise<void> {
    oidcProvidersLoading = true;
    try {
      const result = await import('../lib/api/client').then(({ apiGet }) =>
        apiGet<{ items: OIDCProvider[]; next: string | null }>('/api/v1/oidc/providers'),
      );
      if (result.ok && result.data) {
        oidcProviders = result.data.items ?? [];
      }
    } finally {
      oidcProvidersLoading = false;
    }
  }

  /** Providers not yet linked to this principal. */
  const unlinkedProviders = $derived(
    oidcProviders.filter(
      (prov) => !principalDetail.oidcLinks.some((l) => l.provider_id === prov.id),
    ),
  );

  async function linkOIDC(providerId: string): Promise<void> {
    oidcLinkError = null;
    const result = await principalDetail.beginOIDCLink(id, providerId);
    if (!result.ok) {
      oidcLinkError = result.errorMessage;
      return;
    }
    if (result.auth_url) {
      window.location.href = result.auth_url;
    }
  }

  async function unlinkOIDC(provider: string): Promise<void> {
    await principalDetail.unlinkOIDC(id, provider);
  }

  // --- Danger zone ---
  let deleteConfirmEmail = $state('');
  let deleteError = $state<string | null>(null);
  let deleteSubmitting = $state(false);

  async function deletePrincipal(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (!principalDetail.principal) return;
    if (deleteConfirmEmail !== principalDetail.principal.canonical_email) {
      deleteError = t('principalDetail.danger.emailMismatch');
      return;
    }
    deleteSubmitting = true;
    deleteError = null;
    const result = await principalDetail.deletePrincipal(id);
    deleteSubmitting = false;
    if (!result.ok) {
      deleteError = result.errorMessage;
    } else {
      router.navigate('/principals');
    }
  }

  function formatDate(iso: string): string {
    return formatAbsolute(iso, DATE_TIME_SHORT);
  }

  function formatBytes(b: number): string {
    if (b === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(b) / Math.log(1024));
    return (b / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + ' ' + units[i];
  }
</script>

<div class="detail-page">
  <div class="page-header">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/principals')}
      aria-label={t('principalDetail.backAriaLabel')}
    >
      {t('principalDetail.back')}
    </button>
    {#if principalDetail.principal}
      <div class="header-info">
        <h1 class="page-title">{principalDetail.principal.canonical_email}</h1>
        {#if principalDetail.principal.display_name}
          <p class="page-subtitle">{principalDetail.principal.display_name}</p>
        {/if}
      </div>
    {:else if principalDetail.status === 'loading'}
      <div class="spinner" role="status" aria-label={t('common.loading')}></div>
    {/if}
  </div>

  {#if principalDetail.status === 'error'}
    <div class="page-error" role="alert">{principalDetail.errorMessage}</div>
  {:else if principalDetail.status === 'ready' && principalDetail.principal}
    <Tabs bind:value={activeTab} {tabs}>
      <!-- Profile -->
      {#if activeTab === 'profile'}
        <form class="tab-form" onsubmit={saveProfile} novalidate>
          <div class="field">
            <label for="pd-display-name" class="label">{t('principalDetail.profile.displayName')}</label>
            <input
              id="pd-display-name"
              type="text"
              class="input"
              bind:value={profileDisplayName}
              disabled={profileSaving}
            />
          </div>

          <div class="field">
            <label for="pd-quota" class="label">{t('principalDetail.profile.quota')}</label>
            <input
              id="pd-quota"
              type="number"
              class="input"
              min="0"
              bind:value={profileQuota}
              disabled={profileSaving}
            />
            {#if profileQuota && !isNaN(Number(profileQuota))}
              <p class="field-hint">{formatBytes(Number(profileQuota))}</p>
            {/if}
          </div>

          <div class="field-group">
            <label class="check-label">
              <input type="checkbox" bind:checked={profileAdmin} disabled={profileSaving} />
              {t('principalDetail.profile.admin')}
            </label>
            <label class="check-label">
              <input type="checkbox" bind:checked={profileDisabled} disabled={profileSaving} />
              {t('principalDetail.profile.disabled')}
            </label>
            <label class="check-label">
              <input type="checkbox" bind:checked={profileIgnoreLimits} disabled={profileSaving} />
              {t('principalDetail.profile.ignoreLimits')}
            </label>
          </div>

          {#if profileError}
            <p class="form-error" role="alert">{profileError}</p>
          {/if}
          {#if profileSuccess}
            <p class="form-success" role="status">{profileSuccess}</p>
          {/if}

          <div class="form-actions">
            <button type="submit" class="btn-primary" disabled={profileSaving}>
              {profileSaving ? t('principalDetail.profile.saving') : t('principalDetail.profile.save')}
            </button>
          </div>
        </form>

        <dl class="meta-list">
          <div class="meta-row">
            <dt>{t('principalDetail.profile.principalId')}</dt>
            <dd class="mono">{principalDetail.principal.id}</dd>
          </div>
          <div class="meta-row">
            <dt>{t('principalDetail.profile.created')}</dt>
            <dd>{formatDate(principalDetail.principal.created_at)}</dd>
          </div>
        </dl>
      {/if}

      <!-- Password -->
      {#if activeTab === 'password'}
        <form class="tab-form" onsubmit={savePassword} novalidate>
          {#if !isAdminOverride}
            <div class="field">
              <label for="pd-pw-current" class="label">{t('principalDetail.password.current')}</label>
              <input
                id="pd-pw-current"
                type="password"
                class="input"
                autocomplete="current-password"
                bind:value={pwCurrent}
                disabled={pwSaving}
              />
            </div>
          {:else}
            <p class="admin-override-note">
              {t('principalDetail.password.adminOverrideNote')}
            </p>
          {/if}

          <div class="field">
            <label for="pd-pw-new" class="label">{t('principalDetail.password.new')}</label>
            <input
              id="pd-pw-new"
              type="password"
              class="input"
              autocomplete="new-password"
              bind:value={pwNew}
              disabled={pwSaving}
            />
          </div>

          <div class="field">
            <label for="pd-pw-confirm" class="label">{t('principalDetail.password.confirm')}</label>
            <input
              id="pd-pw-confirm"
              type="password"
              class="input"
              autocomplete="new-password"
              bind:value={pwConfirm}
              disabled={pwSaving}
            />
          </div>

          {#if pwError}
            <p class="form-error" role="alert">{pwError}</p>
          {/if}
          {#if pwSuccess}
            <p class="form-success" role="status">{pwSuccess}</p>
          {/if}

          <div class="form-actions">
            <button type="submit" class="btn-primary" disabled={pwSaving}>
              {pwSaving ? t('principalDetail.password.changing') : t('principalDetail.password.submit')}
            </button>
          </div>
        </form>
      {/if}

      <!-- Two-factor -->
      {#if activeTab === 'totp'}
        <div class="tab-section">
          {#if principalDetail.totpEnabled}
            <!-- Enrolled: show disable form -->
            <div class="totp-status totp-enabled">
              {t('principalDetail.totp.enabledStatus')}
            </div>
            {#if !totpDisableSuccess}
              <div class="totp-disable-form">
                <label for="totp-disable-pw" class="label">{t('principalDetail.totp.currentPasswordLabel')}</label>
                <div class="input-row">
                  <input
                    id="totp-disable-pw"
                    type="password"
                    class="input"
                    bind:value={totpDisablePassword}
                    disabled={totpLoading}
                  />
                  <button
                    type="button"
                    class="btn-danger"
                    onclick={disableTOTP}
                    disabled={totpLoading || !totpDisablePassword}
                  >
                    {totpLoading ? t('principalDetail.totp.disabling') : t('principalDetail.totp.disable')}
                  </button>
                </div>
                {#if totpDisableError}
                  <p class="form-error" role="alert">{totpDisableError}</p>
                {/if}
              </div>
            {:else}
              <p class="form-success">{totpDisableSuccess}</p>
            {/if}
          {:else}
            <!-- Not enrolled -->
            <div class="totp-status">
              {t('principalDetail.totp.disabledStatus')}
            </div>

            {#if !totpProvisioningUri}
              {#if totpConfirmSuccess}
                <p class="form-success">{totpConfirmSuccess}</p>
              {:else}
                <button
                  type="button"
                  class="btn-primary"
                  onclick={startTOTPEnroll}
                  disabled={totpLoading}
                >
                  {totpLoading ? t('principalDetail.totp.enrolling') : t('principalDetail.totp.enable')}
                </button>
              {/if}
            {:else}
              <!-- Show provisioning QR + confirm -->
              <div class="totp-enroll">
                <p class="totp-instructions">
                  {t('principalDetail.totp.instructions')}
                </p>

                {#if totpQrSvg}
                  <div class="totp-qr" aria-label={t('principalDetail.totp.qrAriaLabel')}>
                    <!-- eslint-disable-next-line svelte/no-at-html-tags -->
                    {@html totpQrSvg}
                  </div>
                {/if}

                <p class="totp-uri-label">{t('principalDetail.totp.provisioningUriLabel')}</p>
                <input
                  type="text"
                  class="input input-mono totp-uri-input"
                  readonly
                  value={totpProvisioningUri}
                  onclick={(e) => (e.currentTarget as HTMLInputElement).select()}
                  aria-label={t('principalDetail.totp.provisioningUriAriaLabel')}
                />

                <div class="totp-confirm-row">
                  <input
                    type="text"
                    class="input input-narrow"
                    inputmode="numeric"
                    autocomplete="one-time-code"
                    pattern="[0-9]*"
                    placeholder={t('principalDetail.totp.codePlaceholder')}
                    bind:value={totpCode}
                    disabled={totpLoading}
                    aria-label={t('principalDetail.totp.codeAriaLabel')}
                  />
                  <button
                    type="button"
                    class="btn-primary"
                    onclick={confirmTOTP}
                    disabled={totpLoading || !totpCode}
                  >
                    {totpLoading ? t('principalDetail.totp.confirming') : t('principalDetail.totp.confirm')}
                  </button>
                </div>

                {#if totpConfirmError}
                  <p class="form-error" role="alert">{totpConfirmError}</p>
                {/if}
              </div>
            {/if}
          {/if}
        </div>
      {/if}

      <!-- API keys -->
      {#if activeTab === 'apikeys'}
        <div class="tab-section">
          <div class="section-header">
            <h3 class="section-title">{t('principalDetail.apikeys.title')}</h3>
            <button type="button" class="btn-primary" onclick={openKeyDialog}>
              {t('principalDetail.apikeys.new')}
            </button>
          </div>

          {#if principalDetail.apiKeys.length > 0}
            <div class="keys-list">
              {#each principalDetail.apiKeys as key (key.id)}
                <div class="key-row">
                  <div class="key-info">
                    <span class="key-name">{key.name}</span>
                    {#if key.scopes && key.scopes.length > 0}
                      <div class="chips">
                        {#each key.scopes as scope (scope)}
                          <span class="chip chip-scope">{scope}</span>
                        {/each}
                      </div>
                    {/if}
                    <span class="key-meta">{t('principalDetail.apikeys.created', { date: formatDate(key.created_at) })}</span>
                    {#if key.last_used_at}
                      <span class="key-meta">{t('principalDetail.apikeys.lastUsed', { date: formatDate(key.last_used_at) })}</span>
                    {/if}
                  </div>
                  <button
                    type="button"
                    class="btn-danger-sm"
                    onclick={() => void revokeKey(key.id)}
                  >
                    {t('principalDetail.apikeys.revoke')}
                  </button>
                </div>
              {/each}
            </div>
          {:else}
            <p class="empty-state">{t('principalDetail.apikeys.empty')}</p>
          {/if}
        </div>
      {/if}

      <!-- OIDC -->
      {#if activeTab === 'oidc'}
        <div class="tab-section">
          {#if oidcLinkError}
            <p class="form-error" role="alert">{oidcLinkError}</p>
          {/if}

          <!-- Linked providers -->
          <div class="section-header">
            <h3 class="section-title">{t('principalDetail.oidc.linkedTitle')}</h3>
          </div>

          {#if principalDetail.oidcLinks.length > 0}
            <div class="oidc-list">
              {#each principalDetail.oidcLinks as link (link.provider_id)}
                <div class="oidc-row">
                  <div class="oidc-info">
                    <span class="oidc-provider">{link.provider_name ?? link.provider_id}</span>
                    <span class="oidc-subject mono">{link.subject}</span>
                    <span class="key-meta">{t('principalDetail.oidc.linked', { date: formatDate(link.linked_at) })}</span>
                  </div>
                  <button
                    type="button"
                    class="btn-danger-sm"
                    onclick={() => void unlinkOIDC(link.provider_id)}
                  >
                    {t('principalDetail.oidc.unlink')}
                  </button>
                </div>
              {/each}
            </div>
          {:else}
            <p class="empty-state">{t('principalDetail.oidc.noneLinked')}</p>
          {/if}

          <!-- Available (unlinked) providers -->
          {#if oidcProvidersLoading}
            <div class="spinner" role="status" aria-label={t('principalDetail.oidc.loadingProviders')} style="margin-top: var(--spacing-05)"></div>
          {:else if unlinkedProviders.length > 0}
            <div class="section-header" style="margin-top: var(--spacing-06)">
              <h3 class="section-title">{t('principalDetail.oidc.linkProviderTitle')}</h3>
            </div>
            <div class="oidc-list">
              {#each unlinkedProviders as prov (prov.id)}
                <div class="oidc-row">
                  <div class="oidc-info">
                    <span class="oidc-provider">{prov.name}</span>
                    <span class="key-meta">{prov.issuer}</span>
                  </div>
                  <button
                    type="button"
                    class="btn-secondary"
                    onclick={() => void linkOIDC(prov.id)}
                  >
                    {t('principalDetail.oidc.link')}
                  </button>
                </div>
              {/each}
            </div>
          {:else if oidcProviders.length > 0}
            <p class="empty-state" style="margin-top: var(--spacing-05)">{t('principalDetail.oidc.allLinked')}</p>
          {:else}
            <p class="empty-state" style="margin-top: var(--spacing-05)">{t('principalDetail.oidc.noneConfigured')}</p>
          {/if}
        </div>
      {/if}

      <!-- Danger zone -->
      {#if activeTab === 'danger'}
        <div class="danger-zone">
          <h3 class="danger-title">{t('principalDetail.danger.title')}</h3>
          <p class="danger-desc">
            {t('principalDetail.danger.description')}
          </p>
          <form class="danger-form" onsubmit={deletePrincipal} novalidate>
            <label for="pd-delete-confirm" class="label">
              {t('principalDetail.danger.confirmLabel')}
              <strong>{principalDetail.principal.canonical_email}</strong>
            </label>
            <div class="input-row">
              <input
                id="pd-delete-confirm"
                type="email"
                class="input"
                placeholder={principalDetail.principal.canonical_email}
                bind:value={deleteConfirmEmail}
                disabled={deleteSubmitting}
                autocomplete="off"
              />
              <button
                type="submit"
                class="btn-danger"
                disabled={deleteSubmitting || deleteConfirmEmail !== principalDetail.principal.canonical_email}
              >
                {deleteSubmitting ? t('principalDetail.danger.deleting') : t('principalDetail.danger.delete')}
              </button>
            </div>
            {#if deleteError}
              <p class="form-error" role="alert">{deleteError}</p>
            {/if}
          </form>
        </div>
      {/if}
    </Tabs>
  {/if}
</div>

<!-- New API key dialog -->
<Dialog bind:open={keyDialogOpen} title={t('principalDetail.apikeys.dialogTitle')}>
  {#if newKeyPlaintext && !newKeyConfirmed}
    <!-- Show plaintext key with copy button -->
    <div class="key-reveal">
      <p class="key-reveal-warning">
        {t('principalDetail.apikeys.revealWarning')}
      </p>
      <div class="key-reveal-display">
        <input
          type="text"
          class="input input-mono"
          readonly
          value={newKeyPlaintext}
          onclick={(e) => (e.currentTarget as HTMLInputElement).select()}
          aria-label={t('principalDetail.apikeys.newKeyAriaLabel')}
        />
        <button type="button" class="btn-secondary" onclick={copyKey}>
          {t('principalDetail.apikeys.copy')}
        </button>
      </div>
      <div class="form-actions" style="margin-top: var(--spacing-05)">
        <button
          type="button"
          class="btn-primary"
          onclick={() => {
            newKeyConfirmed = true;
            keyDialogOpen = false;
          }}
        >
          {t('principalDetail.apikeys.savedConfirm')}
        </button>
      </div>
    </div>
  {:else}
    <form class="create-form" onsubmit={createKey} novalidate>
      <div class="field">
        <label for="key-label" class="label">{t('principalDetail.apikeys.label')}</label>
        <input
          id="key-label"
          type="text"
          class="input"
          placeholder={t('principalDetail.apikeys.labelPlaceholder')}
          bind:value={keyLabel}
          disabled={keyCreating}
        />
      </div>

      <div class="field">
        <span class="label">{t('principalDetail.apikeys.scopes')}</span>
        <div class="scope-checks">
          {#each availableScopes as scope (scope)}
            <label class="check-label">
              <input
                type="checkbox"
                checked={keyScopes.includes(scope)}
                onchange={() => toggleScope(scope)}
                disabled={keyCreating}
              />
              {scope}
            </label>
          {/each}
        </div>
      </div>

      {#if keyCreateError}
        <p class="form-error" role="alert">{keyCreateError}</p>
      {/if}

      <div class="form-actions">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => { keyDialogOpen = false; }}
          disabled={keyCreating}
        >
          {t('common.cancel')}
        </button>
        <button type="submit" class="btn-primary" disabled={keyCreating}>
          {keyCreating ? t('principalDetail.apikeys.creating') : t('principalDetail.apikeys.createKey')}
        </button>
      </div>
    </form>
  {/if}
</Dialog>

<style>
  .detail-page {
    max-width: 800px;
  }

  .page-header {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-05);
    margin-bottom: var(--spacing-07);
    flex-wrap: wrap;
  }

  .back-btn {
    background: none;
    border: none;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    padding: var(--spacing-02) 0;
    flex-shrink: 0;
    margin-top: 4px;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .back-btn:hover {
    opacity: 0.8;
  }

  .header-info {
    flex: 1;
    min-width: 0;
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
    font-family: var(--font-mono);
    word-break: break-all;
  }

  .page-subtitle {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: var(--spacing-01) 0 0;
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
    margin-top: 6px;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .page-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-06);
  }

  /* Tab content forms */
  .tab-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
    max-width: 480px;
  }

  .tab-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }
  .input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .input-mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    letter-spacing: 0.03em;
  }
  .input-narrow {
    width: 140px;
    min-width: 0;
  }

  .field-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
  }

  .field-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .form-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .form-success {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-success);
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-success) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-success);
  }

  .admin-override-note {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
    font-style: italic;
  }

  .form-actions {
    display: flex;
    justify-content: flex-start;
    gap: var(--spacing-04);
  }

  .meta-list {
    margin: var(--spacing-07) 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-05);
  }
  .meta-row {
    display: flex;
    gap: var(--spacing-06);
    font-size: var(--type-body-compact-01-size);
  }
  .meta-row dt {
    color: var(--text-secondary);
    width: 120px;
    flex-shrink: 0;
  }
  .meta-row dd {
    color: var(--text-primary);
    margin: 0;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  /* TOTP */
  .totp-status {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
  }
  .totp-enabled {
    border-color: var(--support-success);
    color: var(--support-success);
    background: color-mix(in srgb, var(--support-success) 8%, transparent);
  }

  .totp-disable-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    max-width: 400px;
  }

  .totp-enroll {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 480px;
  }

  .totp-instructions {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .totp-qr {
    display: inline-block;
    background: #fff;
    padding: var(--spacing-03);
    border-radius: var(--radius-md);
    width: 200px;
    height: 200px;
    overflow: hidden;
  }
  :global(.totp-qr svg) {
    display: block;
    width: 100%;
    height: 100%;
  }

  .totp-uri-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    margin: 0;
  }

  .totp-uri-input {
    font-size: var(--type-code-01-size);
  }

  .totp-confirm-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .input-row {
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
  }

  /* Section headings within tabs */
  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  .section-title {
    font-size: var(--type-heading-01-size);
    line-height: var(--type-heading-01-line);
    font-weight: var(--type-heading-01-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .keys-list, .oidc-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .key-row, .oidc-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-05);
    padding: var(--spacing-04) var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .key-info, .oidc-info {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    min-width: 0;
  }

  .key-name {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .key-meta {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .oidc-provider {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .oidc-subject {
    font-size: var(--type-code-01-size);
    font-family: var(--font-mono);
    color: var(--text-secondary);
  }

  .chips {
    display: flex;
    gap: var(--spacing-02);
    flex-wrap: wrap;
  }

  .chip {
    display: inline-block;
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    line-height: 18px;
  }

  .chip-scope {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
  }

  .empty-state {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
  }

  .scope-checks {
    display: flex;
    gap: var(--spacing-05);
    flex-wrap: wrap;
  }

  /* Key reveal */
  .key-reveal {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }
  .key-reveal-warning {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-warning);
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-warning) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-warning);
  }
  .key-reveal-display {
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
  }

  /* Danger zone */
  .danger-zone {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 480px;
    padding: var(--spacing-05);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-lg);
    background: color-mix(in srgb, var(--support-error) 5%, transparent);
  }

  .danger-title {
    font-size: var(--type-heading-01-size);
    line-height: var(--type-heading-01-line);
    font-weight: var(--type-heading-01-weight);
    color: var(--support-error);
    margin: 0;
  }

  .danger-desc {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .danger-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  /* Shared buttons */
  .btn-primary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-secondary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--layer-02);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--support-error);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-danger:hover:not(:disabled) {
    filter: brightness(0.9);
  }
  .btn-danger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger-sm {
    padding: var(--spacing-02) var(--spacing-04);
    background: none;
    color: var(--support-error);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    cursor: pointer;
    border: 1px solid var(--support-error);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    flex-shrink: 0;
    white-space: nowrap;
  }
  .btn-danger-sm:hover {
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
  }

  /* Dialog form reuse */
  .create-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }
</style>
