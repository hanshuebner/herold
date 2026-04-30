<script lang="ts">
  /**
   * Shared full-viewport preview lightbox used by both the chat image
   * preview (REQ-CHAT-22) and the mail attachment viewer (REQ-MAIL-23).
   *
   * Supported `kind` values:
   *   - 'image' (default): renders an <img>. Right-click-save still works.
   *   - 'pdf': renders an <iframe> pointing at the URL so the browser's
   *     built-in PDF viewer handles paging / zoom.
   *
   * Behaviour:
   *   - Backdrop click closes; image / pdf click does not (so the user
   *     can interact with the content).
   *   - Escape closes.
   *   - Focus is trapped on the close button while open; the previous
   *     focus owner is restored on close.
   *   - When `name` is supplied a small toolbar appears showing the
   *     filename plus an optional Download link (when `downloadUrl` is
   *     set; defaults to `src`).
   */

  import { onMount, onDestroy } from 'svelte';

  interface Props {
    src: string;
    /** Image alt text or PDF iframe title. */
    alt?: string;
    /** Filename rendered in the bottom toolbar; omit to hide the toolbar. */
    name?: string;
    /** Override download URL (defaults to `src`). */
    downloadUrl?: string;
    /** Lightbox kind. 'image' renders <img>; 'pdf' renders <iframe>. */
    kind?: 'image' | 'pdf';
    onClose: () => void;
  }
  let {
    src,
    alt = 'Preview',
    name,
    downloadUrl,
    kind = 'image',
    onClose,
  }: Props = $props();

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
    if (ev.target === ev.currentTarget) {
      onClose();
    }
  }

  function handleBackdropKeydown(ev: KeyboardEvent): void {
    if (ev.key === 'Escape') {
      ev.stopPropagation();
      onClose();
    }
  }
</script>

<div
  class="lightbox-backdrop"
  role="dialog"
  aria-modal="true"
  aria-label={name ?? alt}
  tabindex="-1"
  onclick={handleBackdropClick}
  onkeydown={handleBackdropKeydown}
>
  <button
    bind:this={closeBtn}
    class="close-btn"
    type="button"
    aria-label="Close preview"
    onclick={onClose}
  >
    &times;
  </button>
  {#if kind === 'image'}
    <img class="lightbox-img" {src} {alt} />
  {:else if kind === 'pdf'}
    <iframe class="lightbox-pdf" {src} title={alt}></iframe>
  {/if}
  {#if name}
    <div class="lightbox-toolbar">
      <span class="lightbox-name">{name}</span>
      <a class="lightbox-action" href={downloadUrl ?? src} download={name}>
        Download
      </a>
    </div>
  {/if}
</div>

<style>
  .lightbox-backdrop {
    position: fixed;
    inset: 0;
    z-index: 620;
    background: rgba(0, 0, 0, 0.85);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: var(--spacing-04);
  }

  .lightbox-img {
    max-width: min(95vw, 1600px);
    max-height: 85vh;
    object-fit: contain;
    border-radius: var(--radius-sm);
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.6);
    cursor: default;
  }

  .lightbox-pdf {
    width: 95vw;
    height: 85vh;
    border: 0;
    background: #fff;
    border-radius: var(--radius-sm);
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.6);
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

  .lightbox-toolbar {
    margin-top: var(--spacing-03);
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    color: #fff;
    background: rgba(0, 0, 0, 0.5);
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
  }

  .lightbox-name {
    font-size: var(--type-body-compact-01-size);
    max-width: 60vw;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .lightbox-action {
    color: #fff;
    text-decoration: underline;
    font-size: var(--type-body-compact-01-size);
  }
</style>
