<script lang="ts">
  /**
   * Add / edit dialog for one TaggedAddressFilter
   * (REQ-SET-TAG-03, part of REQ-SET-TAG-01..21).
   *
   * Modes:
   *   - create: identity dropdown + suffix + action + label-name; on
   *     save calls `taggedAddressStore.createFilter`.
   *   - edit:   action + label-name only (the suffix / base identity
   *     are immutable per REQ-SET-TAG-03; to change those, the user
   *     must delete and recreate). On save calls
   *     `taggedAddressStore.updateFilter`.
   *
   * Validation is local-first via `validateSuffix` / `validateLabelName`
   * in `lib/settings/tagged-addresses.ts`; the server's identical gate
   * is the canonical authority but the local pass keeps the round-trip
   * cheap and the error messages localised.
   */

  import { onMount, untrack } from 'svelte';
  import { mail } from '../../lib/mail/store.svelte';
  import { taggedAddressStore } from '../../lib/mail/tagged-address-store.svelte';
  import type { TaggedAddressFilter } from '../../lib/mail/tagged-address-store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import { identityStatus } from '../../lib/identities/identity-status';
  import {
    FILTER_ACTIONS,
    normaliseSuffix,
    validateLabelName,
    validateSuffix,
    type FilterAction,
    type LabelError,
    type SuffixError,
  } from '../../lib/settings/tagged-addresses';
  import type { Identity } from '../../lib/mail/types';
  import Button from '@herold/design-system/Button.svelte';

  interface Props {
    /** Null = create mode; a filter row = edit mode (REQ-SET-TAG-03). */
    filter: TaggedAddressFilter | null;
    /** Called when the dialog should close (cancel or after save). */
    onclose: () => void;
    /** Called after a successful save / create with the resulting row. */
    onsaved?: (row: TaggedAddressFilter) => void;
  }

  let { filter, onclose, onsaved }: Props = $props();

  const isEdit = $derived(filter !== null);

  /**
   * Verified identities the user can use as the base for a filter
   * (REQ-SET-TAG-03 + REQ-TAG-13: only verified identities may host
   * filters server-side). The synthesised default identity (no
   * `mayDelete`) counts as verified-by-construction.
   */
  const verifiedIdentities = $derived<Identity[]>(
    Array.from(mail.identities.values()).filter(
      (id) => identityStatus(id) === 'verified',
    ),
  );

  // ── Working copy ──────────────────────────────────────────────────────
  let baseIdentityId = $state<string>('');
  let suffix = $state<string>('');
  let action = $state<FilterAction>('label');
  let labelName = $state<string>('');

  let suffixError = $state<SuffixError | null>(null);
  let labelError = $state<LabelError | null>(null);
  let saveError = $state<string | null>(null);
  let saving = $state(false);

  // Initialise from the prop on mount. Subsequent prop changes are
  // intentionally not tracked: in this app the dialog is recreated
  // on each open via the parent's {#if filter} block, so reseeding
  // mid-life is unnecessary and would clobber unsaved edits.
  onMount(() => {
    untrack(() => {
      if (filter) {
        baseIdentityId = filter.baseIdentityId;
        suffix = filter.suffix;
        action = filter.action;
        labelName = filter.labelName;
      } else {
        baseIdentityId = verifiedIdentities[0]?.id ?? '';
        suffix = '';
        action = 'label';
        labelName = '';
      }
    });
  });

  // Live validation. The suffix gate normalises before validating so
  // the user sees the "lower-case only" hint the moment they type a
  // capital, without the suffix turning lower-case in the input.
  $effect(() => {
    if (suffix.length === 0) {
      suffixError = null;
      return;
    }
    suffixError = validateSuffix(suffix);
  });
  $effect(() => {
    if (labelName.length === 0) {
      labelError = null;
      return;
    }
    labelError = validateLabelName(labelName);
  });

  const canSave = $derived(
    !saving &&
      (isEdit || baseIdentityId !== '') &&
      suffix.trim().length > 0 &&
      labelName.trim().length > 0 &&
      suffixError === null &&
      labelError === null,
  );

  const baseIdentity = $derived<Identity | null>(
    mail.identities.get(baseIdentityId) ?? null,
  );

  function close(): void {
    if (saving) return;
    onclose();
  }

  function onBackdropClick(e: MouseEvent): void {
    if (e.target === e.currentTarget) close();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  }

  async function save(): Promise<void> {
    if (!canSave) return;
    saveError = null;

    // Re-validate so a paste-then-submit path can't slip a bad suffix
    // through.
    const normalised = normaliseSuffix(suffix);
    const sErr = validateSuffix(normalised);
    const lErr = validateLabelName(labelName);
    if (sErr || lErr) {
      suffixError = sErr;
      labelError = lErr;
      return;
    }

    saving = true;
    try {
      let row: TaggedAddressFilter;
      if (filter) {
        row = await taggedAddressStore.updateFilter(filter.id, {
          action,
          labelName: labelName.trim(),
        });
        toast.show({ message: t('settings.taggedAddresses.toast.updated') });
      } else {
        row = await taggedAddressStore.createFilter({
          baseIdentityId,
          suffix: normalised,
          action,
          labelName: labelName.trim(),
        });
        toast.show({ message: t('settings.taggedAddresses.toast.created') });
      }
      onsaved?.(row);
      onclose();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      saveError = msg;
    } finally {
      saving = false;
    }
  }

  function suffixOnInput(e: Event): void {
    suffix = (e.currentTarget as HTMLInputElement).value;
  }

  function suffixOnBlur(): void {
    // Mirror the server's normalisation on blur so the persisted form
    // matches what the user sees before submit.
    suffix = normaliseSuffix(suffix);
  }
