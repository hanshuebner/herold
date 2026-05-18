<script lang="ts">
  /**
   * Subtle shared autosave indicator for the identity editor (Item 7).
   *
   * The editor autosaves on blur; this component is the only feedback
   * surface. It is deliberately quiet: a small inline status line that
   * shows "Saving…" while a write is in flight and "Saved" briefly after
   * it lands, then fades back to nothing. Errors stay visible (they are
   * actionable) until the next successful save.
   */
  import { t } from '../../lib/i18n/i18n.svelte';

  export type SaveState = 'idle' | 'saving' | 'saved' | 'error';

  interface Props {
    state: SaveState;
    /** Error text shown when `state === 'error'`. */
    errorMessage?: string | null;
  }
  let { state, errorMessage = null }: Props = $props();
</script>

<span
  class="save-status"
  class:visible={state !== 'idle'}
  class:is-error={state === 'error'}
  role="status"
  aria-live="polite"
  data-testid="identity-save-status"
  data-save-state={state}
>
  {#if state === 'saving'}
    <span class="dot dot-saving" aria-hidden="true"></span>
    {t('settings.identityEdit.saving')}
  {:else if state === 'saved'}
    <span class="dot dot-saved" aria-hidden="true"></span>
    {t('settings.identityEdit.saved')}
  {:else if state === 'error'}
    <span class="dot dot-error" aria-hidden="true"></span>
    {errorMessage || t('settings.identityEdit.saveFailed')}
  {/if}
</span>

<style>
  .save-status {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    opacity: 0;
    transition: opacity var(--duration-moderate-01) var(--easing-productive-enter);
    min-height: 18px;
  }

  .save-status.visible {
    opacity: 1;
  }

  .save-status.is-error {
    color: var(--support-error);
  }

  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .dot-saving {
    background: var(--text-helper);
  }

  .dot-saved {
    background: var(--support-success);
  }

  .dot-error {
    background: var(--support-error);
  }
</style>
