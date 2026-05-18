<script lang="ts">
  /**
   * Per-identity display-name editor.
   *
   * Lets the user change the `name` field of an Identity (the human-
   * readable part that appears in outbound `From: "Name" <addr>`
   * headers). Calls Identity/set via the mail store's
   * updateIdentityName method, which also mirrors the change into the
   * identities cache so compose / reply flows pick up the new name
   * immediately.
   *
   * Item 7: autosaves on blur — no Save button. Save feedback is the
   * editor page's shared `AutosaveController` indicator.
   */
  import { mail } from '../../lib/mail/store.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';
  import type { AutosaveController } from './autosave.svelte';

  interface Props {
    identity: Identity;
    /** Shared autosave indicator controller owned by the editor page. */
    autosave: AutosaveController;
  }
  let { identity, autosave }: Props = $props();

  let draft = $state('');
  let savedValue = $state('');

  $effect(() => {
    draft = identity.name ?? '';
    savedValue = identity.name ?? '';
  });

  /** Commit on blur when the field changed. */
  async function commit(): Promise<void> {
    if (draft === savedValue) return;
    const next = draft;
    const ok = await autosave.run(() => mail.updateIdentityName(identity.id, next));
    if (ok) savedValue = next;
  }
</script>

<div class="form">
  <label class="field-label">
    <span class="label-text">{t('settings.displayName.label')}</span>
    <input
      type="text"
      bind:value={draft}
      placeholder={identity.email}
      autocomplete="off"
      onblur={() => void commit()}
      data-testid="identity-display-name-input"
    />
  </label>

  <p class="helper">{t('settings.displayName.helper')}</p>
</div>

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

  .helper {
    margin: 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
</style>
