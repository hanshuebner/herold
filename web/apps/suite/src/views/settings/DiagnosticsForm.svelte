<script lang="ts">
  /**
   * DiagnosticsForm — per-user telemetry opt-in checkbox (REQ-CLOG-06) and
   * admin-only diagnostic log copy affordance (issue #83).
   *
   * Telemetry toggle:
   *   Reads the current value from the JMAP session descriptor capability
   *   "urn:netzhansa:params:jmap:clientlog". Persists changes via
   *   PUT /api/v1/me/clientlog/telemetry_enabled with body {enabled: bool|null}.
   *
   * Admin log copy:
   *   When the signed-in user has the 'admin' or 'superadmin' role, shows a
   *   "Load log" button that fetches recent entries from the anonymous
   *   client-log ring buffer (GET /api/v1/admin/clientlog?slice=public&app=suite).
   *   Renders them as plain text in a read-only textarea with a Copy button,
   *   newest last, so the maintainer can paste the service-worker notification
   *   trace directly when debugging issues.
   */

  import { auth } from '../../lib/auth/auth.svelte';
  import { get, put } from '../../lib/api/client';
  import { t } from '../../lib/i18n/i18n.svelte';

  const CAP_CLIENTLOG = 'urn:netzhansa:params:jmap:clientlog';

  interface ClientlogCapability {
    telemetry_enabled?: boolean;
    livetail_until?: string | null;
  }

  interface ClientLogRow {
    id: number;
    slice: string;
    server_ts: string;
    client_ts: string;
    app: string;
    kind: string;
    level: string;
    msg: string;
    user_id?: string;
    session_id?: string;
    page_id: string;
    route?: string;
    build_sha: string;
    ua: string;
    stack?: string;
  }

  interface ListClientLogResponse {
    rows: ClientLogRow[];
    next_cursor: string;
  }

  /** Current server-side value from the JMAP session descriptor. */
  let serverValue = $derived.by<boolean>(() => {
    const cap = auth.session?.capabilities[CAP_CLIENTLOG] as
      | ClientlogCapability
      | undefined;
    return cap?.telemetry_enabled ?? true;
  });

  // Local optimistic state; null means "not yet changed this session".
  let localValue = $state<boolean | null>(null);

  let checked = $derived(localValue !== null ? localValue : serverValue);

  let busy = $state(false);
  let errorMessage = $state<string | null>(null);

  async function handleChange(e: Event): Promise<void> {
    const target = e.currentTarget as HTMLInputElement;
    const next = target.checked;

    // Optimistic update.
    localValue = next;
    errorMessage = null;
    busy = true;
    try {
      await put<void>('/api/v1/me/clientlog/telemetry_enabled', {
        enabled: next,
      });
      // Server accepted; local value now authoritative until next session refresh.
    } catch (err) {
      // Revert optimistic update.
      localValue = null;
      errorMessage =
        err instanceof Error ? err.message : t('settings.diagnostics.saveError');
    } finally {
      busy = false;
    }
  }

  // ── Admin log copy ─────────────────────────────────────────────────────────

  let hasAdminRole = $derived(
    auth.roles.includes('admin') || auth.roles.includes('superadmin'),
  );

  let loadingLog = $state(false);
  let logText = $state<string | null>(null);
  let logError = $state<string | null>(null);
  let copied = $state(false);

  async function loadLog(): Promise<void> {
    loadingLog = true;
    logError = null;
    logText = null;
    try {
      const resp = await get<ListClientLogResponse>(
        '/api/v1/admin/clientlog?slice=public&app=suite&limit=100',
      );
      const rows = resp.rows ?? [];
      if (rows.length === 0) {
        logText = '';
        return;
      }
      // Reverse to show oldest first (newest last).
      const lines = [...rows].reverse().map((row) => {
        const ts = row.server_ts;
        const level = row.level.padEnd(5);
        return `${ts}  ${level}  ${row.msg}`;
      });
      logText = lines.join('\n');
    } catch (err) {
      logError = err instanceof Error ? err.message : t('settings.diagnostics.logCopy.error');
    } finally {
      loadingLog = false;
    }
  }

  async function copyLog(): Promise<void> {
    if (logText === null) return;
    try {
      await navigator.clipboard.writeText(logText);
      copied = true;
      setTimeout(() => { copied = false; }, 2000);
    } catch {
      // Clipboard write failed; the textarea text is still selectable manually.
    }
  }
