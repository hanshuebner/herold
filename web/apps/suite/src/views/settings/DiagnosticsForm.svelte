<script lang="ts">
  /**
   * DiagnosticsForm — per-user telemetry opt-in checkbox (REQ-CLOG-06).
   *
   * Reads the current value from the JMAP session descriptor capability
   * "urn:netzhansa:params:jmap:clientlog". Persists changes via
   * PUT /api/v1/me/clientlog/telemetry_enabled with body {enabled: bool|null}.
   *
   * null clears the per-user override and falls back to the server default.
   * Here we only expose true/false; null is never sent from this UI.
   */

  import { auth } from '../../lib/auth/auth.svelte';
  import { put } from '../../lib/api/client';
  import { t } from '../../lib/i18n/i18n.svelte';

  const CAP_CLIENTLOG = 'urn:netzhansa:params:jmap:clientlog';

  interface ClientlogCapability {
    telemetry_enabled?: boolean;
    livetail_until?: string | null;
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
        err instanceof Error ? err.message : 'Could not save setting.';
    } finally {
      busy = false;
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
</style>