</script>

<svelte:window onkeydown={onKeyDown} />

<div
  class="backdrop"
  role="presentation"
  onclick={onBackdropClick}
  aria-hidden="true"
></div>

<div
  class="dialog"
  role="dialog"
  aria-modal="true"
  aria-labelledby="ta-dialog-title"
  tabindex="-1"
  data-testid="tagged-address-filter-dialog"
>
  <header class="dialog-header">
    <h3 id="ta-dialog-title" class="dialog-title">
      {isEdit
        ? t('settings.taggedAddresses.dialog.titleEdit')
        : t('settings.taggedAddresses.dialog.titleAdd')}
    </h3>
    <button
      type="button"
      class="close"
      onclick={close}
      aria-label={t('common.close')}
      data-testid="tagged-address-filter-dialog-close"
    >
      ×
    </button>
  </header>

  <form
    class="dialog-body"
    onsubmit={(e) => {
      e.preventDefault();
      void save();
    }}
  >
    {#if !isEdit && verifiedIdentities.length === 0}
      <p class="empty" role="alert">
        {t('settings.taggedAddresses.dialog.noVerifiedIdentities')}
      </p>
    {/if}

    <!-- Base identity (create-mode only; REQ-SET-TAG-03 keeps it
         immutable on edit). -->
    {#if !isEdit}
      <label class="field-label">
        <span class="label-text">{t('settings.taggedAddresses.dialog.identity')}</span>
        <select
          bind:value={baseIdentityId}
          disabled={saving || verifiedIdentities.length === 0}
          data-testid="tagged-address-filter-dialog-identity"
        >
          {#each verifiedIdentities as id (id.id)}
            <option value={id.id}>
              {id.name ? `${id.name} <${id.email}>` : id.email}
            </option>
          {/each}
        </select>
        <span class="helper">
          {t('settings.taggedAddresses.dialog.identityHint', {
            address: baseIdentity?.email ?? '—',
          })}
        </span>
      </label>
    {:else if baseIdentity}
      <div class="static-row">
        <span class="static-label">{t('settings.taggedAddresses.dialog.identity')}</span>
        <span class="static-value">
          {baseIdentity.name
            ? `${baseIdentity.name} <${baseIdentity.email}>`
            : baseIdentity.email}
        </span>
      </div>
    {/if}

    <!-- Suffix (create-mode only). -->
    {#if !isEdit}
      <label class="field-label">
        <span class="label-text">{t('settings.taggedAddresses.dialog.suffix')}</span>
        <input
          type="text"
          value={suffix}
          oninput={suffixOnInput}
          onblur={suffixOnBlur}
          placeholder={t('settings.taggedAddresses.dialog.suffixPlaceholder')}
          disabled={saving}
          autocomplete="off"
          aria-invalid={suffixError !== null}
          data-testid="tagged-address-filter-dialog-suffix"
          maxlength="64"
        />
        <span class="helper">
          {t('settings.taggedAddresses.dialog.suffixHint')}
        </span>
        {#if suffixError}
          <span class="error" role="alert" data-testid="tagged-address-filter-dialog-suffix-error">
            {t(suffixError)}
          </span>
        {/if}
      </label>
    {:else}
      <div class="static-row">
        <span class="static-label">{t('settings.taggedAddresses.dialog.suffix')}</span>
        <span class="static-value mono">+{suffix}</span>
      </div>
    {/if}

    <!-- Action radio group. -->
    <fieldset class="field-label">
      <legend class="label-text">{t('settings.taggedAddresses.dialog.action')}</legend>
      <div class="radio-group" role="radiogroup">
        {#each FILTER_ACTIONS as choice (choice)}
          <label class="radio">
            <input
              type="radio"
              name="ta-action"
              value={choice}
              checked={action === choice}
              onchange={() => (action = choice)}
              disabled={saving}
              data-testid={`tagged-address-filter-dialog-action-${choice}`}
            />
            <span>
              {choice === 'label'
                ? t('settings.taggedAddresses.dialog.actionLabel')
                : choice === 'label_archive'
                  ? t('settings.taggedAddresses.dialog.actionLabelArchive')
                  : t('settings.taggedAddresses.dialog.actionLabelArchiveRead')}
            </span>
          </label>
        {/each}
      </div>
    </fieldset>

    <!-- Label name. -->
    <label class="field-label">
      <span class="label-text">{t('settings.taggedAddresses.dialog.labelName')}</span>
      <input
        type="text"
        bind:value={labelName}
        placeholder={t('settings.taggedAddresses.dialog.labelPlaceholder')}
        disabled={saving}
        autocomplete="off"
        aria-invalid={labelError !== null}
        data-testid="tagged-address-filter-dialog-label"
        maxlength="255"
      />
      <span class="helper">{t('settings.taggedAddresses.dialog.labelNameHint')}</span>
      {#if labelError}
        <span class="error" role="alert" data-testid="tagged-address-filter-dialog-label-error">
          {t(labelError)}
        </span>
      {/if}
    </label>

    {#if saveError}
      <p class="error" role="alert" data-testid="tagged-address-filter-dialog-save-error">
        {saveError}
      </p>
    {/if}

    <div class="actions">
      <Button
        variant="secondary"
        onclick={close}
        disabled={saving}
        testid="tagged-address-filter-dialog-cancel"
      >
        {t('settings.taggedAddresses.dialog.cancel')}
      </Button>
      <Button
        type="submit"
        variant="primary"
        disabled={!canSave}
        testid="tagged-address-filter-dialog-save"
      >
        {saving
          ? t('settings.taggedAddresses.dialog.saving')
          : isEdit
            ? t('settings.taggedAddresses.dialog.save')
            : t('settings.taggedAddresses.dialog.create')}
      </Button>
    </div>
  </form>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 800;
  }

  .dialog {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(560px, calc(100vw - 2 * var(--spacing-05)));
    max-height: calc(100vh - 2 * var(--spacing-07));
    display: flex;
    flex-direction: column;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 801;
    overflow: hidden;
  }

  .dialog-header {
    display: flex;
    align-items: center;
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    gap: var(--spacing-04);
  }

  .dialog-title {
    margin: 0;
    flex: 1;
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
  }

  .close {
    color: var(--text-helper);
    font-size: 20px;
    line-height: 1;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
    flex-shrink: 0;
  }
  .close:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .dialog-body {
    padding: var(--spacing-05);
    overflow-y: auto;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .field-label {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    margin: 0;
    border: 0;
    padding: 0;
  }
  .label-text {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-weight: 600;
  }
  .helper {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  input[type='text'],
  select {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    width: 100%;
  }
  input[type='text']:focus,
  select:focus {
    outline: 2px solid var(--interactive);
    outline-offset: -1px;
  }
  input[aria-invalid='true'] {
    border-color: var(--support-error);
  }

  .radio-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .radio {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    cursor: pointer;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
  }
  .radio input[type='radio'] {
    accent-color: var(--interactive);
    width: 18px;
    height: 18px;
  }

  .static-row {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .static-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-weight: 600;
  }
  .static-value {
    color: var(--text-primary);
    word-break: break-all;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .empty {
    color: var(--support-warning);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    padding-top: var(--spacing-03);
  }
</style>
