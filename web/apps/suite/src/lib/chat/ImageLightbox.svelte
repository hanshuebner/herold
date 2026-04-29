<script lang="ts">
  /**
   * Full-viewport image lightbox for chat inline images (REQ-CHAT-22).
   *
   * Mounted above the chat overlay tray (z-index 600+). Clicking the
   * backdrop closes; clicking the image itself does not (allows
   * right-click to save). Escape key also closes.
   *
   * Focus is trapped on the close button during display; the previously
   * focused element is restored on close.
   */

  import { onMount, onDestroy } from 'svelte';

  interface Props {
    src: string;
    alt?: string;
    onClose: () => void;
  }
  let { src, alt = 'Image preview', onClose }: Props = $props();

  let closeBtn = $state<HTMLButtonElement | null>(null);
  let previousFocus: Element | null = null;

  onMount(() => {
    previousFocus = document.activeElement;
    requestAnimationFrame(() => closeBtn?.focus());

    const handleKey = (ev: KeyboardEvent): void => {
      if (ev.key === 'Escape') {
        ev.stopPropagation();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKey);
    return () => {
      window.removeEventListener('keydown', handleKey);
    };
  });

  onDestroy(() => {
    if (previousFocus instanceof HTMLElement) {
      previousFocus.focus();
    }
  });

  function handleBackdropClick(ev: MouseEvent): void {
    // Close only when the click lands on the backdrop itself, not on
    // the image or close button (ev.target === backdrop div).
    if (ev.target === ev.currentTarget) {
      onClose();
    }
  }

  function handleBackdropKeydown(ev: KeyboardEvent): void {
    if (ev.key === 'Escape') {
      onClose();
    }
  }
</script>

<div
  class="lightbox-backdrop"
  role="dialog"
  aria-modal="true"
  aria-label="Image preview"
  tabindex="-1"
  onclick={handleBackdropClick}
  onkeydown={handleBackdropKeydown}
>
  <button
    bind:this={closeBtn}
    class="close-btn"
    type="button"
    aria-label="Close image preview"
    onclick={onClose}
  >
    &times;
  </button>
  <img
    class="lightbox-img"
    {src}
    {alt}
  />
</div>

<style>
  .lightbox-backdrop {
    position: fixed;
    inset: 0;
    z-index: 620;
    background: rgba(0, 0, 0, 0.85);
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .lightbox-img {
    max-width: min(95vw, 1600px);
    max-height: 90vh;
    object-fit: contain;
    border-radius: var(--radius-sm);
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.6);
    cursor: default;
  }

  .close-btn {
    position: absolute;
    top: var(--spacing-04);
    right: var(--spacing-04);
    width: 36px;
    height: 36px;
    border-radius: var(--radius-pill);
    background: rgba(255, 255, 255, 0.15);
    border: 1px solid rgba(255, 255, 255, 0.3);
    color: #fff;
    font-size: 22px;
    line-height: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .close-btn:hover {
    background: rgba(255, 255, 255, 0.28);
  }

  .close-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }
</style>
