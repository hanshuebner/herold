<script lang="ts">
  /**
   * Singleton confirm-dialog host. Renders the pending request from
   * `confirm` (lib/dialog/confirm.svelte.ts). Mounted once near the
   * top of Shell so it overlays everything else.
   */
  import { confirm } from './confirm.svelte';
  import Button from '@herold/design-system/Button.svelte';

  let ctx = $derived(confirm.pending);

  // Focus the primary action when the dialog opens so Enter confirms
  // and Escape (handled below) cancels. The shared Button does not
  // expose a bindable element ref, so reach for it by test hook.
  $effect(() => {
    if (ctx) {
      requestAnimationFrame(() => {
        const el = document.querySelector<HTMLButtonElement>(
          '[data-testid="confirm-dialog-confirm"]',
        );
        el?.focus();
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
        <Button variant="secondary" onclick={() => confirm.decide(false)}>
          {ctx.cancelLabel ?? 'Cancel'}
        </Button>
        <Button
          variant={ctx.kind === 'danger' ? 'danger' : 'primary'}
          testid="confirm-dialog-confirm"
          onclick={() => confirm.decide(true)}
        >
          {ctx.confirmLabel ?? 'OK'}
        </Button>
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
</style>
