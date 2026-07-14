<script lang="ts">
  import { oidcProviders } from '../lib/oidc-providers/providers.svelte';
  import { router } from '../lib/router/router.svelte';
  import { formatDateOnly } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  $effect(() => {
    if (oidcProviders.status === 'idle') {
      void oidcProviders.load();
    }
  });

  function formatDate(iso: string): string {
    return formatDateOnly(iso);
  }
</script>

<div class="providers-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('oidcProviders.title')}</h1>
      {#if oidcProviders.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
  </div>

  <p class="hint">{t('oidcProviders.hint')}</p>

  {#if oidcProviders.errorMessage && oidcProviders.status === 'error'}
    <div class="page-error" role="alert">{oidcProviders.errorMessage}</div>
  {/if}

  {#if oidcProviders.status === 'ready' || oidcProviders.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-name">{t('oidcProviders.table.name')}</th>
            <th class="col-issuer">{t('oidcProviders.table.issuer')}</th>
            <th class="col-trusted">{t('oidcProviders.table.trusted')}</th>
            <th class="col-created">{t('oidcProviders.table.created')}</th>
          </tr>
        </thead>
        <tbody>
          {#each oidcProviders.items as provider (provider.id)}
            <tr class="table-row" onclick={() => router.navigate(`/oidc-providers/${provider.id}`)}>
              <td class="col-name">
                <span class="mono">{provider.name}</span>
              </td>
              <td class="col-issuer">
                <span class="mono">{provider.issuer}</span>
              </td>
              <td class="col-trusted">
                {#if provider.authz_trusted}
                  <span class="badge badge-trusted">{t('oidcProviders.trustedBadge')}</span>
                {:else}
                  <span class="badge badge-untrusted">{t('oidcProviders.untrustedBadge')}</span>
                {/if}
              </td>
              <td class="col-created">{formatDate(provider.created_at)}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="4" class="empty-row">{t('oidcProviders.empty')}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if oidcProviders.status !== 'loading' && oidcProviders.status !== 'idle'}
    <p class="empty-state">{t('oidcProviders.empty')}</p>
  {/if}
</div>

<style>
  .providers-page {
    max-width: 1000px;
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
    overflow: hidden;
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
    background: var(--layer-01);
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

  .col-name { width: 25%; }
  .col-issuer { width: 40%; }
  .col-trusted { width: 15%; }
  .col-created { width: 20%; white-space: nowrap; }

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
  }
  .badge-trusted {
    color: var(--support-success);
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
  }
  .badge-untrusted {
    color: var(--text-secondary);
    background: var(--layer-02);
  }

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
  }

  .empty-state {
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
  }
</style>
