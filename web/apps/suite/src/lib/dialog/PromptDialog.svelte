<script lang="ts">
  /**
   * Singleton text-prompt host. Mirrors ConfirmDialog: renders the
   * pending request from `prompt` (lib/dialog/prompt.svelte.ts).
   * Mounted once near the top of Shell so it overlays everything else.
   */
  import { prompt } from './prompt.svelte';

  let ctx = $derived(prompt.pending);
  let value = $state('');
  let inputEl = $state<HTMLInputElement | null>(null);

  // Reset the value cell every time a new prompt opens. Reading
  // ctx.defaultValue tracks; the seed runs only on open transitions
  // because the rest of the body never writes back to defaultValue.
  $effect(() => {
    if (ctx) {
      value = ctx.defaultValue ?? '';
      requestAnimationFrame(() => {
        inputEl?.focus();
        inputEl?.select();
      });
    }
  });

  // Capture-phase Escape handler so we close the dialog before the
  // global keyboard engine sees the event and dispatches it to (e.g.)
  // the compose Escape binding underneath.
  $effect(() => {
    if (!ctx) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        prompt.decide(null);
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });

  function submit(): void {
    if (!ctx) return;
    const trimmed = value.trim();
    if (!ctx.allowEmpty && trimmed.length === 0) return;
    prompt.decide(ctx.allowEmpty ? value : trimmed);
  }

  function cancel(): void {
    prompt.decide(null);
  }

  function onSubmit(e: Event): void {
    e.preventDefault();
    submit();
  }
</script>

{#if ctx}
  <div class="backdrop" aria-hidden="true" onclick={cancel}></div>
  <div
    class="modal-wrapper"
    role="dialog"
    aria-modal="true"
    aria-labelledby={ctx.title ? 'pd-title' : undefined}
    aria-describedby={ctx.message ? 'pd-message' : undefined}
  >
    <form
      class="modal"
      onsubmit={onSubmit}
    >
      {#if ctx.title}
        <h2 id="pd-title" class="title">{ctx.title}</h2>
      {/if}
      {#if ctx.message}
        <p id="pd-message" class="body">{ctx.message}</p>
      {/if}
      <label class="field">
        <span class="field-label">{ctx.label}</span>
        <input
          type="text"
          bind:value
          bind:this={inputEl}
          placeholder={ctx.placeholder ?? ''}
          autocomplete="off"
          spellcheck="false"
        />
      </label>
      <div class="actions">
        <button type="button" class="btn-secondary" onclick={cancel}>
          {ctx.cancelLabel ?? 'Cancel'}
        </button>
        <button
          type="submit"
          class={ctx.kind === 'danger' ? 'btn-danger' : 'btn-primary'}
          disabled={!ctx.allowEmpty && value.trim().length === 0}
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
  .body {
    margin: 0;
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
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

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }
  .btn-primary,
  .btn-secondary,
  .btn-danger {
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
  .btn-danger {
    background: var(--support-error);
    color: var(--text-on-color);
    border: 1px solid transparent;
  }
  .btn-danger:hover:not(:disabled) {
    filter: brightness(0.9);
  }
  .btn-danger:disabled {
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
