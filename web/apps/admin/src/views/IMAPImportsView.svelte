<script lang="ts">
  /**
   * IMAP-Import-Diagnose-Ansicht (REQ-ADM-305, re #138).
   *
   * Zeigt den Live-Worker-Status aller IMAP-Import-Konten aus
   * GET /api/v1/imap-imports/status. Bietet einen Debug-Logging-Schalter
   * pro Konto (PATCH debug_log). Debug-Ausgabe erscheint im Ereignisse-View
   * gefiltert nach der Konto-ID.
   */

  import { imapImports } from '../lib/imap-imports/imap-imports.svelte';
  import { events } from '../lib/events/events.svelte';
  import { router } from '../lib/router/router.svelte';
  import { formatAbsolute, formatRelative, DATE_TIME_SHORT } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  $effect(() => {
    if (imapImports.status === 'idle') {
      void imapImports.load();
    }
  });

  // Per-account debug-toggle in-flight and error state
  let toggleInFlight = $state<Record<string, boolean>>({});
  let toggleError = $state<Record<string, string | null>>({});

  async function toggleDebugLog(
    principalId: string,
    accountId: string,
    currentValue: boolean,
  ): Promise<void> {
    if (toggleInFlight[accountId]) return;
    toggleInFlight = { ...toggleInFlight, [accountId]: true };
    toggleError = { ...toggleError, [accountId]: null };
    const result = await imapImports.setDebugLog(principalId, accountId, !currentValue);
    toggleInFlight = { ...toggleInFlight, [accountId]: false };
    if (!result.ok) {
      toggleError = { ...toggleError, [accountId]: result.errorMessage };
    }
  }

  function phaseClass(phase: string): string {
    switch (phase) {
      case 'idle':
      case 'polling':
        return 'badge-neutral';
      case 'syncing':
      case 'writeback':
        return 'badge-info';
      case 'connecting':
      case 'starting':
        return 'badge-warning';
      case 'backoff':
      case 'errored':
        return 'badge-error';
      case 'migrating':
        return 'badge-info';
      case 'migrated':
      case 'stopped':
        return 'badge-neutral';
      default:
        return 'badge-neutral';
    }
  }

  function formatDate(iso: string): string {
    return formatAbsolute(iso, DATE_TIME_SHORT);
  }

  function formatRelativeDate(iso: string): string {
    return formatRelative(iso);
  }

  function goToEventsForAccount(accountId: string): void {
    // Pre-fill the actor_id filter so the Events view opens ready to filter.
    events.actorIdFilter = accountId;
    router.navigate('/events');
  }
</script>

