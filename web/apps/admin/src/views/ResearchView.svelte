<script lang="ts">
  import { queue, type QueueStateFilter, type QueueItem } from '../lib/queue/queue.svelte';

  // Research uses the same queue state singleton but with different UX.
  // Load on mount so the operator can start with a full list.
  $effect(() => {
    if (queue.status === 'idle') {
      void queue.load();
    }
  });

  const stateOptions: { value: QueueStateFilter; label: string }[] = [
    { value: 'all', label: 'All states' },
    { value: 'queued', label: 'Queued' },
    { value: 'deferred', label: 'Deferred' },
    { value: 'held', label: 'Held' },
    { value: 'failed', label: 'Failed' },
    { value: 'inflight', label: 'Inflight' },
    { value: 'done', label: 'Done' },
  ];

  function onStateChange(e: Event): void {
    const val = (e.currentTarget as HTMLSelectElement).value as QueueStateFilter;
    queue.stateFilter = val;
    void queue.load();
  }

  function reloadFromTop(): void {
    void queue.load();
  }

  // Expanded row ID for inline detail.
  let expandedId = $state<string | null>(null);

  function toggleExpand(id: string): void {
    expandedId = expandedId === id ? null : id;
  }

  function formatDate(iso: string | null | undefined): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit',
    });
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

  function detailRows(item: QueueItem): Array<{ label: string; value: string; mono: boolean }> {
    const rows: Array<{ label: string; value: string; mono: boolean }> = [
      { label: 'ID', value: item.id, mono: true },
      { label: 'State', value: item.state, mono: false },
      { label: 'Principal ID', value: item.principal_id, mono: true },
      { label: 'Sender', value: item.mail_from, mono: true },
      { label: 'Recipient', value: item.rcpt_to, mono: true },
      { label: 'Envelope ID', value: item.envelope_id, mono: true },
      { label: 'Attempts', value: String(item.attempts), mono: false },
    ];
    if (item.created_at) rows.push({ label: 'Created', value: formatDate(item.created_at), mono: false });
    if (item.last_attempt_at) rows.push({ label: 'Last attempt', value: formatDate(item.last_attempt_at), mono: false });
    if (item.next_attempt_at) rows.push({ label: 'Next attempt', value: formatDate(item.next_attempt_at), mono: false });
    if (item.last_error) rows.push({ label: 'Last error', value: item.last_error, mono: true });
    return rows;
  }
</script>

<div class="research-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">Email research</h1>
      {#if queue.status === 'loading'}
        <div class="spinner" role="status" aria-label="Loading"></div>
      {/if}
    </div>
  </div>

  <!-- Search and state filter -->
  <div class="search-bar">
    <input
      type="search"
      class="search-input"
      placeholder="Find messages by sender or recipient..."
      bind:value={queue.search}
      aria-label="Search by sender or recipient"
    />
    <select
      class="select"
      value={queue.stateFilter}
      onchange={onStateChange}
      aria-label="State filter"
    >
      {#each stateOptions as opt (opt.value)}
        <option value={opt.value}>{opt.label}</option>
      {/each}
    </select>
    <button
      type="button"
      class="btn-secondary"
      onclick={reloadFromTop}
      disabled={queue.status === 'loading'}
    >
      Reload
    </button>
  </div>

  {#if isFiltering}
    <p class="filter-note">
      Showing {totalFiltered} of {totalLoaded} loaded{queue.hasMore ? ' -- load more to widen the search' : ''}.
    </p>
  {/if}

  {#if queue.errorMessage && queue.status === 'error'}
    <div class="page-error" role="alert">{queue.errorMessage}</div>
  {/if}

  {#if queue.status === 'ready' || queue.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-expand"></th>
            <th class="col-state">State</th>
            <th class="col-from">Sender</th>
            <th class="col-to">Recipient</th>
            <th class="col-attempts">Att.</th>
            <th class="col-created">Created</th>
          </tr>
        </thead>
        <tbody>
          {#each queue.filtered as item (item.id)}
            <tr
              class="table-row"
              class:expanded={expandedId === item.id}
              onclick={() => toggleExpand(item.id)}
            >
              <td class="col-expand">
                <span class="expand-indicator" aria-hidden="true">
                  {expandedId === item.id ? 'v' : '>'}
                </span>
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
              <td class="col-created">
                <span class="time-text">{formatDate(item.created_at)}</span>
              </td>
            </tr>
            {#if expandedId === item.id}
              <tr class="expand-row">
                <td colspan="6" class="expand-cell">
                  <dl class="item-detail">
                    {#each detailRows(item) as row (row.label)}
                      <div class="item-detail-row">
                        <dt>{row.label}</dt>
                        <dd class:mono={row.mono}>{row.value}</dd>
                      </div>
                    {/each}
                  </dl>
                </td>
              </tr>
            {/if}
          {:else}
            <tr>
              <td colspan="6" class="empty-row">
                {queue.search ? 'No messages match the search.' : 'No queue items found.'}
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
          {queue.status === 'loading' ? 'Loading...' : 'Load more'}
        </button>
      </div>
    {/if}
  {:else if queue.status !== 'loading' && queue.status !== 'idle'}
    <p class="empty-state">No messages found.</p>
  {/if}
</div>

<style>
  .research-page {
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

  /* Search bar */
  .search-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-04);
  }

  .search-input {
    flex: 1;
    min-width: 260px;
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
  .search-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
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
  }
  .select:focus {
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
    padding: var(--spacing-03) var(--spacing-04);
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
  .table-row:hover {
    background: var(--layer-02);
  }
  .table-row.expanded {
    background: var(--layer-02);
    border-bottom: none;
  }

  .table td {
    padding: var(--spacing-03) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    vertical-align: middle;
  }

  .col-expand { width: 28px; }
  .col-state { width: 9%; }
  .col-from { width: 26%; }
  .col-to { width: 26%; }
  .col-attempts { width: 6%; text-align: center; }
  .col-created { width: 15%; white-space: nowrap; }

  .expand-indicator {
    font-size: 10px;
    color: var(--text-helper);
    font-family: var(--font-mono);
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }
  .small {
    font-size: calc(var(--type-code-01-size) * 0.9);
    word-break: break-all;
  }

  .time-text {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  /* Expanded detail row */
  .expand-row {
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .expand-cell {
    padding: 0 var(--spacing-05) var(--spacing-04) calc(28px + var(--spacing-04)) !important;
    background: var(--layer-02);
  }

  .item-detail {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 1px;
    background: var(--border-subtle-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
    margin: var(--spacing-03) 0;
    max-width: 700px;
  }

  .item-detail-row {
    display: contents;
  }

  .item-detail-row dt,
  .item-detail-row dd {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .item-detail-row dt {
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .item-detail-row dd {
    color: var(--text-primary);
    word-break: break-all;
  }
  .item-detail-row dd.mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
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
    padding: var(--spacing-07) var(--spacing-04) !important;
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

  /* Buttons */
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
