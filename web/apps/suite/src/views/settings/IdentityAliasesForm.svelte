<script lang="ts">
  /**
   * Per-identity alias-address editor (issue #387).
   *
   * An identity's aliases are additional addr-spec strings that select it
   * as the reply sender when a message was addressed or delivered to
   * them (`selectReplyIdentity`'s alias-matching step in
   * `lib/compose/reply-identity.ts`). The primary `email` remains the
   * only address that ever appears in an outbound From header; aliases
   * are match-only.
   *
   * Item 7 (see IdentityEditPage.svelte header comment): this section
   * autosaves — there is no per-field Save button. Adding or removing an
   * alias immediately issues an `Identity/set update { aliases }` via the
   * page's shared `AutosaveController`; a rejected write (duplicate
   * alias, an address already claimed as another identity's primary,
   * or a malformed address) reverts the optimistic list edit and
   * surfaces the server's `invalidProperties` description through the
   * shared save-status indicator, exactly like the Reply-To / Bcc
   * fields above it.
   *
   * Never rendered for the synthesised default identity (`mayDelete:
   * false`) — the server refuses an `aliases` update on that row
   * outright (it has no backing identity row to hold them), so the
   * caller (IdentityEditPage) gates this component on `identity.mayDelete`.
   */
  import { mail } from '../../lib/mail/store.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import { isValidEmail } from '../../lib/identities/wizard-validators';
  import type { Identity } from '../../lib/mail/types';
  import type { AutosaveController } from './autosave.svelte';

  interface Props {
    identity: Identity;
    /** Shared autosave indicator controller owned by the editor page. */
    autosave: AutosaveController;
  }
  let { identity, autosave }: Props = $props();

  /** Working list, mirrored from the identity prop. */
  let addresses = $state<string[]>([]);
  /** Last list the server confirmed — the revert target on a failed save. */
  let savedAddresses = $state<string[]>([]);
  let draft = $state('');
  let draftError = $state<string | null>(null);

  // Mirror the prop into the working copy. Self-referential reads are
  // avoided so a keystroke does not re-run this effect (Svelte 5 footgun,
  // see web/CLAUDE.md "Patterns to avoid").
  $effect(() => {
    const list = identity.aliases ?? [];
    addresses = [...list];
    savedAddresses = [...list];
  });

  /** Persist `next` as the full alias list; revert on failure. */
  async function commit(next: string[]): Promise<void> {
    addresses = next; // optimistic
    const ok = await autosave.run(() => mail.updateIdentityAliases(identity.id, next));
    if (ok) {
      savedAddresses = [...next];
    } else {
      addresses = [...savedAddresses];
    }
  }

  function addAddress(): void {
    const value = draft.trim();
    if (value === '') return;
    if (!isValidEmail(value)) {
      draftError = t('settings.identityEdit.invalidEmail');
      return;
    }
    const lower = value.toLowerCase();
    if (lower === identity.email.trim().toLowerCase()) {
      draftError = t('settings.identityEdit.aliasIsPrimary');
      return;
    }
    if (addresses.some((a) => a.toLowerCase() === lower)) {
      draftError = t('settings.identityEdit.aliasDuplicate');
      return;
    }
    draftError = null;
    draft = '';
    void commit([...addresses, value]);
  }

  function removeAddress(address: string): void {
    void commit(addresses.filter((a) => a !== address));
  }

  function onDraftKeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter') {
      event.preventDefault();
      addAddress();
    }
  }
</script>

<div class="form" data-testid="identity-aliases-form">
  <h4 class="group-title">{t('settings.identityEdit.aliasesHeading')}</h4>
  <p class="hint">{t('settings.identityEdit.aliasesHelper')}</p>

  {#if addresses.length > 0}
    <ul class="chip-list" aria-label={t('settings.identityEdit.aliasesHeading')}>
      {#each addresses as address (address)}
        <li class="alias-chip" data-testid="identity-alias-chip">
          <span class="chip-text">{address}</span>
          <button
            type="button"
            class="chip-remove"
            aria-label={t('settings.identityEdit.aliasRemoveAria', { email: address })}
            data-testid="identity-alias-remove"
            onclick={() => removeAddress(address)}
          >
            &times;
          </button>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="hint empty-state">{t('settings.identityEdit.aliasesEmpty')}</p>
  {/if}

  <div class="add-row">
    <label class="field-label add-input-label">
      <span class="sr-only">{t('settings.identityEdit.aliasAddLabel')}</span>
      <input
        type="email"
        bind:value={draft}
        placeholder={t('settings.identityEdit.aliasPlaceholder')}
        autocomplete="off"
        aria-invalid={draftError !== null}
        onkeydown={onDraftKeydown}
        oninput={() => (draftError = null)}
        data-testid="identity-alias-input"
      />
    </label>
    <button
      type="button"
      class="add-btn"
      data-testid="identity-alias-add"
      onclick={addAddress}
    >
      {t('settings.identityEdit.aliasAdd')}
    </button>
  </div>

  {#if draftError}
    <p class="error" role="alert" data-testid="identity-alias-error">
      {draftError}
    </p>
  {/if}
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

  .group-title {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .hint {
    margin: 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .empty-state {
    font-style: italic;
  }

  .chip-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .alias-chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-02) var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    font-family: var(--font-mono);
  }

  .chip-remove {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--text-helper);
    font-size: 14px;
    line-height: 1;
    cursor: pointer;
  }

  .chip-remove:hover {
    background: var(--layer-03);
    color: var(--support-error);
  }

  .add-row {
    display: flex;
    gap: var(--spacing-02);
    align-items: stretch;
  }

  .add-input-label {
    flex: 1;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  input[type='email'] {
    width: 100%;
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: var(--spacing-03);
  }

  input[type='email']:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  input[type='email'][aria-invalid='true'] {
    border-color: var(--support-error);
  }

  .add-btn {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-lg);
    border: 1px solid var(--border-subtle-01);
    background: var(--layer-02);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    cursor: pointer;
    white-space: nowrap;
  }

  .add-btn:hover {
    background: var(--layer-03);
  }

  .error {
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: color-mix(in srgb, var(--support-error) 12%, transparent);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }
</style>