<div class="imap-imports-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('imapImports.title')}</h1>
      {#if imapImports.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
    <button
      type="button"
      class="btn-secondary"
      onclick={() => void imapImports.refresh()}
      disabled={imapImports.status === 'loading'}
      data-testid="imap-imports-refresh"
    >
      {t('imapImports.refresh')}
    </button>
  </div>

  <p class="page-hint">
    {t('imapImports.hint.prefix')}
    <button type="button" class="link-btn" onclick={() => router.navigate('/events')}>
      {t('imapImports.hint.eventsLink')}
    </button>{t('imapImports.hint.suffix')}
  </p>

  {#if imapImports.status === 'error'}
    <div class="page-error" role="alert">{imapImports.errorMessage}</div>
  {:else if imapImports.status === 'ready' && imapImports.workers.length === 0}
    <p class="empty-state" data-testid="imap-imports-empty">
      {t('imapImports.empty')}
    </p>
  {:else if imapImports.workers.length > 0}
    <div class="workers-list" data-testid="imap-imports-list">
      {#each imapImports.workers as worker (worker.account_id)}
        <div class="worker-card" data-testid="worker-card-{worker.account_id}">
          <!-- Header row -->
          <div class="worker-header">
            <div class="worker-identity">
              <span class="worker-name" data-testid="worker-account-name">{worker.account_name}</span>
              <span class="worker-host">{worker.host}</span>
            </div>
            <div class="worker-badges">
              <span
                class="badge {worker.connected ? 'badge-success' : 'badge-neutral'}"
                data-testid="worker-connected"
              >
                {worker.connected ? t('imapImports.connected') : t('imapImports.disconnected')}
              </span>
              <span
                class="badge {phaseClass(worker.phase)}"
                data-testid="worker-phase"
              >
                {worker.phase}
              </span>
            </div>
          </div>

          <!-- Detail grid -->
          <dl class="worker-detail">
            {#if worker.watch_mode}
              <div class="detail-row">
                <dt>{t('imapImports.field.watchMode')}</dt>
                <dd data-testid="worker-watch-mode">{worker.watch_mode}</dd>
              </div>
            {/if}
            {#if worker.conn_mode}
              <div class="detail-row">
                <dt>{t('imapImports.field.connMode')}</dt>
                <dd>{worker.conn_mode}</dd>
              </div>
            {/if}
            <div class="detail-row">
              <dt>{t('imapImports.field.phaseSince')}</dt>
              <dd title={formatDate(worker.phase_since)}>
                {formatRelativeDate(worker.phase_since)}
              </dd>
            </div>
            {#if worker.last_sync_at}
              <div class="detail-row">
                <dt>{t('imapImports.field.lastSync')}</dt>
                <dd data-testid="worker-last-sync">{formatDate(worker.last_sync_at)}</dd>
              </div>
            {/if}
            <div class="detail-row">
              <dt>{t('imapImports.field.messagesFetched')}</dt>
              <dd data-testid="worker-messages-fetched">{worker.messages_fetched}</dd>
            </div>
            <div class="detail-row">
              <dt>{t('imapImports.field.flagsPropagated')}</dt>
              <dd>{worker.flags_propagated}</dd>
            </div>
            {#if worker.consecutive_failures > 0}
              <div class="detail-row">
                <dt>{t('imapImports.field.consecutiveFailures')}</dt>
                <dd class="text-error">{worker.consecutive_failures}</dd>
              </div>
            {/if}
            {#if worker.current_folder}
              <div class="detail-row">
                <dt>{t('imapImports.field.currentFolder')}</dt>
                <dd>{worker.current_folder}</dd>
              </div>
            {/if}
            {#if worker.next_poll_at}
              <div class="detail-row">
                <dt>{t('imapImports.field.nextPoll')}</dt>
                <dd title={formatDate(worker.next_poll_at)}>
                  {formatRelativeDate(worker.next_poll_at)}
                </dd>
              </div>
            {/if}
            <div class="detail-row">
              <dt>{t('imapImports.field.accountId')}</dt>
              <dd class="mono">{worker.account_id}</dd>
            </div>
            <div class="detail-row">
              <dt>{t('imapImports.field.principalId')}</dt>
              <dd class="mono">{worker.principal_id}</dd>
            </div>
          </dl>

          {#if worker.last_error}
            <div class="worker-error" data-testid="worker-last-error">
              <span class="error-label">{t('imapImports.lastError')}</span>
              <span class="error-text">{worker.last_error}</span>
            </div>
          {/if}

          <!-- Debug logging toggle + events link -->
          <div class="worker-actions">
            <label class="debug-toggle" data-testid="worker-debug-toggle-label-{worker.account_id}">
              <input
                type="checkbox"
                checked={worker.debug_log}
                disabled={toggleInFlight[worker.account_id]}
                onchange={() =>
                  void toggleDebugLog(worker.principal_id, worker.account_id, worker.debug_log)}
                data-testid="worker-debug-toggle-{worker.account_id}"
                aria-label={t('imapImports.debugLoggingFor', { name: worker.account_name })}
              />
              {t('imapImports.debugLogging')}
              {#if toggleInFlight[worker.account_id]}
                <span class="saving-hint">…</span>
              {/if}
            </label>

            <button
              type="button"
              class="btn-secondary btn-sm"
              onclick={() => goToEventsForAccount(worker.account_id)}
              data-testid="worker-events-btn-{worker.account_id}"
            >
              {t('imapImports.filterEvents')}
            </button>
          </div>

          {#if toggleError[worker.account_id]}
            <p class="toggle-error" role="alert">{toggleError[worker.account_id]}</p>
          {/if}

          <p class="debug-hint">
            {t('imapImports.debugHint')}
          </p>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .imap-imports-page {
    max-width: 860px;
  }

  .page-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-05);
    margin-bottom: var(--spacing-04);
    flex-wrap: wrap;
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

  .page-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-06);
    line-height: var(--type-body-compact-01-line);
  }

  .link-btn {
    background: none;
    border: none;
    color: var(--interactive);
    font-size: inherit;
    font-family: inherit;
    cursor: pointer;
    padding: 0;
    text-decoration: underline;
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

  .empty-state {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
  }

  .workers-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }

  .worker-card {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    padding: var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
  }

  .worker-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-04);
    flex-wrap: wrap;
  }

  .worker-identity {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .worker-name {
    font-size: var(--type-body-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .worker-host {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-family: var(--font-mono);
  }

  .worker-badges {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    flex-wrap: wrap;
    flex-shrink: 0;
  }

  .badge {
    display: inline-flex;
    align-items: center;
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
  }

  .badge-success {
    background: color-mix(in srgb, var(--support-success) 16%, transparent);
    color: color-mix(in srgb, var(--support-success) 80%, var(--text-primary));
  }

  .badge-neutral {
    background: color-mix(in srgb, var(--text-helper) 16%, transparent);
    color: var(--text-helper);
  }

  .badge-info {
    background: color-mix(in srgb, var(--interactive) 16%, transparent);
    color: var(--interactive);
  }

  .badge-warning {
    background: color-mix(in srgb, var(--support-warning) 16%, transparent);
    color: color-mix(in srgb, var(--support-warning) 70%, var(--text-primary));
  }

  .badge-error {
    background: color-mix(in srgb, var(--support-error) 16%, transparent);
    color: var(--support-error);
  }

  .worker-detail {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--spacing-01) var(--spacing-05);
    margin: 0;
    padding: 0;
  }

  .detail-row {
    display: contents;
  }

  .detail-row dt {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .detail-row dd {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    margin: 0;
    word-break: break-all;
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .text-error {
    color: var(--support-error);
  }

  .worker-error {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .error-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--support-error);
  }

  .error-text {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    word-break: break-all;
  }

  .worker-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    padding-top: var(--spacing-03);
    border-top: 1px solid var(--border-subtle-01);
  }

  .debug-toggle {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
    user-select: none;
  }

  .debug-toggle input[type='checkbox'] {
    accent-color: var(--interactive);
    width: 16px;
    height: 16px;
    cursor: pointer;
  }

  .debug-toggle input[type='checkbox']:disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }

  .saving-hint {
    color: var(--text-helper);
  }

  .toggle-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
  }

  .debug-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
    line-height: var(--type-body-compact-01-line);
    font-style: italic;
  }

  /* Shared buttons */
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

  .btn-sm {
    padding: var(--spacing-02) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    min-height: unset;
  }
</style>
