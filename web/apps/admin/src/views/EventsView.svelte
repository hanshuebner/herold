<script lang="ts">
  import { events } from '../lib/events/events.svelte';
  import { formatRelative, formatAbsolute } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  $effect(() => {
    if (events.status === 'idle') {
      void events.load();
    }
  });

  function applyFilters(): void {
    void events.load();
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

<div class="events-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('events.title')}</h1>
      {#if events.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
  </div>

  <!-- Filter -->
  <div class="filter-row">
    <input
      type="text"
      class="input"
      placeholder={t('events.filter.actionPlaceholder')}
      bind:value={events.actionFilter}
      onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
      aria-label={t('events.filter.actionAriaLabel')}
    />
    <input
      type="text"
      class="input"
      placeholder={t('events.filter.actorPlaceholder')}
      bind:value={events.actorIdFilter}
      onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
      aria-label={t('events.filter.actorAriaLabel')}
    />
    <div class="date-pair">
      <label for="events-since" class="date-label">{t('events.filter.since')}</label>
      <input
        id="events-since"
        type="datetime-local"
        class="input"
        bind:value={events.sinceFilter}
        aria-label={t('events.filter.sinceAriaLabel')}
      />
    </div>
    <div class="date-pair">
      <label for="events-until" class="date-label">{t('events.filter.until')}</label>
      <input
        id="events-until"
        type="datetime-local"
        class="input"
        bind:value={events.untilFilter}
        aria-label={t('events.filter.untilAriaLabel')}
      />
    </div>
    <button
      type="button"
      class="btn-primary"
      onclick={applyFilters}
      disabled={events.status === 'loading'}
    >
      {events.status === 'loading' ? t('common.loading') : t('events.filter.apply')}
    </button>
    <button
      type="button"
      class="btn-secondary"
      onclick={() => {
        events.actionFilter = '';
        events.actorIdFilter = '';
        events.sinceFilter = '';
        events.untilFilter = '';
        applyFilters();
      }}
    >
      {t('events.filter.clear')}
    </button>
  </div>

  {#if events.errorMessage && events.status === 'error'}
    <div class="page-error" role="alert">{events.errorMessage}</div>
  {/if}

  {#if events.status === 'ready' || events.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-time">{t('events.table.when')}</th>
            <th class="col-action">{t('events.table.action')}</th>
            <th class="col-actor">{t('events.table.actor')}</th>
            <th class="col-domain">{t('events.table.domain')}</th>
            <th class="col-outcome">{t('events.table.outcome')}</th>
            <th class="col-subject">{t('events.table.subject')}</th>
            <th class="col-message">{t('events.table.message')}</th>
          </tr>
        </thead>
        <tbody>
          {#each events.items as entry (entry.id)}
            <tr class="table-row">
              <td class="col-time">
                <span class="relative-time" title={formatAbsolute(entry.at)}>{formatRelative(entry.at)}</span>
              </td>
              <td class="col-action">
                <span class="action-text">{entry.action}</span>
              </td>
              <td class="col-actor">
                <span class="mono small">{entry.actor_id}</span>
              </td>
              <td class="col-domain">
                <span class="mono small">{entry.domain}</span>
              </td>
              <td class="col-outcome">
                <span class="chip {outcomeChipClass(entry.outcome)}">{entry.outcome}</span>
              </td>
              <td class="col-subject">
                <span class="mono small">{entry.subject}</span>
              </td>
              <td class="col-message">
                {#if entry.message}
                  <span class="message-text" title={entry.message}>{truncate(entry.message)}</span>
                {/if}
              </td>
            </tr>
          {:else}
            <tr>
              <td colspan="7" class="empty-row">{t('events.empty')}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if events.hasMore}
      <div class="load-more">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => void events.loadMore()}
          disabled={events.status === 'loading'}
        >
          {events.status === 'loading' ? t('common.loading') : t('events.loadMore')}
        </button>
      </div>
    {/if}
  {:else if events.status !== 'loading' && events.status !== 'idle'}
    <p class="empty-state">{t('events.empty')}</p>
  {/if}
</div>

<style>
  .events-page {
    max-width: 1400px;
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

  .col-time    { width: 8%; white-space: nowrap; }
  .col-action  { width: 15%; }
  .col-actor   { width: 14%; }
  .col-domain  { width: 12%; }
  .col-outcome { width: 8%; }
  .col-subject { width: 14%; }
  .col-message { width: 29%; }

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
