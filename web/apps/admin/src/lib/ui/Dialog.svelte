<!--
  Dialog wrapper around bits-ui's Dialog primitive.

  Usage:
    <Dialog bind:open title="Create principal">
      {#snippet trigger()}
        <button type="button">Open</button>
      {/snippet}
      ...dialog body content...
    </Dialog>

  The trigger snippet is rendered outside the overlay; if omitted, open the
  dialog programmatically via bind:open.
-->
<script lang="ts">
  import { Dialog as BitsDialog } from 'bits-ui';

  interface Props {
    open?: boolean;
    title: string;
    description?: string;
    /** Width of the dialog panel. Defaults to 480px. */
    width?: string;
    /** Snippet for the trigger element rendered outside the overlay. */
    trigger?: import('svelte').Snippet;
    children?: import('svelte').Snippet;
  }

  let {
    open = $bindable(false),
    title,
    description,
    width = '480px',
    trigger,
    children,
  }: Props = $props();
</script>

<BitsDialog.Root bind:open>
  {#if trigger}
    <BitsDialog.Trigger>
      {#snippet child({ props })}
        <span {...props}>
          {@render trigger?.()}
        </span>
      {/snippet}
    </BitsDialog.Trigger>
  {/if}

  <BitsDialog.Portal>
    <BitsDialog.Overlay class="dialog-overlay" />
    <BitsDialog.Content class="dialog-content" style="--dialog-width: {width}">
      <div class="dialog-header">
        <BitsDialog.Title class="dialog-title">{title}</BitsDialog.Title>
        {#if description}
          <BitsDialog.Description class="dialog-description">{description}</BitsDialog.Description>
        {/if}
        <BitsDialog.Close class="dialog-close" aria-label="Close dialog">
          <span aria-hidden="true">x</span>
        </BitsDialog.Close>
      </div>
      <div class="dialog-body">
        {@render children?.()}
      </div>
    </BitsDialog.Content>
  </BitsDialog.Portal>
</BitsDialog.Root>

<style>
  :global(.dialog-overlay) {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 100;
    animation: fade-in var(--duration-fast-02) var(--easing-productive-enter);
  }

  :global(.dialog-content) {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(var(--dialog-width, 480px), calc(100vw - var(--spacing-08)));
    max-height: calc(100vh - var(--spacing-08));
    max-height: calc(100dvh - var(--spacing-08));
    overflow-y: auto;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    z-index: 101;
    animation: slide-up var(--duration-fast-02) var(--easing-productive-enter);
    outline: none;
  }

  :global(.dialog-header) {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-04);
    padding: var(--spacing-06) var(--spacing-06) var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  :global(.dialog-title) {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  :global(.dialog-description) {
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
    margin: var(--spacing-02) 0 0;
  }

  :global(.dialog-close) {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: 18px;
    font-weight: 400;
    line-height: 1;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
    cursor: pointer;
    background: none;
    border: none;
    padding: 0;
  }

  :global(.dialog-close:hover) {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  :global(.dialog-body) {
    padding: var(--spacing-06);
  }

  @keyframes fade-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  @keyframes slide-up {
    from {
      opacity: 0;
      transform: translate(-50%, calc(-50% + 12px));
    }
    to {
      opacity: 1;
      transform: translate(-50%, -50%);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    :global(.dialog-overlay),
    :global(.dialog-content) {
      animation: none;
    }
  }
</style>
