<script lang="ts">
  /**
   * Per-identity display-name editor.
   *
   * Lets the user change the `name` field of an Identity (the human-readable
   * part that appears in outbound `From: "Name" <addr>` headers).
   * Calls Identity/set via the mail store's updateIdentityName method, which
   * also mirrors the change into the identities cache so compose / reply
   * flows pick up the new name immediately.
   */
  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';

  interface Props {
    identity: Identity;
  }
  let { identity }: Props = $props();

  let draft = $state('');
  let savedValue = $state('');
  let saving = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    draft = identity.name ?? '';
    savedValue = identity.name ?? '';
  });

  let dirty = $derived(draft !== savedValue);

  async function save(): Promise<void> {
    if (!dirty || saving) return;
    saving = true;
    error = null;
    try {
      await mail.updateIdentityName(identity.id, draft);
      savedValue = draft;
      toast.show({ message: t('settings.saved'), timeoutMs: 3000 });
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      toast.show({ message: t('settings.saveFailed'), kind: 'error', timeoutMs: 5000 });
    } finally {
      saving = false;
    }
  }

  function revert(): void {
    draft = savedValue;
    error = null;
  }
</script>

<form
  class="form"
  onsubmit={(e) => {
    e.preventDefault();
    void save();
  }}
>
  <label class="field-label">
    <span class="label-text">{t('settings.displayName.label')}</span>
    <input
      type="text"
      bind:value={draft}
      placeholder={identity.email}
      disabled={saving}
      autocomplete="off"
    />
  </label>

  <p class="helper">{t('settings.displayName.helper')}</p>

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  <div class="actions">
    <button type="button" class="ghost" onclick={revert} disabled={!dirty || saving}>
      Revert
    </button>
    <button type="submit" class="primary" disabled={!dirty || saving}>
      {saving ? 'Saving...' : t('settings.save')}
    </button>
  </div>
</form>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .field-label {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label-text {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  input[type='text'] {
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: var(--spacing-03);
  }

  input[type='text']:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  input[type='text']:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .helper {
    margin: 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
  }

  .primary,
  .ghost {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }

  .primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .primary:disabled,
  .ghost:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .ghost {
    color: var(--text-secondary);
  }

  .ghost:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .error {
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }
</style>