</script>

<div class="row vertical">
  <label class="switch-row" for="telemetry-enabled">
    <span class="label-text">{t('settings.diagnostics.telemetry.label')}</span>
    <label class="switch" aria-label={t('settings.diagnostics.telemetry.label')}>
      <input
        id="telemetry-enabled"
        type="checkbox"
        checked={checked}
        disabled={busy || auth.status !== 'ready'}
        onchange={handleChange}
      />
      <span class="track" aria-hidden="true"></span>
    </label>
  </label>
  {#if errorMessage}
    <p class="error-text" role="alert">{errorMessage}</p>
  {/if}
</div>

{#if hasAdminRole}
  <div class="log-copy-section">
    <h3 class="section-heading">{t('settings.diagnostics.logCopy.heading')}</h3>
    <p class="hint">{t('settings.diagnostics.logCopy.hint')}</p>
    <div class="log-actions">
      <button
        type="button"
        class="btn-secondary"
        onclick={loadLog}
        disabled={loadingLog}
        aria-busy={loadingLog}
      >
        {loadingLog
          ? t('settings.diagnostics.logCopy.loading')
          : t('settings.diagnostics.logCopy.fetchBtn')}
      </button>
    </div>
    {#if logError}
      <p class="error-text" role="alert">{logError}</p>
    {:else if logText !== null}
      {#if logText === ''}
        <p class="hint">{t('settings.diagnostics.logCopy.empty')}</p>
      {:else}
        <textarea
          readonly
          value={logText}
          rows={12}
          class="log-textarea"
          aria-label={t('settings.diagnostics.logCopy.heading')}
        ></textarea>
        <div class="log-actions">
          <button type="button" class="btn-secondary" onclick={copyLog}>
            {copied
              ? t('settings.diagnostics.logCopy.copiedBtn')
              : t('settings.diagnostics.logCopy.copyBtn')}
          </button>
        </div>
      {/if}
    {/if}
  </div>
{/if}

<style>
  .row.vertical {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .switch-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    cursor: pointer;
  }

  .label-text {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    flex: 1;
  }

  .switch {
    position: relative;
    display: inline-flex;
    width: 44px;
    height: 24px;
    cursor: pointer;
    flex-shrink: 0;
  }

  .switch input {
    position: absolute;
    inset: 0;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }

  .switch input:disabled {
    cursor: not-allowed;
  }

  .switch .track {
    width: 100%;
    height: 100%;
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    position: relative;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .switch .track::before {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 20px;
    height: 20px;
    background: var(--text-on-color);
    border-radius: var(--radius-pill);
    transition: transform var(--duration-fast-02) var(--easing-productive-enter);
  }

  .switch input:checked + .track {
    background: var(--interactive);
  }

  .switch input:checked + .track::before {
    transform: translateX(20px);
  }

  .error-text {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .log-copy-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    margin-top: var(--spacing-05);
    padding-top: var(--spacing-05);
    border-top: 1px solid var(--border-subtle-01);
  }

  .section-heading {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }

  .hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .log-actions {
    display: flex;
    gap: var(--spacing-03);
  }

  .btn-secondary {
    padding: var(--spacing-02) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    background: var(--layer-02);
    border: 1px solid var(--border-strong-01);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }

  .btn-secondary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .log-textarea {
    width: 100%;
    font-family: var(--font-mono, monospace);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03);
    resize: vertical;
    box-sizing: border-box;
  }
</style>
