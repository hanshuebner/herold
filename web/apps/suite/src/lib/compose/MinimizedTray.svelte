<script lang="ts">
  /**
   * Bottom-right tray of minimized compose snapshots. Each chip
   * carries a label (subject or first recipient) and a discard X;
   * clicking the chip body restores the snapshot into the active
   * compose.
   */
  import { composeStack } from './compose-stack.svelte';

  function shorten(text: string, n = 32): string {
    return text.length <= n ? text : `${text.slice(0, n - 1)}…`;
  }
</script>

{#if composeStack.minimized.length > 0}
  <div class="tray" aria-label="Minimized compose windows">
    {#each composeStack.minimized as snap (snap.key)}
      <div class="chip" role="group">
        <button
          type="button"
          class="chip-body"
          aria-label="Restore compose"
          onclick={() => composeStack.restore(snap.key)}
        >
          <span aria-hidden="true">✎</span>
          <span class="chip-label">
            {shorten(
              snap.subject.trim() ||
                snap.to.split(',')[0]?.trim() ||
                '(empty)',
            )}
          </span>
        </button>
        <button
          type="button"
          class="chip-close"
          aria-label="Discard"
          title="Discard"
          onclick={() => composeStack.discard(snap.key)}
        >
          ×
        </button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .tray {
    position: fixed;
    right: var(--spacing-05);
    bottom: var(--spacing-05);
    display: flex;
    flex-direction: row-reverse;
    gap: var(--spacing-03);
    z-index: 800;
    pointer-events: none;
  }
  .chip {
    pointer-events: auto;
    display: inline-flex;
    align-items: stretch;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    overflow: hidden;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  .chip-body {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .chip-body:hover {
    background: var(--layer-03);
  }
  .chip-label {
    max-width: 16ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chip-close {
    color: var(--text-helper);
    padding: 0 var(--spacing-03);
    border-left: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .chip-close:hover {
    background: var(--support-error);
    color: var(--text-on-color);
  }

  /* Compose modal can extend most of the screen at narrow viewports
     so the tray relocates above the modal's bottom edge. */
  @media (max-width: 640px) {
    .tray {
      right: var(--spacing-04);
      bottom: var(--spacing-04);
    }
    .chip-label {
      max-width: 10ch;
    }
  }
</style>
