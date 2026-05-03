<script lang="ts">
  /**
   * Label create/edit dialog.  Bundles a name field and a color picker with
   * a "Random" button.  Driven by the labelDialog singleton store.
   */
  import { labelDialog } from './label-dialog.svelte';
  import { randomLabelColor } from '../mail/label-color';

  let ctx = $derived(labelDialog.pending);
  let name = $state('');
  let color = $state('#5d6d7e');
  let nameEl = $state<HTMLInputElement | null>(null);

  // Reset fields when a new dialog opens.
  $effect(() => {
    if (ctx) {
      name = ctx.defaultName ?? '';
      color = ctx.defaultColor ?? randomLabelColor();
      requestAnimationFrame(() => {
        nameEl?.focus();
        nameEl?.select();
      });
    }
  });

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
        <span class="field-label">Name</span>
        <input
          type="text"
          bind:value={name}
          bind:this={nameEl}
          autocomplete="off"
          spellcheck="false"
        />
      </label>

      <div class="color-row">
        <span class="field-label">Color</span>
        <div class="color-controls">
          <input
            type="color"
            bind:value={color}
            class="color-input"
            aria-label="Label color"
            title="Label color"
          />
          <span class="color-swatch" style="background:{color};" aria-hidden="true"></span>
          <button type="button" class="btn-random" onclick={pickRandom}>Random</button>
        </div>
      </div>

      <div class="actions">
        <button type="button" class="btn-secondary" onclick={cancel}>
          {ctx.cancelLabel ?? 'Cancel'}
        </button>
        <button
          type="submit"
          class="btn-primary"
          disabled={name.trim().length === 0}
        >
          {ctx.confirmLabel ?? 'OK'}
        </button>
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

  .btn-random {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    background: var(--layer-03);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    cursor: pointer;
  }

  .btn-random:hover {
    background: var(--layer-01);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  .btn-primary,
  .btn-secondary {
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border: 1px solid transparent;
  }

  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .btn-primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .btn-secondary {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
  }

  .btn-secondary:hover {
    background: var(--layer-03);
  }
</style>
