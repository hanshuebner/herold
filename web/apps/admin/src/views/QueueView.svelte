<script lang="ts">
  import { queue, type QueueStateFilter } from '../lib/queue/queue.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';
  import { formatRelative } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  let flushDialogOpen = $state(false);
  let flushing = $state(false);
  let flushResult = $state<{ flushed: number } | null>(null);
  let flushError = $state<string | null>(null);

  $effect(() => {
    if (queue.status === 'idle') {
      void queue.load();
    }
  });

  const stateOptions: { value: QueueStateFilter; labelKey: string }[] = [
    { value: 'all', labelKey: 'queue.state.all' },
    { value: 'queued', labelKey: 'queue.state.queued' },
    { value: 'deferred', labelKey: 'queue.state.deferred' },
    { value: 'held', labelKey: 'queue.state.held' },
    { value: 'failed', labelKey: 'queue.state.failed' },
    { value: 'inflight', labelKey: 'queue.state.inflight' },
    { value: 'done', labelKey: 'queue.state.done' },
  ];

  function onStateChange(e: Event): void {
    const val = (e.currentTarget as HTMLSelectElement).value as QueueStateFilter;
    queue.stateFilter = val;
    void queue.load();
  }

  async function confirmFlush(): Promise<void> {
    flushing = true;
    flushError = null;
    flushResult = null;
    const result = await queue.flush();
    flushing = false;
    if (!result.ok) {
      flushError = result.errorMessage;
    } else {
      flushResult = { flushed: result.flushed ?? 0 };
    }
  }

  function openFlushDialog(): void {
    flushResult = null;
    flushError = null;
    flushDialogOpen = true;
  }

  function stateChipClass(state: string): string {
    switch (state) {
      case 'queued': return 'chip-blue';
      case 'deferred': return 'chip-amber';
      case 'held': return 'chip-grey';
      case 'failed': return 'chip-red';
      case 'inflight': return 'chip-green';
      case 'done': return 'chip-grey';
      default: return 'chip-grey';
    }
  }

  const totalLoaded = $derived(queue.items.length);
  const totalFiltered = $derived(queue.filtered.length);
  const isFiltering = $derived(queue.search.trim() !== '');
</script>

<div class="queue-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('queue.title')}</h1>
      {#if queue.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
    <div class="page-header-right">
      <button type="button" class="btn-secondary" onclick={openFlushDialog}>
        {t('queue.flushDeferred')}
      </button>
    </div>
  </div>

  <!-- Filters -->
  <div class="filter-bar">
    <select
      class="select"
      value={queue.stateFilter}
      onchange={onStateChange}
      aria-label={t('queue.stateFilterAriaLabel')}
    >
      {#each stateOptions as opt (opt.value)}
        <option value={opt.value}>{t(opt.labelKey)}</option>
      {/each}
    </select>

    <input
      type="search"
      class="search-input"
      placeholder={t('queue.searchPlaceholder')}
      bind:value={queue.search}
      aria-label={t('queue.searchAriaLabel')}
    />
  </div>

  {#if queue.errorMessage && queue.status === 'error'}
    <div class="page-error" role="alert">{queue.errorMessage}</div>
  {/if}

  {#if isFiltering}
    <p class="filter-note">
      {t('queue.filterNote', {
        filtered: totalFiltered,
        total: totalLoaded,
        more: queue.hasMore ? t('queue.filterNote.loadMoreHint') : '',
      })}
    </p>
  {/if}

  {#if queue.status === 'ready' || queue.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-id">{t('queue.table.id')}</th>
            <th class="col-state">{t('queue.table.state')}</th>
            <th class="col-from">{t('queue.table.sender')}</th>
            <th class="col-to">{t('queue.table.recipient')}</th>
            <th class="col-attempts">{t('queue.table.attempts')}</th>
            <th class="col-next">{t('queue.table.nextRetry')}</th>
          </tr>
        </thead>
        <tbody>
          {#each queue.filtered as item (item.id)}
            <tr class="table-row" onclick={() => router.navigate(`/queue/${item.id}`)}>
              <td class="col-id">
                <span class="mono">{item.id}</span>
              </td>
              <td class="col-state">
                <span class="chip {stateChipClass(item.state)}">{item.state}</span>
              </td>
              <td class="col-from">
                <span class="mono small">{item.mail_from}</span>
              </td>
              <td class="col-to">
                <span class="mono small">{item.rcpt_to}</span>
              </td>
              <td class="col-attempts">{item.attempts}</td>
              <td class="col-next">
                <span class="relative-time">{formatRelative(item.next_attempt_at)}</span>
              </td>
            </tr>
          {:else}
            <tr>
              <td colspan="6" class="empty-row">
                {queue.search ? t('queue.noMatch') : t('queue.empty')}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if queue.hasMore}
      <div class="load-more">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => void queue.loadMore()}
          disabled={queue.status === 'loading'}
        >
          {queue.status === 'loading' ? t('common.loading') : t('queue.loadMore')}
        </button>
      </div>
    {/if}
  {:else if queue.status !== 'loading' && queue.status !== 'idle'}
    <p class="empty-state">{t('queue.empty')}</p>
  {/if}
</div>

<!-- Flush deferred dialog -->
<Dialog bind:open={flushDialogOpen} title={t('queue.flush.dialogTitle')}>
  <div class="flush-dialog">
    {#if flushResult !== null}
      <p class="form-success" role="status">
        {flushResult.flushed !== 1
          ? t('queue.flush.resultPlural', { count: flushResult.flushed })
          : t('queue.flush.result', { count: flushResult.flushed })}
      </p>
      <div class="form-actions">
        <button type="button" class="btn-primary" onclick={() => { flushDialogOpen = false; }}>
          {t('queue.flush.close')}
        </button>
      </div>
    {:else}
      <p class="flush-desc">
        {t('queue.flush.description')}
      </p>
      {#if flushError}
        <p class="form-error" role="alert">{flushError}</p>
      {/if}
      <div class="form-actions">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => { flushDialogOpen = false; }}
          disabled={flushing}
        >
          {t('common.cancel')}
        </button>
        <button
          type="button"
          class="btn-primary"
          onclick={() => void confirmFlush()}
          disabled={flushing}
        >
          {flushing ? t('queue.flush.flushing') : t('queue.flushDeferred')}
        </button>
      </div>
    {/if}
  </div>
</Dialog>

<style>
  .queue-page {
    max-width: 1100px;
  }

  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-05);
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

  /* Filter bar */
  .filter-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-05);
  }

  .select {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    cursor: pointer;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .select:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }

  .search-input {
    flex: 1;
    min-width: 200px;
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .search-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }

  .filter-note {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0 0 var(--spacing-04);
  }

  .page-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-05);
  }

  /* Table */
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

  .col-id { width: 8%; }
  .col-state { width: 10%; }
  .col-from { width: 28%; }
  .col-to { width: 28%; }
  .col-attempts { width: 8%; text-align: center; }
  .col-next { width: 18%; white-space: nowrap; }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }
  .small {
    font-size: calc(var(--type-code-01-size) * 0.9);
    word-break: break-all;
  }

  .relative-time {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  /* Chips */
  .chip {
    display: inline-block;
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    line-height: 18px;
  }
  .chip-blue {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
  }
  .chip-amber {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: var(--support-warning);
  }
  .chip-grey {
    background: color-mix(in srgb, var(--text-helper) 15%, transparent);
    color: var(--text-helper);
  }
  .chip-red {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
  }
  .chip-green {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
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

  /* Flush dialog */
  .flush-dialog {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .flush-desc {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
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

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-04);
  }

  /* Buttons */
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
