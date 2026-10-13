<script lang="ts">
  /**
   * Apps page (REQ-APPS-30, 31, 36): every registered app with its
   * state, a badge for a pending manifest update, who it is enabled for
   * and when it was last used; plus the Add app dialog. A successful add
   * opens the app's page, which shows the signing secret once and the
   * review.
   */
  import Dialog from '../lib/ui/Dialog.svelte';
  import DataKeyMissingNotice from '../lib/apps/DataKeyMissingNotice.svelte';
  import { apps, appDisplayName, type AppRecord } from '../lib/apps/apps.svelte';
  import { router } from '../lib/router/router.svelte';
  import { formatRelative } from '../lib/format';
  import { i18n, t } from '../lib/i18n/i18n.svelte';

  $effect(() => {
    if (apps.status === 'idle') {
      void apps.load();
    }
  });

  let addOpen = $state(false);
  let origin = $state('');
  let submitting = $state(false);
  let addError = $state<string | null>(null);
  let addErrors = $state<string[]>([]);

  function openAdd(): void {
    origin = '';
    addError = null;
    addErrors = [];
    addOpen = true;
  }

  async function handleAdd(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (!origin.trim()) return;
    submitting = true;
    addError = null;
    addErrors = [];
    const res = await apps.create(origin.trim());
    submitting = false;
    if (!res.ok || !res.app) {
      addError = res.errorMessage;
      addErrors = res.errors ?? [];
      return;
    }
    addOpen = false;
    router.navigate(`/apps/${res.app.id}`);
  }

  function principalsLabel(app: AppRecord): string {
    if (app.all_principals) return t('apps.access.all');
    const n = (app.principals ?? []).length;
    return n === 1 ? t('apps.access.one') : t('apps.access.count', { n });
  }
</script>

<div class="apps-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('apps.title')}</h1>
      {#if apps.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
    {#if !apps.dataKeyMissing}
      <button type="button" class="btn-primary" onclick={openAdd}>{t('apps.add')}</button>
    {/if}
  </div>

  <p class="hint">{t('apps.hint')}</p>

  {#if apps.dataKeyMissing}
    <DataKeyMissingNotice />
  {:else if apps.errorMessage && apps.status === 'error'}
    <div class="page-error" role="alert">{apps.errorMessage}</div>
  {/if}

  {#if !apps.dataKeyMissing && (apps.status === 'ready' || apps.items.length > 0)}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th>{t('apps.table.name')}</th>
            <th>{t('apps.table.appKey')}</th>
            <th>{t('apps.table.origin')}</th>
            <th>{t('apps.table.state')}</th>
            <th>{t('apps.table.access')}</th>
            <th>{t('apps.table.lastInvoked')}</th>
          </tr>
        </thead>
        <tbody>
          {#each apps.items as app (app.id)}
            <tr class="table-row" data-app-id={app.id} onclick={() => router.navigate(`/apps/${app.id}`)}>
              <td>
                <a class="row-link" href={`#/apps/${app.id}`}>{appDisplayName(app, i18n.locale)}</a>
              </td>
              <td class="mono">{app.app_key}</td>
              <td class="mono">{app.origin}</td>
              <td>
                <span class="badge badge-{app.state}">{t(`apps.state.${app.state}`)}</span>
                {#if app.pending_manifest && app.manifest}
                  <span class="badge badge-update">{t('apps.badge.updatePending')}</span>
                {/if}
              </td>
              <td>{principalsLabel(app)}</td>
              <td>{app.last_invoked_at ? formatRelative(app.last_invoked_at) : t('apps.never')}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="6" class="empty-row">{t('apps.empty')}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<Dialog bind:open={addOpen} title={t('apps.addDialog.title')} width="520px">
  <form class="create-form" onsubmit={handleAdd} novalidate>
    <div class="field">
      <label for="app-origin" class="label">{t('apps.addDialog.origin')}</label>
      <input
        id="app-origin"
        type="url"
        class="input input-mono"
        placeholder="https://forum.example.org"
        bind:value={origin}
        disabled={submitting}
        required
      />
      <p class="field-hint">{t('apps.addDialog.originHint')}</p>
    </div>

    {#if addError}
      <div class="form-error" role="alert">
        <p>{addError}</p>
        {#if addErrors.length > 0}
          <ul class="problem-list">
            {#each addErrors as err, i (i)}
              <li class="mono">{err}</li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}

    <div class="form-actions">
      <button type="button" class="btn-secondary" onclick={() => (addOpen = false)} disabled={submitting}>
        {t('common.cancel')}
      </button>
      <button type="submit" class="btn-primary" disabled={submitting || !origin.trim()}>
        {submitting ? t('apps.addDialog.adding') : t('apps.addDialog.submit')}
      </button>
    </div>
  </form>
</Dialog>

<style>
  .apps-page {
    max-width: 1100px;
  }

  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-03);
  }

  .page-header-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
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
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0 0 var(--spacing-06);
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

  .table-wrapper {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    overflow-x: auto;
  }

  .table {
    width: 100%;
    border-collapse: collapse;
  }

  .table thead tr {
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .table th {
    text-align: left;
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .table-row {
    cursor: pointer;
    border-bottom: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .table-row:last-child {
    border-bottom: none;
  }
  .table-row:hover {
    background: var(--layer-02);
  }

  .table td {
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    vertical-align: middle;
  }

  .row-link {
    color: var(--text-primary);
    font-weight: 600;
    text-decoration: none;
  }
  .row-link:hover {
    text-decoration: underline;
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

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
  }

  .create-form {
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

  .field-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
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

  .form-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }
  .form-error p {
    margin: 0;
  }

  .problem-list {
    margin: var(--spacing-02) 0 0;
    padding-left: var(--spacing-05);
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-04);
  }

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
    white-space: nowrap;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
</style>
