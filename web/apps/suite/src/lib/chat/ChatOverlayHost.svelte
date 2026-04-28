<script lang="ts">
  /**
   * Positions floating chat overlay windows along the bottom-right of the
   * viewport.  Expanded windows are 320px wide with 8px gaps.  Minimized
   * windows collapse to 200px-wide title bars and stack horizontally.
   *
   * This component is mounted unconditionally in the shell (below the
   * mail/chat content area) and rendered only when the overlay store has
   * open windows.
   *
   * Hidden on phone breakpoints via CSS media query (same threshold as
   * ChatRail: <768px).
   */

  import { chatOverlay } from './overlay-store.svelte';
  import ChatOverlayWindow from './ChatOverlayWindow.svelte';
</script>

{#if chatOverlay.windows.length > 0}
  <div class="overlay-host" aria-label="Chat windows" role="region">
    {#each chatOverlay.windows as win (win.key)}
      <ChatOverlayWindow
        windowKey={win.key}
        conversationId={win.conversationId}
        minimized={win.minimized}
      />
    {/each}
  </div>
{/if}

<style>
  .overlay-host {
    position: fixed;
    bottom: 0;
    /* right tracks the chat rail width so overlays slide left when the rail
       expands.  --chat-rail-width is set on :root by ChatRail.svelte's $effect;
       the 16px gap matches the collapsed baseline (64px + 16px = 80px). */
    right: calc(var(--chat-rail-width, 64px) + 16px);
    display: flex;
    align-items: flex-end;
    gap: var(--spacing-03);
    z-index: 400;
    /* Pointer events only on the windows themselves; the gap is transparent. */
    pointer-events: none;
    /* Animate the right offset in sync with the rail's width transition.
       Uses the same duration/easing token as the rail itself. */
    transition: right var(--duration-moderate-01) var(--easing-productive-enter);
  }

  @media (prefers-reduced-motion: reduce) {
    .overlay-host {
      transition: none;
    }
  }

  .overlay-host > :global(*) {
    pointer-events: auto;
  }

  /* On phone breakpoints both the rail and the overlays are suppressed. */
  @media (max-width: 767px) {
    .overlay-host {
      display: none;
    }
  }
</style>
