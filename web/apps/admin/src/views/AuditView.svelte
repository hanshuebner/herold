<script lang="ts">
  import { audit } from '../lib/audit/audit.svelte';

  $effect(() => {
    if (audit.status === 'idle') {
      void audit.load();
    }
  });

  function applyFilters(): void {
    void audit.load();
  }

  function formatRelative(iso: string | null | undefined): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    const diff = Date.now() - d.getTime();

    if (diff < 60_000) return 'just now';
    if (diff < 3_600_000) return `${Math.round(diff / 60_000)}m ago`;
    if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)}h ago`;
    return `${Math.round(diff / 86_400_000)}d ago`;
  }

  function outcomeChipClass(outcome: string): string {
    switch (outcome) {
      case 'success': return 'chip-green';
      case 'failure': return 'chip-red';
      default: return 'chip-grey';
    }
  }

  function truncate(s: string, max = 80): string {
    if (s.length <= max) return s;
    return s.slice(0, max) + '...';
  }
</script>

<div class="audit-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">Audit log</h1>
      {#if audit.status === 'loading'}
        <div class="spinner" role="status" aria-label="Loading"></div>
      {/if}
    </div>
  </div>

  <!-- Filters -->
  <div class="filter-row">
    <input
      type="text"
      class="input"
      placeholder="Action contains..."
      bind:value={audit.actionFilter}
      onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
      aria-label="Filter by action"
    />
    <input
      type="text"
      class="input"
      placeholder="Principal ID"
      bind:value={audit.principalIdFilter}
      onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
      aria-label="Filter by principal ID"
    />
    <div class="date-pair">
      <label for="audit-since" class="date-label">Since</label>
      <input
        id="audit-since"
        type="datetime-local"
        class="input"
        bind:value={audit.sinceFilter}
        aria-label="Since date"
      />
    </div>
    <div class="date-pair">
      <label for="audit-until" class="date-label">Until</label>
      <input
        id="audit-until"
        type="datetime-local"
        class="input"
        bind:value={audit.untilFilter}
        aria-label="Until date"
      />
    </div>
    <button
      type="button"
      class="btn-primary"
      onclick={applyFilters}
      disabled={audit.status === 'loading'}
    >
      {audit.status === 'loading' ? 'Loading...' : 'Apply'}
    </button>
    <button
      type="button"
      class="btn-secondary"
      onclick={() => {
        audit.actionFilter = '';
        audit.principalIdFilter = '';
        audit.sinceFilter = '';
        audit.untilFilter = '';
        applyFilters();
      }}
    >
      Clear
    </button>
  </div>

  {#if audit.errorMessage && audit.status === 'error'}
    <div class="page-error" role="alert">{audit.errorMessage}</div>
  {/if}

  {#if audit.status === 'ready' || audit.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-time">When</th>
            <th class="col-actor">Actor</th>
            <th class="col-action">Action</th>
            <th class="col-subject">Subject</th>
            <th class="col-outcome">Outcome</th>
            <th class="col-message">Message</th>
          </tr>
        </thead>
        <tbody>
          {#each audit.items as entry (entry.id)}
            <tr class="table-row">
              <td class="col-time">
                <span class="relative-time" title={entry.at}>{formatRelative(entry.at)}</span>
              </td>
              <td class="col-actor">
                <span class="mono small">{entry.actor_kind}:{entry.actor_id}</span>
              </td>
              <td class="col-action">
                <span class="action-text">{entry.action}</span>
              </td>
              <td class="col-subject">
                <span class="mono small">{entry.subject}</span>
              </td>
              <td class="col-outcome">
                <span class="chip {outcomeChipClass(entry.outcome)}">{entry.outcome}</span>
              </td>
              <td class="col-message">
                {#if entry.message}
                  <span class="message-text" title={entry.message}>{truncate(entry.message)}</span>
                {/if}
              </td>
            </tr>
          {:else}
            <tr>
              <td colspan="6" class="empty-row">No audit entries found.</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if audit.hasMore}
      <div class="load-more">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => void audit.loadMore()}
          disabled={audit.status === 'loading'}
        >
          {audit.status === 'loading' ? 'Loading...' : 'Load more'}
        </button>
      </div>
    {/if}
  {:else if audit.status !== 'loading' && audit.status !== 'idle'}
    <p class="empty-state">No audit entries found.</p>
  {/if}
</div>

<style>
  .audit-page {
    max-width: 1200px;
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

  /* Filters */
  .filter-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-05);
  }

  .input {
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
  .input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }

  .date-pair {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
  }

  .date-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
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
    padding: var(--spacing-03) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    vertical-align: top;
  }

  .col-time { width: 8%; white-space: nowrap; }
  .col-actor { width: 15%; }
  .col-action { width: 15%; }
  .col-subject { width: 18%; }
  .col-outcome { width: 8%; }
  .col-message { width: 36%; }

  .relative-time {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: default;
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }
  .small {
    font-size: calc(var(--type-code-01-size) * 0.9);
    word-break: break-all;
  }

  .action-text {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-secondary);
  }

  .message-text {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
    max-width: 400px;
    cursor: default;
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
  .chip-green {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
  }
  .chip-red {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
  }
  .chip-grey {
    background: color-mix(in srgb, var(--text-helper) 15%, transparent);
    color: var(--text-helper);
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
