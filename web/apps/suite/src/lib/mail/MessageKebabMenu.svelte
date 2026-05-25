<script lang="ts">
  /**
   * Per-message kebab menu surfaced from the accordion header (REQ-UI-21b,
   * `02-mail-basics.md` § Per-message context menu).
   *
   * Conservative slice (re #98): kebab carries verbs that have an inherently
   * per-message use case (download .eml, show original, print this message)
   * or whose thread-scoped variant is wrong for multi-sender threads
   * (delete one msg, mark one msg unread, mark unread from here, report
   * spam, report phishing, filter messages like this).
   *
   * Reply / forward live in the fixed reply bar at the thread bottom;
   * block sender lives in ThreadToolbar; report illegal and translate
   * are deferred.
   *
   * Patterned after ActionOverflowMenu.svelte but icon-only trigger sized
   * to fit inside the message header next to the date.
   */
  import { t } from '../i18n/i18n.svelte';

  export interface KebabItem {
    id: string;
    label: string;
    danger?: boolean;
    onclick: () => void;
  }

  interface Props {
    items: KebabItem[];
  }
  let { items }: Props = $props();

  let open = $state(false);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let menuEl = $state<HTMLDivElement | null>(null);

  function toggle(): void {
    open = !open;
  }

  function close(): void {
    open = false;
  }

  function handleItemClick(item: KebabItem): void {
    item.onclick();
    close();
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      close();
      triggerEl?.focus();
    }
  }

  function handleOutsideClick(e: MouseEvent): void {
    const target = e.target as Node;
    if (triggerEl && triggerEl.contains(target)) return;
    if (menuEl && menuEl.contains(target)) return;
    close();
  }

  $effect(() => {
    if (!open) return;
    document.addEventListener('click', handleOutsideClick, true);
    return () => {
      document.removeEventListener('click', handleOutsideClick, true);
    };
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<span
  class="kebab-wrapper"
  onclick={(e) => e.stopPropagation()}
  onkeydown={(e) => e.stopPropagation()}
  role="presentation"
>
  <button
    type="button"
    class="kebab-trigger"
    bind:this={triggerEl}
    onclick={toggle}
    aria-expanded={open}
    aria-haspopup="menu"
    aria-label={t('msg.kebab.openLabel')}
    title={t('msg.kebab.openLabel')}
  >
    <span class="kebab-dots" aria-hidden="true">&#x22EE;</span>
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="kebab-menu"
      role="menu"
      tabindex="-1"
      bind:this={menuEl}
      onkeydown={handleKeydown}
    >
      {#each items as item (item.id)}
        <button
          type="button"
          class="kebab-item"
          class:danger={item.danger}
          role="menuitem"
          onclick={() => handleItemClick(item)}
        >
          <span class="kebab-item-label">{item.label}</span>
        </button>
      {/each}
    </div>
  {/if}
</span>

<style>
  .kebab-wrapper {
    position: relative;
    display: inline-flex;
  }

  .kebab-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    background: transparent;
    color: var(--text-secondary);
    border: none;
    border-radius: 50%;
    cursor: pointer;
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .kebab-trigger:hover {
    background: var(--layer-01);
    color: var(--text-primary);
  }

  .kebab-trigger[aria-expanded='true'] {
    background: var(--layer-01);
    color: var(--text-primary);
  }

  .kebab-dots {
    font-size: 18px;
    line-height: 1;
  }

  .kebab-menu {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    right: 0;
    z-index: 300;
    min-width: 240px;
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    display: flex;
    flex-direction: column;
    padding: var(--spacing-02) 0;
  }

  .kebab-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    background: transparent;
    color: var(--text-primary);
    border: none;
    font-size: var(--type-body-compact-01-size);
    text-align: left;
    width: 100%;
    min-height: var(--touch-min);
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .kebab-item:hover {
    background: var(--layer-01);
  }

  .kebab-item.danger {
    color: var(--support-error);
  }

  .kebab-item-label {
    flex: 1;
  }

  @media print {
    .kebab-wrapper {
      display: none !important;
    }
  }
</style>
