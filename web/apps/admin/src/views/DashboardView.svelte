<script lang="ts">
  import { dashboard } from '../lib/dashboard/dashboard.svelte';
  import { router } from '../lib/router/router.svelte';

  // Load on mount.
  $effect(() => {
    void dashboard.load();
  });

  // Refresh when the window regains focus.
  $effect(() => {
    function onFocus() {
      void dashboard.load();
    }
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  });

  function formatRelative(isoDate: string): string {
    const date = new Date(isoDate);
    if (isNaN(date.getTime())) return isoDate;
    const diffMs = Date.now() - date.getTime();
    const diffSec = Math.floor(diffMs / 1000);
    if (diffSec < 60) return `${diffSec}s ago`;
    const diffMin = Math.floor(diffSec / 60);
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h ago`;
    const diffDay = Math.floor(diffHr / 24);
    return `${diffDay}d ago`;
  }
</script>

<div class="dashboard">
  <div class="page-header">
    <h1 class="page-title">Dashboard</h1>
    {#if dashboard.status === 'loading'}
      <div class="spinner" role="status" aria-label="Loading"></div>
    {/if}
  </div>

  <div class="cards">
    <!-- Queue summary card -->
    <div class="card">
      <div class="card-header">
        <h2 class="card-title">Queue</h2>
        <button
          type="button"
          class="card-link"
          onclick={() => router.navigate('/queue')}
        >
          View all
        </button>
      </div>

      {#if dashboard.queueError}
        <p class="inline-error">{dashboard.queueError}</p>
      {:else}
        <p class="card-stat">{dashboard.queueTotal}</p>
        <p class="card-stat-label">active messages</p>
        {#if dashboard.queueStats}
          <dl class="stat-list">
            {#each Object.entries(dashboard.queueStats) as [state, count] (state)}
              {#if count !== undefined && count > 0}
                <div class="stat-row">
                  <dt class="stat-key">{state}</dt>
                  <dd class="stat-val">{count}</dd>
                </div>
              {/if}
            {/each}
          </dl>
        {/if}
      {/if}
    </div>

    <!-- Recent activity card -->
    <div class="card">
      <div class="card-header">
        <h2 class="card-title">Recent activity</h2>
        <button
          type="button"
          class="card-link"
          onclick={() => router.navigate('/audit')}
        >
          View all
        </button>
      </div>

      {#if dashboard.auditError}
        <p class="inline-error">{dashboard.auditError}</p>
      {:else if dashboard.auditEntries.length === 0 && dashboard.status === 'ready'}
        <p class="empty">No recent activity.</p>
      {:else}
        <ul class="audit-list">
          {#each dashboard.auditEntries.slice(0, 8) as entry (entry.id)}
            <li class="audit-row">
              <span class="audit-action">{entry.action}</span>
              {#if entry.principal_email}
                <span class="audit-who">{entry.principal_email}</span>
              {/if}
              <span class="audit-when">{formatRelative(entry.created_at)}</span>
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    <!-- Domains overview card -->
    <div class="card">
      <div class="card-header">
        <h2 class="card-title">Domains</h2>
        <button
          type="button"
          class="card-link"
          onclick={() => router.navigate('/domains')}
        >
          View all
        </button>
      </div>

      {#if dashboard.domainsError}
        <p class="inline-error">{dashboard.domainsError}</p>
      {:else}
        <p class="card-stat">{dashboard.domains.length}</p>
        <p class="card-stat-label">local domain{dashboard.domains.length !== 1 ? 's' : ''}</p>
        {#if dashboard.domains.length > 0}
          <ul class="domain-list">
            {#each dashboard.domains.slice(0, 5) as domain (domain.name)}
              <li class="domain-item">{domain.name}</li>
            {/each}
            {#if dashboard.domains.length > 5}
              <li class="domain-item domain-more">+ {dashboard.domains.length - 5} more</li>
            {/if}
          </ul>
        {:else if dashboard.status === 'ready'}
          <p class="empty">No domains configured.</p>
        {/if}
      {/if}
    </div>

    <!-- Client-log stats card (REQ-ADM-233) -->
    <div class="card">
      <div class="card-header">
        <h2 class="card-title">Client logs</h2>
        <button
          type="button"
          class="card-link"
          onclick={() => router.navigate('/clientlog')}
        >
          View all
        </button>
      </div>

      {#if dashboard.clientlogStatsError}
        <p class="inline-error">{dashboard.clientlogStatsError}</p>
      {:else if dashboard.clientlogStats}
        <div class="stat-cols">
          <div class="stat-col">
            <h3 class="stat-col-title">Received</h3>
            <dl class="stat-list">
              {#each Object.entries(dashboard.clientlogStats.received_total) as [key, val] (key)}
                <div class="stat-row">
                  <dt class="stat-key">{key}</dt>
                  <dd class="stat-val">{val}</dd>
                </div>
              {:else}
                <div class="stat-row">
                  <dt class="stat-key">none</dt>
                  <dd class="stat-val">0</dd>
                </div>
              {/each}
            </dl>
          </div>
          <div class="stat-col">
            <h3 class="stat-col-title">Ring buffer</h3>
            <dl class="stat-list">
              {#each Object.entries(dashboard.clientlogStats.ring_buffer_rows) as [key, val] (key)}
                <div class="stat-row">
                  <dt class="stat-key">{key}</dt>
                  <dd class="stat-val">{val}</dd>
                </div>
              {:else}
                <div class="stat-row">
                  <dt class="stat-key">none</dt>
                  <dd class="stat-val">0</dd>
                </div>
              {/each}
            </dl>
          </div>
        </div>
      {:else if dashboard.status === 'ready'}
        <p class="empty">No client-log data.</p>
      {/if}
    </div>
  </div>
</div>

<style>
  .dashboard {
    max-width: 1200px;
  }

  .page-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-07);
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

  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: var(--spacing-06);
  }

  .card {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .card-header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-02);
  }

  .card-title {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .card-link {
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    white-space: nowrap;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .card-link:hover {
    opacity: 0.8;
  }

  .card-stat {
    font-size: 36px;
    line-height: 1.1;
    font-weight: 300;
    color: var(--text-primary);
    margin: 0;
    font-family: var(--font-sans);
  }

  .card-stat-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .stat-list {
    margin: var(--spacing-04) 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .stat-row {
    display: flex;
    justify-content: space-between;
    gap: var(--spacing-04);
  }
  .stat-key {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }
  .stat-val {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .audit-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .audit-row {
    display: flex;
    gap: var(--spacing-03);
    align-items: baseline;
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
  }
  .audit-action {
    color: var(--text-primary);
    font-weight: 500;
    flex-shrink: 0;
    max-width: 120px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .audit-who {
    color: var(--text-secondary);
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .audit-when {
    color: var(--text-helper);
    flex-shrink: 0;
  }

  .domain-list {
    list-style: none;
    margin: var(--spacing-03) 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .domain-item {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-family: var(--font-mono);
  }
  .domain-more {
    color: var(--text-helper);
    font-family: var(--font-sans);
    font-style: italic;
  }

  .inline-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
    padding: var(--spacing-03);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .empty {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
  }

  /* Client-log stats two-column layout */
  .stat-cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-04);
  }

  .stat-col {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .stat-col-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    margin: 0;
  }
</style>
