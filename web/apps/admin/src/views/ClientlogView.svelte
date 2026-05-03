<script lang="ts">
  import { untrack } from 'svelte';
  import { clientlog } from '../lib/clientlog/clientlog.svelte';

  // Load on mount; reload when filters change via Apply button.
  $effect(() => {
    if (clientlog.status === 'idle') {
      untrack(() => void clientlog.load());
    }
  });

  function applyFilters(): void {
    // Reset to fresh load so cursor is cleared.
    clientlog.status = 'idle';
    void clientlog.load();
  }

  function clearFilters(): void {
    clientlog.resetFilters();
    clientlog.status = 'idle';
    void clientlog.load();
  }

  function formatRelative(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    const diff = Date.now() - d.getTime();
    if (diff < 60_000) return 'just now';
    if (diff < 3_600_000) return `${Math.round(diff / 60_000)}m ago`;
    if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)}h ago`;
    return `${Math.round(diff / 86_400_000)}d ago`;
  }

  function formatSkew(skewMs: number): string {
    if (Math.abs(skewMs) < 1000) return `${skewMs}ms`;
    return `${(skewMs / 1000).toFixed(1)}s`;
  }

  function kindChipClass(kind: string): string {
    switch (kind) {
      case 'error': return 'chip-red';
      case 'log': return 'chip-blue';
      case 'vital': return 'chip-green';
      default: return 'chip-grey';
    }
  }

  function levelChipClass(level: string): string {
    switch (level) {
      case 'error': return 'chip-red';
      case 'warn': return 'chip-yellow';
      case 'info': return 'chip-blue';
      default: return 'chip-grey';
    }
  }

  function livetailCountdown(until: string | null): string {
    if (!until) return '';
    const ms = new Date(until).getTime() - Date.now();
    if (ms <= 0) return 'expired';
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${s}s remaining`;
    return `${Math.floor(s / 60)}m ${s % 60}s remaining`;
  }
</script>

