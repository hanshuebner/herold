<script lang="ts">
  import { domains } from '../lib/domains/domains.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';

  let createOpen = $state(false);
  let createName = $state('');
  let createError = $state<string | null>(null);
  let createSubmitting = $state(false);

  $effect(() => {
    if (domains.status === 'idle') {
      void domains.load();
    }
  });

  function openCreate(): void {
    createName = '';
    createError = null;
    createOpen = true;
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  }

  async function handleCreate(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (createSubmitting) return;
    createError = null;
    createSubmitting = true;

    const result = await domains.create({ name: createName.trim().toLowerCase() });
    createSubmitting = false;

    if (!result.ok) {
      createError = result.errorMessage;
      return;
    }

    createOpen = false;
    createName = '';
  }
</script>

<div class="domains-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">Domains</h1>
      {#if domains.status === 'loading'}
        <div class="spinner" role="status" aria-label="Loading"></div>
      {/if}
    </div>
    <div class="page-header-right">
      <button type="button" class="btn-primary" onclick={openCreate}>
        New domain
      </button>
    </div>
  </div>

  {#if domains.errorMessage && domains.status === 'error'}
    <div class="page-error" role="alert">{domains.errorMessage}</div>
  {/if}

  {#if domains.status === 'ready' || domains.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-name">Name</th>
            <th class="col-created">Created</th>
          </tr>
        </thead>
        <tbody>
          {#each domains.items as domain (domain.name)}
            <tr class="table-row" onclick={() => router.navigate(`/domains/${domain.name}`)}>
              <td class="col-name">
                <span class="domain-name">{domain.name}</span>
              </td>
              <td class="col-created">{formatDate(domain.created_at)}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="2" class="empty-row">No domains found.</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if domains.hasMore}
      <div class="load-more">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => void domains.loadMore()}
          disabled={domains.status === 'loading'}
        >
          {domains.status === 'loading' ? 'Loading...' : 'Load more'}
        </button>
      </div>
    {/if}
  {:else if domains.status !== 'loading' && domains.status !== 'idle'}
    <p class="empty-state">No domains found.</p>
  {/if}
</div>

<Dialog bind:open={createOpen} title="New domain">
  <form class="create-form" onsubmit={handleCreate} novalidate>
    <div class="field">
      <label for="cd-name" class="label">Domain name</label>
      <input
        id="cd-name"
        type="text"
        class="input input-mono"
        placeholder="example.com"
        autocomplete="off"
        required
        bind:value={createName}
        disabled={createSubmitting}
      />
    </div>

    {#if createError}
      <p class="form-error" role="alert">{createError}</p>
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn-secondary"
        onclick={() => { createOpen = false; }}
        disabled={createSubmitting}
      >
        Cancel
      </button>
      <button type="submit" class="btn-primary" disabled={createSubmitting || !createName.trim()}>
        {createSubmitting ? 'Creating...' : 'Create domain'}
      </button>
    </div>
  </form>
</Dialog>

<style>
  .domains-page {
    max-width: 900px;
  }

  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-06);
  }

  .page-header-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .page-header-right {
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

  .col-name { width: 70%; }
  .col-created { width: 30%; white-space: nowrap; }

  .domain-name {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
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

  .load-more {
    margin-top: var(--spacing-05);
    text-align: center;
  }

  /* Create form */
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

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-04);
    padding-top: var(--spacing-02);
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
</style>
