<script lang="ts">
  /**
   * Compose-window From dropdown (REQ-MAIL-12).
   *
   * Renders a clickable trigger showing the currently selected From
   * identity. Clicking opens a popover-style listbox of all identities
   * sorted by `composeFromSort` (verified-default first, verified next,
   * verifying next, unverified next, external-broken last). Disabled
   * rows are visible-but-not-selectable and render an explanatory
   * tooltip + a state chip; a click on a disabled row is a no-op.
   *
   * The picker is rendered only when there are >= 2 identities; the
   * caller (`ComposeWindow.svelte`) handles the single-identity case
   * with a static text display. This keeps the dropdown DOM out of
   * the common single-identity compose entirely.
   *
   * The component is presentational: it does NOT subscribe to the
   * compose store directly. The parent passes `identity` + an
   * `onSelect` callback so the same component is unit-testable
   * without instantiating the full ComposeStore.
   */
  import type { Identity } from '../mail/types';
  import type { SubmissionSummary } from '../identities/identity-status';
  import { identityStatus } from '../identities/identity-status';
  import { composeFromSort, isFromSelectable } from './from-picker';
  import { t } from '../i18n/i18n.svelte';

  interface Props {
    /** All identities to consider for the picker. */
    identities: readonly Identity[];
    /** Currently selected identity (the From). */
    selected: Identity | null;
    /**
     * Resolve a per-identity submission summary (used for the
     * external-without-submission disabled gate). The caller wires
     * this to `submissionStore.forIdentity(id).data`-shaped data, or
     * returns null when no submission record exists / capability
     * absent.
     */
    submissionResolver: (id: string) => SubmissionSummary | null;
    /**
     * Whether this identity has external submission configured (drives
     * the `[ext]` cosmetic indicator that already existed in compose).
     */
    externalConfigured?: (id: string) => boolean;
    /** Called when the user picks a different (selectable) identity. */
    onSelect: (identity: Identity) => void;
    /** Disabled when compose is in 'sending' status. */
    disabled?: boolean;
  }

  let {
    identities,
    selected,
    submissionResolver,
    externalConfigured,
    onSelect,
    disabled = false,
  }: Props = $props();

  let open = $state(false);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let listEl = $state<HTMLUListElement | null>(null);

  let sorted = $derived(composeFromSort(identities, submissionResolver));

  /** Display string for an identity row. */
  function rowLabel(id: Identity): string {
    return id.name?.trim() ? `${id.name} <${id.email}>` : id.email;
  }

  function chipKey(
    id: Identity,
    sub: SubmissionSummary | null,
  ): 'verifying' | 'unverified' | 'external' | null {
    const status = identityStatus(id);
    if (status === 'verifying') return 'verifying';
    if (status === 'unverified') return 'unverified';
    if (status === 'verified' && !isFromSelectable(id, sub)) return 'external';
    return null;
  }

  function disabledKey(
    id: Identity,
    sub: SubmissionSummary | null,
  ): 'unverified' | 'verifying' | 'external' | null {
    if (isFromSelectable(id, sub)) return null;
    const status = identityStatus(id);
    if (status === 'unverified') return 'unverified';
    if (status === 'verifying') return 'verifying';
    return 'external';
  }

  function toggle(): void {
    if (disabled) return;
    open = !open;
    if (open) {
      // Focus the first selectable row on open.
      queueMicrotask(() => {
        const first = listEl?.querySelector<HTMLButtonElement>(
          'button.row:not(:disabled)',
        );
        first?.focus();
      });
    }
  }

  function close(): void {
    open = false;
    triggerEl?.focus();
  }

  function pick(id: Identity): void {
    const sub = submissionResolver(id.id);
    if (!isFromSelectable(id, sub)) return;
    onSelect(id);
    close();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (!open) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
      return;
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const rows = Array.from(
        listEl?.querySelectorAll<HTMLButtonElement>('button.row') ?? [],
      ).filter((b) => !b.disabled);
      if (rows.length === 0) return;
      const idx = rows.indexOf(document.activeElement as HTMLButtonElement);
      const next =
        e.key === 'ArrowDown'
          ? rows[(idx + 1 + rows.length) % rows.length]
          : rows[(idx - 1 + rows.length) % rows.length];
      next?.focus();
    }
  }

  // Click-outside-to-close. Uses a single document listener while open;
  // detached on close so we don't leak handlers between composes.
  $effect(() => {
    if (!open) return;
    function onDocClick(e: MouseEvent): void {
      const t = e.target as Node | null;
      if (!t) return;
      if (triggerEl?.contains(t)) return;
      if (listEl?.contains(t)) return;
      open = false;
    }
    document.addEventListener('mousedown', onDocClick);
    return () => document.removeEventListener('mousedown', onDocClick);
  });
