<script lang="ts" module>
  export interface LightboxItem {
    /** Resource URL (already built with disposition=inline where needed). */
    url: string;
    /** Filename rendered in the bottom toolbar; omit to hide the name/download row. */
    name?: string;
    /** Override download URL (defaults to `url`). */
    downloadUrl?: string;
    /** Lightbox kind. 'image' renders <img>; 'pdf' renders <iframe>. */
    kind?: 'image' | 'pdf';
  }
</script>

<script lang="ts">
  /**
   * Shared full-viewport preview lightbox used by both the chat image
   * preview (REQ-CHAT-22) and the mail attachment viewer (REQ-MAIL-23).
   *
   * Supported `kind` values per item:
   *   - 'image' (default): renders an <img>. Right-click-save still works.
   *   - 'pdf': renders an <iframe> pointing at the URL so the browser's
   *     built-in PDF viewer handles paging / zoom.
   *
   * Navigation (REQ-MAIL-23, issue #518): the caller passes the full
   * ordered `items` list (a message's viewable attachments, or a single
   * entry for the chat / inline-image preview that has no siblings) plus
   * the `index` of the one to open. ArrowLeft/ArrowRight and the
   * on-screen prev/next buttons step through `items`; the boundary
   * behaviour stops at the ends (the control disables rather than
   * wrapping). A "{current} of {total}" indicator appears only when
   * there is more than one item, so the single-item chat / inline-image
   * callers are visually unaffected.
   *
   * Behaviour:
   *   - Backdrop click closes; image / pdf click does not (so the user
   *     can interact with the content).
   *   - Escape closes.
   *   - Focus is trapped on the close button while open; the previous
   *     focus owner is restored on close.
   *   - When the current item has a `name` a small toolbar appears
   *     showing the filename plus a Download link.
   */

  import { onMount, onDestroy } from 'svelte';
  import { t } from '../i18n/i18n.svelte';

  interface Props {
    /** Ordered list of viewable items; a single-entry array has no nav UI. */
    items: LightboxItem[];
    /** Index into `items` to open at. */
    index?: number;
    /** Image alt text or PDF iframe title. */
    alt?: string;
    onClose: () => void;
  }
  let { items, index: startIndex = 0, alt = 'Preview', onClose }: Props = $props();

  // Seeded once from the opening index; subsequent navigation is purely
  // internal state (the Lightbox owns stepping, the caller never reopens
  // the same instance at a different index).
  // svelte-ignore state_referenced_locally -- intentional: see comment above.
  let index = $state(startIndex);
  let current = $derived(items[index] ?? items[0]!);
  let hasPrev = $derived(index > 0);
  let hasNext = $derived(index < items.length - 1);

  function goPrev(): void {
    if (hasPrev) index -= 1;
  }

  function goNext(): void {
    if (hasNext) index += 1;
  }

  let closeBtn = $state<HTMLButtonElement | null>(null);
  let previousFocus: Element | null = null;

  onMount(() => {
    previousFocus = document.activeElement;
    requestAnimationFrame(() => closeBtn?.focus());

    const handleKey = (ev: KeyboardEvent): void => {
      if (ev.key === 'Escape') {
        ev.stopPropagation();
        onClose();
        return;
      }
      if (items.length <= 1) return;
      if (ev.key === 'ArrowRight') {
        ev.stopPropagation();
        goNext();
      } else if (ev.key === 'ArrowLeft') {
        ev.stopPropagation();
        goPrev();
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
  aria-label={current.name ?? alt}
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
  {#if items.length > 1}
    <button
      class="nav-btn prev-btn"
      type="button"
      aria-label={t('att.lightbox.prev')}
      disabled={!hasPrev}
      onclick={goPrev}
    >
      &lsaquo;
    </button>
    <button
      class="nav-btn next-btn"
      type="button"
      aria-label={t('att.lightbox.next')}
      disabled={!hasNext}
      onclick={goNext}
    >
      &rsaquo;
    </button>
  {/if}
  {#if (current.kind ?? 'image') === 'image'}
    <img class="lightbox-img" src={current.url} {alt} />
  {:else if current.kind === 'pdf'}
    <iframe class="lightbox-pdf" src={current.url} title={alt}></iframe>
  {/if}
  {#if current.name || items.length > 1}
    <div class="lightbox-toolbar">
      {#if current.name}
        <span class="lightbox-name">{current.name}</span>
        <a
          class="lightbox-action"
          href={current.downloadUrl ?? current.url}
          download={current.name}
        >
          Download
        </a>
      {/if}
      {#if items.length > 1}
        <span class="lightbox-position">
          {t('att.lightbox.position', { current: index + 1, total: items.length })}
        </span>
      {/if}
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

  .nav-btn {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    width: 44px;
    height: 44px;
    border-radius: var(--radius-pill);
    background: rgba(255, 255, 255, 0.15);
    border: 1px solid rgba(255, 255, 255, 0.3);
    color: #fff;
    font-size: 28px;
    line-height: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .prev-btn {
    left: var(--spacing-04);
  }

  .next-btn {
    right: var(--spacing-04);
  }

  .nav-btn:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.28);
  }

  .nav-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  .nav-btn:disabled {
    opacity: 0.3;
    cursor: default;
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

  .lightbox-position {
    font-size: var(--type-body-compact-01-size);
    color: rgba(255, 255, 255, 0.8);
    white-space: nowrap;
  }
</style>
