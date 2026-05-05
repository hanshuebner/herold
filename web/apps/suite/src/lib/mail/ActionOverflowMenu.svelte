<script lang="ts">
  /**
   * Overflow menu for action toolbars (re #60).
   *
   * Renders a "More actions" trigger button. Clicking it opens a vertical
   * dropdown listing actions with label, optional shortcut hint, and icon.
   * Closes on: outside click, Escape key, or item selection.
   *
   * Callers pass fully-rendered action entries so this component stays
   * generic and free of business-logic coupling.
   */
  import { t } from '../i18n/i18n.svelte';

  interface OverflowItem {
    id: string;
    label: string;
    shortcut?: string;
    icon?: import('svelte').Snippet;
    disabled?: boolean;
    onclick: () => void;
  }

  interface Props {
    items: OverflowItem[];
    /** Label for the trigger button; defaults to t('actions.moreActions'). */
    triggerLabel?: string;
    /** Extra CSS class for the trigger button. */
    class?: string;
  }

  let { items, triggerLabel, class: extraClass }: Props = $props();

  let open = $state(false);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let menuEl = $state<HTMLDivElement | null>(null);

  function toggle(): void {
    open = !open;
  }

  function close(): void {
    open = false;
  }

  function handleItemClick(item: OverflowItem): void {
    if (item.disabled) return;
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

<div class="overflow-wrapper">
  <button
    type="button"
    class="overflow-trigger {extraClass ?? ''}"
    bind:this={triggerEl}
    onclick={toggle}
    aria-expanded={open}
    aria-haspopup="menu"
    aria-label={triggerLabel ?? t('actions.moreActions')}
    title={triggerLabel ?? t('actions.moreActions')}
  >
    <span class="overflow-dots" aria-hidden="true">&#x22EE;</span>
    <span class="overflow-label">{triggerLabel ?? t('actions.moreActions')}</span>
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="overflow-menu"
      role="menu"
      tabindex="-1"
      bind:this={menuEl}
      onkeydown={handleKeydown}
    >
      {#each items as item (item.id)}
        <button
          type="button"
          class="overflow-item"
          class:disabled={item.disabled}
          role="menuitem"
          disabled={item.disabled}
          onclick={() => handleItemClick(item)}
        >
          {#if item.icon}
            <span class="overflow-item-icon" aria-hidden="true">
              {@render item.icon()}
            </span>
          {/if}
          <span class="overflow-item-label">{item.label}</span>
          {#if item.shortcut}
            <span class="overflow-item-shortcut" aria-hidden="true">{item.shortcut}</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .overflow-wrapper {
    position: relative;
    display: inline-flex;
  }

  .overflow-trigger {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    color: var(--text-secondary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: 32px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .overflow-trigger:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .overflow-dots {
    font-size: 16px;
    line-height: 1;
    letter-spacing: 0;
  }

  .overflow-label {
    font-size: var(--type-body-compact-01-size);
  }

  .overflow-menu {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    right: 0;
    z-index: 300;
    min-width: 220px;
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
    display: flex;
    flex-direction: column;
    padding: var(--spacing-02) 0;
  }

  .overflow-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    text-align: left;
    width: 100%;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .overflow-item:hover:not(.disabled) {
    background: var(--layer-01);
  }

  .overflow-item.disabled {
    color: var(--text-helper);
    cursor: not-allowed;
  }

  .overflow-item-icon {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    color: var(--text-secondary);
  }

  .overflow-item-label {
    flex: 1;
  }

  .overflow-item-shortcut {
    color: var(--text-helper);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    background: var(--layer-02);
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-sm);
    flex-shrink: 0;
  }

  @media print {
    .overflow-wrapper {
      display: none !important;
    }
  }
</style>