</script>

<div class="from-picker" onkeydown={onKeyDown} role="presentation">
  <button
    type="button"
    class="trigger"
    bind:this={triggerEl}
    onclick={toggle}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-label={t('compose.from.picker.aria')}
    title={t('compose.from.picker.open')}
    {disabled}
    data-testid="from-picker-trigger"
  >
    <span class="trigger-label">
      {#if selected}
        {rowLabel(selected)}
        {#if externalConfigured?.(selected.id)}
          <span class="ext-indicator" title="Mail sent via external SMTP" aria-label="External SMTP">[ext]</span>
        {/if}
      {:else}
        <span class="muted">{t('compose.from')}</span>
      {/if}
    </span>
    <span class="caret" aria-hidden="true">▾</span>
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
    <ul
      class="list"
      role="listbox"
      bind:this={listEl}
      data-testid="from-picker-list"
    >
      {#each sorted as id (id.id)}
        {@const sub = submissionResolver(id.id)}
        {@const dKey = disabledKey(id, sub)}
        {@const cKey = chipKey(id, sub)}
        {@const isSelected = selected?.id === id.id}
        {@const rowDisabled = dKey !== null}
        <li role="presentation">
          <button
            type="button"
            class="row"
            class:row-selected={isSelected}
            class:row-disabled={rowDisabled}
            onclick={() => pick(id)}
            disabled={rowDisabled}
            role="option"
            aria-selected={isSelected}
            aria-disabled={rowDisabled}
            title={rowDisabled
              ? t(`compose.from.disabled.${dKey}`)
              : ''}
            data-testid="from-picker-row"
            data-identity-id={id.id}
            data-disabled={rowDisabled ? 'true' : 'false'}
          >
            <span class="row-label">{rowLabel(id)}</span>
            <span class="row-trailing">
              {#if cKey === 'verifying'}
                <span class="chip chip-verifying" data-testid="from-chip">
                  {t('compose.from.chip.verifying')}
                </span>
              {:else if cKey === 'unverified'}
                <span class="chip chip-unverified" data-testid="from-chip">
                  {t('compose.from.chip.unverified')}
                </span>
              {:else if cKey === 'external'}
                <span class="chip chip-external" data-testid="from-chip">
                  {t('compose.from.chip.external')}
                </span>
              {:else if externalConfigured?.(id.id)}
                <span class="ext-indicator" title="Mail sent via external SMTP">[ext]</span>
              {/if}
            </span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .from-picker {
    position: relative;
    flex: 1;
    min-width: 0;
  }

  .trigger {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    cursor: pointer;
    max-width: 100%;
    text-align: left;
  }

  .trigger:hover:not(:disabled) {
    background: var(--layer-03);
    border-color: var(--border-subtle-01);
  }

  .trigger:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .trigger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .trigger-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .caret {
    color: var(--text-helper);
    font-size: 10px;
    line-height: 1;
  }

  .muted {
    color: var(--text-helper);
    font-style: italic;
  }

  .ext-indicator {
    display: inline-flex;
    align-items: center;
    padding: 1px var(--spacing-02);
    background: color-mix(in srgb, var(--interactive) 12%, transparent);
    color: var(--interactive);
    border: 1px solid color-mix(in srgb, var(--interactive) 35%, transparent);
    border-radius: var(--radius-sm);
    font-size: 10px;
    font-weight: 600;
    font-family: var(--font-mono);
    letter-spacing: 0.04em;
    line-height: 1;
    vertical-align: middle;
    cursor: default;
  }

  .list {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    min-width: 260px;
    max-height: 320px;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: var(--spacing-02) 0;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
    z-index: 950;
  }

  .row {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    background: transparent;
    border: none;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    text-align: left;
    cursor: pointer;
  }

  .row:hover:not(:disabled) {
    background: var(--layer-03);
  }

  .row:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .row-selected {
    background: color-mix(in srgb, var(--interactive) 8%, transparent);
    font-weight: 600;
  }

  .row-disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .row-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-trailing {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
  }

  .chip {
    display: inline-flex;
    align-items: center;
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    white-space: nowrap;
  }

  .chip-verifying {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: color-mix(in srgb, var(--support-warning) 90%, var(--text-primary));
    border: 1px solid color-mix(in srgb, var(--support-warning) 50%, transparent);
  }

  .chip-unverified {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
    border: 1px solid color-mix(in srgb, var(--support-error) 50%, transparent);
  }

  .chip-external {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: color-mix(in srgb, var(--support-warning) 90%, var(--text-primary));
    border: 1px solid color-mix(in srgb, var(--support-warning) 50%, transparent);
  }
</style>
