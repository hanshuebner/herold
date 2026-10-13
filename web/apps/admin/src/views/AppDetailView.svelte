<script lang="ts">
  /**
   * One registered app (REQ-APPS-31..36).
   *
   * - The signing secret of a create or rotate, shown once with a copy
   *   button until the operator dismisses it.
   * - Review: the manifest's extensions with full endpoint URLs and scope
   *   explanations, the manifest warnings, and Approve, which quotes the
   *   reviewed digest; a 409 reloads the page with a notice.
   * - Manifest update: approved and pending manifests side by side with
   *   the server's diff marked, Approve update, and Refresh manifest.
   * - Access: All users, or an explicit set of principals.
   * - Rotate secret, Disable/Enable, Remove; Rotate and Remove confirm
   *   first. Elevation is requested by the API client's step-up flow.
   */
  import { untrack } from 'svelte';
  import Dialog from '../lib/ui/Dialog.svelte';
  import ManifestExtensions from '../lib/apps/ManifestExtensions.svelte';
  import DataKeyMissingNotice from '../lib/apps/DataKeyMissingNotice.svelte';
  import {
    apps,
    appDetail,
    appDisplayName,
    hasManifestUpdate,
    manifestUnderReview,
  } from '../lib/apps/apps.svelte';
  import { apiGet } from '../lib/api/client';
  import { router } from '../lib/router/router.svelte';
  import { formatAbsolute, formatRelative } from '../lib/format';
  import { i18n, t } from '../lib/i18n/i18n.svelte';

  interface Props {
    id: string;
  }
  let { id }: Props = $props();

  $effect(() => {
    const appId = id;
    untrack(() => {
      void appDetail.load(appId);
    });
  });

  let app = $derived(appDetail.app && String(appDetail.app.id) === id ? appDetail.app : null);
  let update = $derived(app ? hasManifestUpdate(app) : false);
  let reviewManifest = $derived(app ? manifestUnderReview(app) : null);
  let awaitingApproval = $derived(app ? app.pending_manifest !== null : false);

  let busy = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  async function run(name: string, op: () => Promise<{ ok: boolean; errorMessage: string | null }>): Promise<boolean> {
    busy = name;
    actionError = null;
    const res = await op();
    busy = null;
    if (!res.ok && res.errorMessage) actionError = res.errorMessage;
    if (res.ok) void apps.load();
    return res.ok;
  }

  // ── Secret ──────────────────────────────────────────────────────────
  let copied = $state(false);

  async function copySecret(): Promise<void> {
    if (!appDetail.secret) return;
    try {
      await navigator.clipboard.writeText(appDetail.secret);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      copied = false;
    }
  }

  // ── Access ──────────────────────────────────────────────────────────
  let principalInput = $state('');
  let principalOptions = $state<string[]>([]);
  let optionsLoaded = false;

  async function loadPrincipalOptions(): Promise<void> {
    if (optionsLoaded) return;
    optionsLoaded = true;
    const res = await apiGet<{ items: Array<{ canonical_email: string }> }>('/api/v1/principals?limit=500');
    if (res.ok && res.data) {
      principalOptions = (res.data.items ?? []).map((p) => p.canonical_email);
    }
  }

  async function setAllPrincipals(all: boolean): Promise<void> {
    await run('access', () => appDetail.setAccess({ all_principals: all }));
  }

  async function addPrincipal(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const entry = principalInput.trim();
    if (!app || !entry) return;
    const current = (app.principals ?? []).map((p) => p.id);
    if (await run('access', () => appDetail.setAccess({ principals: [...current, entry] }))) {
      principalInput = '';
    }
  }

  async function removePrincipal(pid: number): Promise<void> {
    if (!app) return;
    const rest = (app.principals ?? []).map((p) => p.id).filter((x) => x !== pid);
    await run('access', () => appDetail.setAccess({ principals: rest }));
  }

  // ── Confirmations ───────────────────────────────────────────────────
  let confirmRotate = $state(false);
  let confirmRemove = $state(false);

  async function doRotate(): Promise<void> {
    confirmRotate = false;
    await run('rotate', () => appDetail.rotateSecret());
  }

  async function doRemove(): Promise<void> {
    confirmRemove = false;
    if (await run('remove', () => appDetail.remove())) {
      router.navigate('/apps');
    }
  }

  function shortDigest(d: string): string {
    return d.length > 16 ? `${d.slice(0, 16)}…` : d;
  }
