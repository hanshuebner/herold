<script lang="ts">
  import { toast } from './toast.svelte';
</script>

{#if toast.current}
  <div
    class="toast"
    class:error={toast.current.kind === 'error'}
    role="status"
    aria-live="polite"
  >
    <span class="message">{toast.current.message}</span>
    {#if toast.current.undo}
      <button
        type="button"
        class="undo"
        onclick={() => {
          void toast.undo();
        }}
      >
        Undo
      </button>
    {/if}
    <button
      type="button"
      class="dismiss"
      aria-label="Dismiss"
      onclick={() => toast.dismiss()}
    >
      ×
    </button>
  </div>
{/if}

<style>
  .toast {
    position: fixed;
    bottom: calc(24px + var(--spacing-06));
    left: 50%;
    transform: translateX(-50%);
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.35);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    z-index: 1000;
    max-width: min(560px, calc(100vw - 2 * var(--spacing-05)));
    animation: rise var(--duration-moderate-01) var(--easing-productive-enter);
  }

  .toast.error {
    background: var(--support-error);
    color: var(--text-on-color);
    border-color: var(--support-error);
  }

  .message {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .undo {
    color: var(--interactive);
    font-weight: 600;
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .toast.error .undo {
    color: var(--text-on-color);
  }
  .undo:hover {
    background: var(--layer-03);
  }

  .dismiss {
    color: var(--text-helper);
    font-size: 18px;
    line-height: 1;
    width: 24px;
    height: 24px;
    border-radius: var(--radius-pill);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .toast.error .dismiss {
    color: var(--text-on-color);
  }
  .dismiss:hover {
    background: var(--layer-03);
  }

  @keyframes rise {
    from {
      transform: translate(-50%, 16px);
      opacity: 0;
    }
    to {
      transform: translate(-50%, 0);
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .toast {
      animation: none;
    }
  }
</style>
