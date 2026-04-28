<script lang="ts">
  /**
   * Singleton confirm-dialog host. Renders the pending request from
   * `confirm` (lib/dialog/confirm.svelte.ts). Mounted once near the
   * top of Shell so it overlays everything else.
   */
  import { confirm } from './confirm.svelte';

  let ctx = $derived(confirm.pending);
  let confirmBtn = $state<HTMLButtonElement | null>(null);

  // Focus the primary action when the dialog opens so Enter confirms
  // and Escape (handled below) cancels.
  $effect(() => {
    if (ctx) {
      requestAnimationFrame(() => confirmBtn?.focus());
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
        confirm.decide(false);
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });
</script>

{#if ctx}
  <div class="backdrop" aria-hidden="true" onclick={() => confirm.decide(false)}></div>
  <div class="modal-wrapper">
    <div
      class="modal"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby={ctx.title ? 'cd-title' : undefined}
      aria-describedby="cd-message"
    >
      {#if ctx.title}
        <h2 id="cd-title" class="title">{ctx.title}</h2>
      {/if}
      <p id="cd-message" class="body">{ctx.message}</p>
      <div class="actions">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => confirm.decide(false)}
        >
          {ctx.cancelLabel ?? 'Cancel'}
        </button>
        <button
          type="button"
          class={ctx.kind === 'danger' ? 'btn-danger' : 'btn-primary'}
          bind:this={confirmBtn}
          onclick={() => confirm.decide(true)}
        >
          {ctx.confirmLabel ?? 'OK'}
        </button>
      </div>
    </div>
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
    max-width: 420px;
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
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border: 1px solid transparent;
  }
  .btn-primary:hover {
    filter: brightness(1.1);
  }

  .btn-danger {
    background: var(--support-error);
    color: var(--text-on-color);
    border: 1px solid transparent;
  }
  .btn-danger:hover {
    filter: brightness(0.9);
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
