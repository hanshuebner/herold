<script lang="ts">
  /**
   * Label create/edit dialog.  Bundles a name field and a color picker with
   * a "Random" button.  Driven by the labelDialog singleton store.
   */
  import { labelDialog } from './label-dialog.svelte';
  import { randomLabelColor } from '../mail/label-color';
  import Button from '@herold/design-system/Button.svelte';
  import { t } from '../i18n/i18n.svelte';

  let ctx = $derived(labelDialog.pending);
  let name = $state('');
  let color = $state('#5d6d7e');
  let nameEl = $state<HTMLInputElement | null>(null);
  // Track the values at dialog open so we can compute dirty state.
  let initialName = $state('');
  let initialColor = $state('#5d6d7e');

  // Reset fields when a new dialog opens.
  $effect(() => {
    if (ctx) {
      const n = ctx.defaultName ?? '';
      const c = ctx.defaultColor ?? randomLabelColor();
      name = n;
      color = c;
      initialName = n;
      initialColor = c;
      requestAnimationFrame(() => {
        nameEl?.focus();
        nameEl?.select();
      });
    }
  });

  /**
   * The CTA is enabled when:
   * - In create mode (no defaultName), OR when the caller opted out of
   *   the dirty check (`requireDirty: false`): the name field is non-empty.
   * - In edit mode (defaultName provided, requireDirty default-true):
   *   the name is non-empty AND at least one of name or color differs
   *   from the saved values (dirty).
   *
   * REQ-MAIL-12c's tagged-address banner passes `requireDirty: false`
   * because the pre-filled suffix-capitalised name is a suggestion the
   * user MAY accept verbatim; the banner-driven flow MUST allow a
   * one-click confirm. Settings rename / edit keep the historical
   * dirty-required semantics.
   */
  let ctaEnabled = $derived(
    ctx?.defaultName !== undefined && ctx?.requireDirty !== false
      ? name.trim().length > 0 &&
          (name.trim() !== initialName || color !== initialColor)
      : name.trim().length > 0,
  );

  // Capture-phase Escape so we close before the global keyboard engine fires.
  $effect(() => {
    if (!ctx) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        labelDialog.decide(null);
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });

  function submit(): void {
    if (!ctx) return;
    const trimmed = name.trim();
    if (!trimmed) return;
    labelDialog.decide({ name: trimmed, color });
  }

  function cancel(): void {
    labelDialog.decide(null);
  }

  function onSubmit(e: Event): void {
    e.preventDefault();
    submit();
  }

  function pickRandom(): void {
    color = randomLabelColor();
  }
</script>

{#if ctx}
  <div class="backdrop" aria-hidden="true" onclick={cancel}></div>
  <div
    class="modal-wrapper"
    role="dialog"
    aria-modal="true"
    aria-labelledby="ld-title"
  >
    <form class="modal" onsubmit={onSubmit}>
      <h2 id="ld-title" class="title">{ctx.title}</h2>

      <label class="field">
        <span class="field-label">{t('label.dialog.nameLabel')}</span>
        <input
          type="text"
          bind:value={name}
          bind:this={nameEl}
          autocomplete="off"
          spellcheck="false"
        />
      </label>

      <div class="color-row">
        <span class="field-label">{t('label.dialog.colorLabel')}</span>
        <div class="color-controls">
          <input
            type="color"
            bind:value={color}
            class="color-input"
            aria-label={t('label.dialog.colorAriaLabel')}
            title={t('label.dialog.colorAriaLabel')}
          />
          <span class="color-swatch" style="background:{color};" aria-hidden="true"></span>
          <Button variant="secondary" compact onclick={pickRandom}>{t('label.dialog.random')}</Button>
        </div>
      </div>

      <div class="actions">
        <Button variant="secondary" onclick={cancel}>
          {ctx.cancelLabel ?? 'Cancel'}
        </Button>
        <Button type="submit" variant="primary" disabled={!ctaEnabled}>
          {ctx.confirmLabel ?? 'OK'}
        </Button>
      </div>
    </form>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 999;
    cursor: default;
  }

  .modal-wrapper {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    pointer-events: none;
    padding: var(--spacing-05);
  }

  .modal {
    pointer-events: auto;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    max-width: 460px;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
  }

  .title {
    margin: 0;
    font-size: var(--type-heading-01-size);
    font-weight: var(--type-heading-01-weight);
    line-height: var(--type-heading-01-line);
    color: var(--text-primary);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .field-label {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  input[type='text'] {
    width: 100%;
    background: var(--layer-01);
    color: var(--text-primary);
    padding: var(--spacing-03) var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    font-size: var(--type-body-01-size);
  }

  input[type='text']:focus {
    outline: 2px solid var(--interactive);
    outline-offset: -1px;
  }

  .color-row {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .color-controls {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
  }

  .color-input {
    width: 40px;
    height: 32px;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    padding: 2px;
    background: var(--layer-01);
    cursor: pointer;
  }

  .color-swatch {
    display: inline-block;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    border: 1px solid rgba(0, 0, 0, 0.2);
    flex-shrink: 0;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }
</style>