<div class="clientlog-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">Client logs</h1>
      {#if clientlog.status === 'loading'}
        <div class="spinner" role="status" aria-label="Loading"></div>
      {/if}
    </div>
  </div>

  <!-- Filters -->
  <div class="filter-section">
    <div class="filter-row">
      <!-- Slice toggle -->
      <div class="filter-field">
        <label class="filter-label" for="cl-slice">Slice</label>
        <select id="cl-slice" class="select" bind:value={clientlog.filters.slice}>
          <option value="auth">auth</option>
          <option value="public">public</option>
        </select>
      </div>

      <!-- App filter -->
      <div class="filter-field">
        <label class="filter-label" for="cl-app">App</label>
        <select id="cl-app" class="select" bind:value={clientlog.filters.app}>
          <option value="">all</option>
          <option value="suite">suite</option>
          <option value="admin">admin</option>
        </select>
      </div>

      <!-- Kind filter -->
      <div class="filter-field">
        <label class="filter-label" for="cl-kind">Kind</label>
        <select id="cl-kind" class="select" bind:value={clientlog.filters.kind}>
          <option value="">all</option>
          <option value="error">error</option>
          <option value="log">log</option>
          <option value="vital">vital</option>
        </select>
      </div>

      <!-- Level filter -->
      <div class="filter-field">
        <label class="filter-label" for="cl-level">Level</label>
        <select id="cl-level" class="select" bind:value={clientlog.filters.level}>
          <option value="">all</option>
          <option value="trace">trace</option>
          <option value="debug">debug</option>
          <option value="info">info</option>
          <option value="warn">warn</option>
          <option value="error">error</option>
        </select>
      </div>
    </div>

    <div class="filter-row">
      <!-- Since -->
      <div class="filter-field">
        <label class="filter-label" for="cl-since">Since</label>
        <input
          id="cl-since"
          type="datetime-local"
          class="input"
          bind:value={clientlog.filters.since}
          aria-label="Since date"
        />
      </div>

      <!-- Until -->
      <div class="filter-field">
        <label class="filter-label" for="cl-until">Until</label>
        <input
          id="cl-until"
          type="datetime-local"
          class="input"
          bind:value={clientlog.filters.until}
          aria-label="Until date"
        />
      </div>

      <!-- User -->
      <div class="filter-field">
        <label class="filter-label" for="cl-user">User</label>
        <input
          id="cl-user"
          type="text"
          class="input"
          placeholder="user ID"
          bind:value={clientlog.filters.user}
          aria-label="Filter by user ID"
        />
      </div>

      <!-- Route (text, displayed as monospace, no href per REQ-OPS-218) -->
      <div class="filter-field">
        <label class="filter-label" for="cl-route">Route</label>
        <input
          id="cl-route"
          type="text"
          class="input"
          placeholder="/mail/inbox"
          bind:value={clientlog.filters.route}
          aria-label="Filter by route"
        />
      </div>
    </div>

    <div class="filter-row">
      <!-- Free text -->
      <div class="filter-field filter-field-grow">
        <label class="filter-label" for="cl-text">Search</label>
        <input
          id="cl-text"
          type="text"
          class="input"
          placeholder="substring match on msg or stack"
          bind:value={clientlog.filters.text}
          onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
          aria-label="Free text search"
        />
      </div>

      <div class="filter-actions">
        <button
          type="button"
          class="btn-primary"
          onclick={applyFilters}
          disabled={clientlog.status === 'loading'}
        >
          {clientlog.status === 'loading' ? 'Loading...' : 'Apply'}
        </button>
        <button
          type="button"
          class="btn-secondary"
          onclick={clearFilters}
        >
          Clear
        </button>
      </div>
    </div>
  </div>

  {#if clientlog.errorMessage && clientlog.status === 'error'}
    <div class="page-error" role="alert">{clientlog.errorMessage}</div>
  {/if}

  <div class="main-layout">
    <!-- List -->
    <div class="list-panel" class:list-panel-narrow={clientlog.selected !== null}>
      {#if clientlog.rows.length > 0 || clientlog.status === 'ready'}
        <div class="table-wrapper">
          <table class="table">
            <thead>
              <tr>
                <th class="col-when">When</th>
                <th class="col-app">App</th>
                <th class="col-kind">Kind</th>
                <th class="col-level">Level</th>
                <th class="col-route">Route</th>
                <th class="col-msg">Message</th>
              </tr>
            </thead>
            <tbody>
              {#each clientlog.rows as row (row.id)}
                <tr
                  class="table-row"
                  class:selected={clientlog.selected?.id === row.id}
                  onclick={() => clientlog.openRow(row)}
                  role="button"
                  tabindex="0"
                  onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') clientlog.openRow(row); }}
                  aria-pressed={clientlog.selected?.id === row.id}
                >
                  <td class="col-when">
                    <span class="relative-time" title={row.server_ts}>
                      {formatRelative(row.server_ts)}
                    </span>
                  </td>
                  <td class="col-app">
                    <span class="mono small">{row.app}</span>
                  </td>
                  <td class="col-kind">
                    <span class="chip {kindChipClass(row.kind)}">{row.kind}</span>
                  </td>
                  <td class="col-level">
                    <span class="chip {levelChipClass(row.level)}">{row.level}</span>
                  </td>
                  <td class="col-route">
                    {#if row.route}
                      <!-- REQ-OPS-218: plain monospace text, no href -->
                      <span class="mono small route-text">{row.route}</span>
                    {/if}
                  </td>
                  <td class="col-msg">
                    <span class="msg-text" title={row.msg}>{row.msg}</span>
                  </td>
                </tr>
              {:else}
                <tr>
                  <td colspan="6" class="empty-row">No log entries found.</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        {#if clientlog.hasMore}
          <div class="load-more">
            <button
              type="button"
              class="btn-secondary"
              onclick={() => void clientlog.loadMore()}
              disabled={clientlog.status === 'loading'}
            >
              {clientlog.status === 'loading' ? 'Loading...' : 'Load more'}
            </button>
          </div>
        {/if}
      {:else if clientlog.status !== 'loading' && clientlog.status !== 'idle'}
        <p class="empty-state">No log entries found.</p>
      {/if}
    </div>

    <!-- Detail pane -->
    {#if clientlog.selected !== null}
      {@const row = clientlog.selected}
      <div class="detail-pane">
        <div class="detail-header">
          <h2 class="detail-title">Detail</h2>
          <button
            type="button"
            class="close-btn"
            onclick={() => clientlog.closePane()}
            aria-label="Close detail pane"
          >
            x
          </button>
        </div>

        <dl class="detail-list">
          <div class="detail-row">
            <dt>ID</dt>
            <dd class="mono">{row.id}</dd>
          </div>
          <div class="detail-row">
            <dt>Slice</dt>
            <dd class="mono">{row.slice}</dd>
          </div>
          <div class="detail-row">
            <dt>App</dt>
            <dd class="mono">{row.app}</dd>
          </div>
          <div class="detail-row">
            <dt>Kind</dt>
            <dd><span class="chip {kindChipClass(row.kind)}">{row.kind}</span></dd>
          </div>
          <div class="detail-row">
            <dt>Level</dt>
            <dd><span class="chip {levelChipClass(row.level)}">{row.level}</span></dd>
          </div>
          <div class="detail-row">
            <dt>Server time</dt>
            <dd class="mono">{row.server_ts}</dd>
          </div>
          <div class="detail-row">
            <dt>Client time</dt>
            <dd class="mono">{row.client_ts}</dd>
          </div>
          <div class="detail-row">
            <dt>Clock skew</dt>
            <dd class="mono">{formatSkew(row.clock_skew_ms)}</dd>
          </div>
          {#if row.user_id}
            <div class="detail-row">
              <dt>User</dt>
              <dd class="mono">{row.user_id}</dd>
            </div>
          {/if}
          {#if row.session_id}
            <div class="detail-row">
              <dt>Session</dt>
              <dd class="mono">{row.session_id}</dd>
            </div>
          {/if}
          {#if row.request_id}
            <div class="detail-row">
              <dt>Request ID</dt>
              <dd class="mono">{row.request_id}</dd>
            </div>
          {/if}
          {#if row.route}
            <!-- REQ-OPS-218: route displayed as plain monospace text, no href -->
            <div class="detail-row">
              <dt>Route</dt>
              <dd class="mono">{row.route}</dd>
            </div>
          {/if}
          <div class="detail-row">
            <dt>Build</dt>
            <dd class="mono">{row.build_sha}</dd>
          </div>
          <div class="detail-row">
            <dt>UA</dt>
            <dd class="mono small ua-text">{row.ua}</dd>
          </div>
          <div class="detail-row detail-row-full">
            <dt>Message</dt>
            <dd class="pre-wrap">{row.msg}</dd>
          </div>
        </dl>

        <!-- Stack trace with symbolication -->
        {#if row.stack}
          <div class="stack-section">
            <div class="stack-header">
              <span class="stack-label">Stack trace</span>
              {#if clientlog.symbolicateStatus !== 'done'}
                <button
                  type="button"
                  class="btn-small"
                  onclick={() => void clientlog.symbolicate()}
                  disabled={clientlog.symbolicateStatus === 'loading'}
                  aria-label="Symbolicate stack trace"
                >
                  {clientlog.symbolicateStatus === 'loading' ? 'Loading map...' : 'Symbolicate'}
                </button>
              {/if}
            </div>

            {#if clientlog.symbolicateStatus === 'error'}
              <div class="inline-warn">
                Symbolication failed: {clientlog.symbolicateError} -- showing raw stack.
              </div>
            {/if}

            <!-- Raw or symbolicated stack. REQ-OPS-218: pre + text, no innerHTML -->
            <pre class="stack-pre">{clientlog.symbolicatedStack ?? row.stack}</pre>
          </div>
        {/if}

        <!-- Breadcrumbs -->
        {#if row.payload?.breadcrumbs && row.payload.breadcrumbs.length > 0}
          <div class="breadcrumbs-section">
            <h3 class="section-title">Breadcrumbs</h3>
            <ol class="breadcrumb-list">
              {#each row.payload.breadcrumbs as bc, i (i)}
                <li class="breadcrumb-item">
                  <span class="mono small bc-kind">{bc.kind}</span>
                  <span class="mono small bc-ts">{bc.ts}</span>
                  {#if bc.kind === 'route' && bc.route}
                    <!-- REQ-OPS-218: route as plain text, no href -->
                    <span class="mono small bc-detail">{bc.route}</span>
                  {:else if bc.kind === 'fetch'}
                    <span class="mono small bc-detail">
                      {bc.method ?? ''} {bc.url_path ?? ''}
                      {#if bc.status !== undefined} {bc.status}{/if}
                    </span>
                  {:else if bc.kind === 'console' && bc.msg}
                    <span class="bc-detail bc-msg">{bc.msg}</span>
                  {/if}
                </li>
              {/each}
            </ol>
          </div>
        {/if}

        <!-- Timeline button (only when request_id is present) -->
        {#if row.request_id}
          <div class="action-section">
            <button
              type="button"
              class="btn-secondary"
              onclick={() => void clientlog.loadTimeline()}
              disabled={clientlog.timelineStatus === 'loading'}
            >
              {clientlog.timelineStatus === 'loading' ? 'Loading...' : 'View request timeline'}
            </button>
          </div>

          {#if clientlog.timelineStatus === 'error'}
            <div class="inline-error">{clientlog.timelineError}</div>
          {/if}

          {#if clientlog.timelineStatus === 'ready' && clientlog.timelineEntries.length > 0}
            <div class="timeline-section">
              <h3 class="section-title">Request timeline</h3>
              <p class="timeline-note">
                Client-side entries for request {row.request_id}.
                Server-side log entries will populate once the slog file-sink scan
                lands in a future release.
              </p>
              <ol class="timeline-list">
                {#each clientlog.timelineEntries as entry, i (i)}
                  <li class="timeline-item">
                    <span class="mono small tl-ts">{entry.effective_ts}</span>
                    <span class="tl-source chip {entry.source === 'client' ? 'chip-blue' : 'chip-grey'}">
                      {entry.source}
                    </span>
                    {#if entry.clientlog}
                      <span class="chip {kindChipClass(entry.clientlog.kind)}">{entry.clientlog.kind}</span>
                      <span class="tl-msg">{entry.clientlog.msg}</span>
                    {:else if entry.serverlog}
                      <span class="mono small tl-msg">
                        {JSON.stringify(entry.serverlog)}
                      </span>
                    {/if}
                  </li>
                {/each}
              </ol>
            </div>
          {/if}
        {/if}

        <!-- Live-tail toggle (only when session_id is present) -->
        {#if row.session_id}
          <div class="action-section">
            <h3 class="section-title">Live-tail</h3>
            {#if clientlog.livetailStatus === 'active' && clientlog.livetailUntil}
              <p class="livetail-countdown">
                Active -- {livetailCountdown(clientlog.livetailUntil)}
              </p>
              <!-- Disable button: cast breaks TypeScript narrowing so 'pending'
                   check remains valid during the brief transition. -->
              <button
                type="button"
                class="btn-danger"
                onclick={() => void clientlog.disableLivetail()}
                disabled={(clientlog.livetailStatus as string) === 'pending'}
              >
                {(clientlog.livetailStatus as string) === 'pending' ? 'Working...' : 'Disable live-tail'}
              </button>
            {:else}
              <button
                type="button"
                class="btn-secondary"
                onclick={() => void clientlog.enableLivetail('15m')}
                disabled={clientlog.livetailStatus === 'pending'}
              >
                {clientlog.livetailStatus === 'pending' ? 'Working...' : 'Enable live-tail (15 min)'}
              </button>
            {/if}
            {#if clientlog.livetailError}
              <div class="inline-error">{clientlog.livetailError}</div>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .clientlog-page {
    max-width: 1600px;
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
  .filter-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    margin-bottom: var(--spacing-05);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
  }

  .filter-row {
    display: flex;
    align-items: flex-end;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .filter-field-grow {
    flex: 1;
    min-width: 200px;
  }

  .filter-label {
    font-size: var(--type-label-01-size, 11px);
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .input,
  .select {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .input:focus,
  .select:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }

  .filter-actions {
    display: flex;
    gap: var(--spacing-03);
    align-items: flex-end;
    padding-bottom: 2px;
  }

  /* Main layout */
  .main-layout {
    display: flex;
    gap: var(--spacing-05);
    align-items: flex-start;
  }

  .list-panel {
    flex: 1;
    min-width: 0;
    transition: flex var(--duration-moderate-01) var(--easing-productive-enter);
  }

  .list-panel-narrow {
    flex: 0 0 55%;
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
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .table-row:last-child {
    border-bottom: none;
  }
  .table-row:hover {
    background: var(--layer-02);
  }
  .table-row.selected {
    background: color-mix(in srgb, var(--interactive) 10%, transparent);
    border-left: 3px solid var(--interactive);
  }

  .table td {
    padding: var(--spacing-03) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    vertical-align: top;
  }

  .col-when { width: 8%; white-space: nowrap; }
  .col-app { width: 6%; }
  .col-kind { width: 7%; }
  .col-level { width: 7%; }
  .col-route { width: 20%; }
  .col-msg { width: 52%; }

  .relative-time {
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

  .route-text {
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
    max-width: 220px;
  }

  .msg-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
    max-width: 500px;
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
    white-space: nowrap;
  }
  .chip-red {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
  }
  .chip-blue {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
  }
  .chip-green {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
  }
  .chip-yellow {
    background: color-mix(in srgb, var(--support-warning, #f1c21b) 20%, transparent);
    color: color-mix(in srgb, var(--support-warning, #c17a00) 100%, transparent);
  }
  .chip-grey {
    background: color-mix(in srgb, var(--text-helper) 15%, transparent);
    color: var(--text-helper);
  }

  /* Detail pane */
  .detail-pane {
    flex: 0 0 44%;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-05);
    overflow-y: auto;
    max-height: 80vh;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .detail-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .detail-title {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .close-btn {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    background: none;
    border: none;
    cursor: pointer;
    padding: var(--spacing-02);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .close-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .detail-list {
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .detail-row {
    display: grid;
    grid-template-columns: 100px 1fr;
    gap: var(--spacing-03);
    align-items: baseline;
  }

  .detail-row-full {
    grid-template-columns: 1fr;
  }

  .detail-list dt {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .detail-list dd {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    word-break: break-all;
  }

  .ua-text {
    font-size: calc(var(--type-code-01-size) * 0.85);
    color: var(--text-secondary);
  }

  .pre-wrap {
    white-space: pre-wrap;
    word-break: break-word;
  }

  /* Stack trace */
  .stack-section {
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .stack-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-03);
  }

  .stack-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .stack-pre {
    font-family: var(--font-mono);
    font-size: calc(var(--type-code-01-size) * 0.9);
    color: var(--text-primary);
    background: var(--layer-02);
    border-radius: var(--radius-md);
    padding: var(--spacing-04);
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-word;
    margin: 0;
  }

  /* Breadcrumbs */
  .breadcrumbs-section {
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-04);
  }

  .section-title {
    font-size: var(--type-heading-compact-01-size, 14px);
    font-weight: var(--type-heading-compact-01-weight, 600);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-03) 0;
  }

  .breadcrumb-list {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .breadcrumb-item {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-02);
    border-radius: var(--radius-md);
  }

  .bc-kind {
    color: var(--text-secondary);
    min-width: 50px;
  }

  .bc-ts {
    color: var(--text-helper);
    font-size: calc(var(--type-code-01-size) * 0.85);
  }

  .bc-detail {
    color: var(--text-primary);
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .bc-msg {
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
  }

  /* Timeline */
  .timeline-section {
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-04);
  }

  .timeline-note {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-03) 0;
    font-style: italic;
  }

  .timeline-list {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .timeline-item {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-02);
    border-radius: var(--radius-md);
  }

  .tl-ts {
    color: var(--text-helper);
    white-space: nowrap;
    font-size: calc(var(--type-code-01-size) * 0.85);
  }

  .tl-source {
    flex-shrink: 0;
  }

  .tl-msg {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
  }

  /* Action sections */
  .action-section {
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .livetail-countdown {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-success);
    font-weight: 600;
    margin: 0;
  }

  /* Error / warning messages */
  .page-error,
  .inline-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-03);
  }

  .inline-warn {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-02);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--border-subtle-01);
  }

  /* Load more */
  .load-more {
    margin-top: var(--spacing-05);
    text-align: center;
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

  .btn-small {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: calc(var(--type-body-compact-01-size) * 0.9);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    white-space: nowrap;
    min-height: calc(var(--touch-min) * 0.8);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-small:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-small:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger {
    padding: var(--spacing-03) var(--spacing-06);
    background: color-mix(in srgb, var(--support-error) 15%, var(--layer-02));
    color: var(--support-error);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--support-error);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-danger:hover:not(:disabled) {
    background: color-mix(in srgb, var(--support-error) 25%, var(--layer-02));
  }
  .btn-danger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