</script>

<div class="detail-page">
  <div class="page-header">
    <button type="button" class="back-btn" onclick={() => router.navigate('/apps')}>
      {t('apps.detail.back')}
    </button>
    {#if app}
      <div class="header-info">
        <h1 class="page-title">{appDisplayName(app, i18n.locale)}</h1>
        <span class="badge badge-{app.state}">{t(`apps.state.${app.state}`)}</span>
        {#if update}
          <span class="badge badge-update">{t('apps.badge.updatePending')}</span>
        {/if}
      </div>
    {:else if appDetail.status === 'loading'}
      <div class="spinner" role="status" aria-label={t('common.loading')}></div>
    {/if}
  </div>

  {#if appDetail.dataKeyMissing}
    <DataKeyMissingNotice />
  {:else if appDetail.status === 'error'}
    <div class="page-error" role="alert">{appDetail.errorMessage}</div>
  {/if}

  {#if appDetail.notice}
    <div class="notice" role="status" data-testid="apps-notice">{appDetail.notice}</div>
  {/if}
  {#if actionError}
    <div class="page-error" role="alert">{actionError}</div>
  {/if}

  {#if app}
    {#if appDetail.secret}
      <div class="secret-box" data-testid="apps-secret">
        <h2 class="section-title">{t('apps.secret.title')}</h2>
        <p class="secret-warning">{t('apps.secret.warning')}</p>
        <div class="secret-row">
          <input
            class="input input-mono"
            readonly
            value={appDetail.secret}
            aria-label={t('apps.secret.title')}
            onfocus={(e) => (e.currentTarget as HTMLInputElement).select()}
          />
          <button type="button" class="btn-secondary" onclick={() => void copySecret()}>
            {copied ? t('common.copied') : t('common.copy')}
          </button>
        </div>
        <button type="button" class="btn-ghost-sm" onclick={() => appDetail.dismissSecret()}>
          {t('apps.secret.dismiss')}
        </button>
      </div>
    {/if}

    <div class="section">
      <h2 class="section-title">{t('apps.detail.info')}</h2>
      <dl class="info-grid">
        <dt>{t('apps.table.appKey')}</dt>
        <dd class="mono">{app.app_key}</dd>
        <dt>{t('apps.table.origin')}</dt>
        <dd class="mono">{app.origin}</dd>
        <dt>{t('apps.detail.created')}</dt>
        <dd>{formatAbsolute(app.created_at)}</dd>
        <dt>{t('apps.table.lastInvoked')}</dt>
        <dd>{app.last_invoked_at ? formatRelative(app.last_invoked_at) : t('apps.never')}</dd>
        {#if app.manifest_digest}
          <dt>{t('apps.detail.approvedDigest')}</dt>
          <dd class="mono">{shortDigest(app.manifest_digest)}</dd>
        {/if}
      </dl>
    </div>

    <div class="section" data-testid="apps-review">
      <div class="section-header">
        <h2 class="section-title">
          {#if update}
            {t('apps.update.title')}
          {:else if awaitingApproval}
            {t('apps.review.title')}
          {:else}
            {t('apps.manifest.title')}
          {/if}
        </h2>
        <button
          type="button"
          class="btn-secondary"
          disabled={busy !== null}
          onclick={() => void run('refresh', () => appDetail.refresh())}
        >
          {busy === 'refresh' ? t('apps.refresh.running') : t('apps.refresh.button')}
        </button>
      </div>

      {#if awaitingApproval}
        <p class="section-desc">{update ? t('apps.update.desc') : t('apps.review.desc')}</p>
      {/if}

      {#if appDetail.warnings.length > 0}
        <div class="warnings" role="note">
          <p class="warnings-title">{t('apps.review.warnings')}</p>
          <ul>
            {#each appDetail.warnings as w, i (i)}
              <li>{w}</li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if update && app.manifest && app.pending_manifest}
        <div class="diff-grid" data-testid="apps-diff">
          <div class="diff-col">
            <h3 class="col-title">{t('apps.update.approved')}</h3>
            <ManifestExtensions manifest={app.manifest} origin={app.origin} diff={app.pending_diff} side="approved" />
          </div>
          <div class="diff-col">
            <h3 class="col-title">{t('apps.update.pending')}</h3>
            <ManifestExtensions
              manifest={app.pending_manifest}
              origin={app.origin}
              diff={app.pending_diff}
              side="pending"
            />
          </div>
        </div>
      {:else if reviewManifest}
        <ManifestExtensions manifest={reviewManifest} origin={app.origin} />
      {/if}

      {#if awaitingApproval}
        <div class="approve-row">
          <span class="digest">
            {t('apps.review.digest')} <span class="mono">{shortDigest(app.pending_manifest_digest)}</span>
          </span>
          <button
            type="button"
            class="btn-primary"
            disabled={busy !== null}
            onclick={() => void run('approve', () => appDetail.approve(app.pending_manifest_digest))}
          >
            {update ? t('apps.update.approve') : t('apps.review.approve')}
          </button>
        </div>
      {/if}
    </div>

    <div class="section">
      <h2 class="section-title">{t('apps.access.title')}</h2>
      <p class="section-desc">{t('apps.access.desc')}</p>
      <label class="checkbox">
        <input
          type="checkbox"
          checked={app.all_principals}
          disabled={busy !== null}
          onchange={(e) => void setAllPrincipals((e.currentTarget as HTMLInputElement).checked)}
        />
        {t('apps.access.allToggle')}
      </label>

      {#if !app.all_principals}
        <div class="chip-list" aria-label={t('apps.access.principals')}>
          {#each app.principals ?? [] as p (p.id)}
            <span class="chip">
              <span class="mono">{p.email}</span>
              <button
                type="button"
                class="chip-remove"
                aria-label={t('apps.access.remove', { email: p.email })}
                disabled={busy !== null}
                onclick={() => void removePrincipal(p.id)}
              >
                &#10005;
              </button>
            </span>
          {:else}
            <p class="empty-state">{t('apps.access.none')}</p>
          {/each}
        </div>
        <form class="inline-add-form" onsubmit={addPrincipal} novalidate>
          <input
            type="text"
            class="input input-mono"
            list="app-principal-options"
            placeholder={t('apps.access.addPlaceholder')}
            aria-label={t('apps.access.addPlaceholder')}
            bind:value={principalInput}
            onfocus={() => void loadPrincipalOptions()}
            disabled={busy !== null}
          />
          <datalist id="app-principal-options">
            {#each principalOptions as email (email)}
              <option value={email}></option>
            {/each}
          </datalist>
          <button type="submit" class="btn-secondary" disabled={busy !== null || !principalInput.trim()}>
            {t('apps.access.add')}
          </button>
        </form>
      {/if}
    </div>

    <div class="section">
      <h2 class="section-title">{t('apps.manage.title')}</h2>
      <div class="manage-row">
        <button type="button" class="btn-secondary" disabled={busy !== null} onclick={() => (confirmRotate = true)}>
          {t('apps.manage.rotate')}
        </button>
        {#if app.state === 'active'}
          <button
            type="button"
            class="btn-secondary"
            disabled={busy !== null}
            onclick={() => void run('disable', () => appDetail.setEnabled(false))}
          >
            {t('apps.manage.disable')}
          </button>
        {:else if app.state === 'disabled'}
          <button
            type="button"
            class="btn-secondary"
            disabled={busy !== null}
            onclick={() => void run('enable', () => appDetail.setEnabled(true))}
          >
            {t('apps.manage.enable')}
          </button>
        {/if}
        <button type="button" class="btn-danger" disabled={busy !== null} onclick={() => (confirmRemove = true)}>
          {t('apps.manage.remove')}
        </button>
      </div>
    </div>
  {/if}
</div>

<Dialog bind:open={confirmRotate} title={t('apps.manage.rotateTitle')}>
  <p class="dialog-text">{t('apps.manage.rotateConfirm')}</p>
  <div class="form-actions">
    <button type="button" class="btn-secondary" onclick={() => (confirmRotate = false)}>{t('common.cancel')}</button>
    <button type="button" class="btn-primary" onclick={() => void doRotate()}>{t('apps.manage.rotate')}</button>
  </div>
</Dialog>

<Dialog bind:open={confirmRemove} title={t('apps.manage.removeTitle')}>
  <p class="dialog-text">{t('apps.manage.removeConfirm', { name: app ? appDisplayName(app, i18n.locale) : '' })}</p>
  <div class="form-actions">
    <button type="button" class="btn-secondary" onclick={() => (confirmRemove = false)}>{t('common.cancel')}</button>
    <button type="button" class="btn-danger" onclick={() => void doRemove()}>{t('apps.manage.remove')}</button>
  </div>
</Dialog>

<style>
  .detail-page {
    max-width: 1100px;
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
  }

  .header-info {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-03);
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
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

  .notice {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--interactive) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--interactive);
    margin-bottom: var(--spacing-06);
  }

  .secret-box {
    padding: var(--spacing-05);
    background: color-mix(in srgb, var(--support-warning) 12%, transparent);
    border: 1px solid var(--support-warning);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-07);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    align-items: flex-start;
  }

  .secret-warning {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .secret-row {
    display: flex;
    gap: var(--spacing-03);
    width: 100%;
    max-width: 720px;
  }
  .secret-row .input {
    flex: 1;
  }

  .section {
    margin-bottom: var(--spacing-08);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-03);
  }

  .section-title {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-02);
  }

  .section-desc {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-04);
    max-width: 720px;
  }

  .info-grid {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--spacing-02) var(--spacing-05);
    margin: 0;
  }
  .info-grid dt {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }
  .info-grid dd {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .warnings {
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    border-left: 3px solid var(--support-warning);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
  }
  .warnings-title {
    margin: 0 0 var(--spacing-02);
    font-weight: 600;
  }
  .warnings ul {
    margin: 0;
    padding-left: var(--spacing-05);
  }

  .diff-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-05);
  }
  @media (max-width: 800px) {
    .diff-grid {
      grid-template-columns: 1fr;
    }
  }

  .col-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.02em;
    margin: 0 0 var(--spacing-03);
  }

  .approve-row {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--spacing-04);
    margin-top: var(--spacing-05);
  }

  .digest {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .checkbox {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    margin-bottom: var(--spacing-04);
    cursor: pointer;
  }

  .chip-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    margin-bottom: var(--spacing-04);
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
  }

  .chip-remove {
    background: none;
    border: none;
    color: var(--text-secondary);
    cursor: pointer;
    font-size: 12px;
    line-height: 1;
    padding: 0;
  }
  .chip-remove:hover:not(:disabled) {
    color: var(--support-error);
  }

  .inline-add-form {
    display: flex;
    gap: var(--spacing-03);
    max-width: 480px;
  }
  .inline-add-form .input {
    flex: 1;
  }

  .manage-row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-03);
  }

  .empty-state {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .badge {
    display: inline-block;
    padding: 1px var(--spacing-02);
    font-size: var(--type-helper-text-01-size);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.02em;
    border-radius: var(--radius-sm);
    white-space: nowrap;
  }
  .badge-active {
    color: var(--support-success);
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
  }
  .badge-pending_approval,
  .badge-update {
    color: var(--text-primary);
    background: color-mix(in srgb, var(--support-warning) 25%, transparent);
  }
  .badge-disabled {
    color: var(--text-secondary);
    background: var(--layer-02);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
  }
  .input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }
  .input-mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
  }

  .dialog-text {
    margin: 0 0 var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-04);
  }

  .btn-primary,
  .btn-secondary,
  .btn-danger {
    padding: var(--spacing-03) var(--spacing-06);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    cursor: pointer;
    white-space: nowrap;
  }
  .btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
    font-weight: 600;
    border: none;
  }
  .btn-secondary {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 500;
    border: 1px solid var(--border-subtle-01);
  }
  .btn-danger {
    background: var(--support-error);
    color: var(--text-on-color);
    font-weight: 600;
    border: none;
  }
  .btn-primary:hover:not(:disabled),
  .btn-danger:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-primary:disabled,
  .btn-secondary:disabled,
  .btn-danger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-ghost-sm {
    padding: var(--spacing-01) var(--spacing-03);
    background: none;
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
  }
  .btn-ghost-sm:hover {
    background: var(--layer-02);
  }
</style>
