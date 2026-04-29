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
  import { router } from '../router/router.svelte';
  import ChatOverlayWindow from './ChatOverlayWindow.svelte';

  // Suppress the overlay window only for the conversation the user is
  // currently viewing in the dedicated /chat/conversation/<id> route —
  // that conversation is already shown in the main pane and the overlay
  // would just duplicate it. Windows for other conversations still
  // render so an incoming message in a background chat can pop a
  // visible overlay even while the user is on the chat surface.
  let visibleWindows = $derived.by(() => {
    const activeId =
      router.parts[0] === 'chat' && router.parts[1] === 'conversation'
        ? router.parts[2]
        : null;
    if (!activeId) return chatOverlay.windows;
    return chatOverlay.windows.filter((w) => w.conversationId !== activeId);
  });
</script>

{#if visibleWindows.length > 0}
  <div class="overlay-host" aria-label="Chat windows" role="region">
    {#each visibleWindows as win (win.key)}
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
    right: calc(var(--chat-rail-width, 0px) + 16px);
    display: flex;
    align-items: flex-end;
    gap: var(--spacing-03);
    z-index: 400;
    /* Pointer events only on the windows themselves; the gap is transparent. */
    pointer-events: none;
    transition: none;
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
