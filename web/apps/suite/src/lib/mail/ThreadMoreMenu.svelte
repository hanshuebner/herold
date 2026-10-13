<script lang="ts" module>
  import type { Component } from 'svelte';

  /**
   * One entry of the thread more-menu. An entry with `href` renders as
   * `<a href download>` so the browser handles the save natively
   * (REQ-MAIL-143); every other entry renders as a button calling
   * `onclick`.
   */
  export interface MoreMenuItem {
    id: string;
    label: string;
    /** Tooltip; defaults to `label`. */
    title?: string;
    /** Icon component rendered before the label at 16px. */
    icon?: Component<{ size?: number }>;
    /** Further props passed to `icon`, e.g. the name of an app-action icon. */
    iconProps?: Record<string, unknown>;
    href?: string;
    download?: string;
    onclick?: () => void;
    disabled?: boolean;
    /** Shows a spinner and blocks activation while an action runs. */
    busy?: boolean;
  }

  /**
   * A group of entries rendered below the built-in items, preceded by a
   * separator. Sections without items render nothing, separator
   * included, so a caller can always pass e.g. the app-actions section
   * (REQ-APPS-01) and let an empty list hide it.
   */
  export interface MoreMenuSection {
    id: string;
    /** Accessible name of the group. */
    label?: string;
    items: MoreMenuItem[];
  }
</script>

<script lang="ts">
  /**
   * Thread-level three-dot more-menu on the thread toolbar (REQ-UI-19c).
   *
   * `items` are the built-in thread actions; `sections` are appended
   * below them, each after a separator. Closes on outside click, Escape,
   * Tab, or activating an item. Arrow keys, Home and End move focus
   * between the entries.
   */
  import { tick } from 'svelte';
  import { t } from '../i18n/i18n.svelte';

  interface Props {
    items: MoreMenuItem[];
    sections?: MoreMenuSection[];
    /** Trigger label; defaults to t('thread.moreActions'). */
    triggerLabel?: string;
  }

  let { items, sections = [], triggerLabel }: Props = $props();

  let open = $state(false);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let menuEl = $state<HTMLDivElement | null>(null);

  let label = $derived(triggerLabel ?? t('thread.moreActions'));
  let visibleSections = $derived(sections.filter((s) => s.items.length > 0));

  function entries(): HTMLElement[] {
    if (!menuEl) return [];
    return Array.from(
      menuEl.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])'),
    );
  }

  async function toggle(): Promise<void> {
    open = !open;
    if (open) {
      await tick();
      entries()[0]?.focus();
    }
  }

  function close(): void {
    open = false;
  }

  function activate(e: MouseEvent, item: MoreMenuItem): void {
    if (item.disabled || item.busy) {
      e.preventDefault();
      return;
    }
    item.onclick?.();
    close();
  }

  function handleKeydown(e: KeyboardEvent): void {
    const list = entries();
    const idx = list.indexOf(document.activeElement as HTMLElement);
    switch (e.key) {
      case 'Escape':
        e.preventDefault();
        e.stopPropagation();
        close();
        triggerEl?.focus();
        return;
      case 'Tab':
        close();
        return;
      case 'ArrowDown':
        e.preventDefault();
        list[(idx + 1) % list.length]?.focus();
        return;
      case 'ArrowUp':
        e.preventDefault();
        list[(idx - 1 + list.length) % list.length]?.focus();
        return;
      case 'Home':
        e.preventDefault();
        list[0]?.focus();
        return;
      case 'End':
        e.preventDefault();
        list[list.length - 1]?.focus();
        return;
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

{#snippet entry(item: MoreMenuItem)}
  {@const inactive = Boolean(item.disabled || item.busy)}
  {#if item.href !== undefined}
    <a
      class="menu-item"
      class:inactive
      role="menuitem"
      href={item.href}
      download={item.download}
      title={item.title ?? item.label}
      aria-disabled={inactive || undefined}
      data-item-id={item.id}
      onclick={(e) => activate(e, item)}
    >
      {@render body(item)}
    </a>
  {:else}
    <button
      type="button"
      class="menu-item"
      class:inactive
      role="menuitem"
      title={item.title ?? item.label}
      aria-disabled={inactive || undefined}
      aria-busy={item.busy || undefined}
      data-item-id={item.id}
      onclick={(e) => activate(e, item)}
    >
      {@render body(item)}
    </button>
  {/if}
{/snippet}

{#snippet body(item: MoreMenuItem)}
  <span class="menu-item-icon" aria-hidden="true">
    {#if item.busy}
      <span class="spinner"></span>
    {:else if item.icon}
      <item.icon size={16} {...item.iconProps} />
    {/if}
  </span>
  <span class="menu-item-label">{item.label}</span>
{/snippet}

<div class="more-wrapper">
  <button
    type="button"
    class="more-trigger"
    bind:this={triggerEl}
    onclick={() => void toggle()}
    aria-expanded={open}
    aria-haspopup="menu"
    aria-label={label}
    title={label}
  >
    <span class="more-dots" aria-hidden="true">&#x22EE;</span>
  </button>

  {#if open}
    <div
      class="more-menu"
      role="menu"
      tabindex="-1"
      aria-label={label}
      bind:this={menuEl}
      onkeydown={handleKeydown}
    >
      {#each items as item (item.id)}
        {@render entry(item)}
      {/each}
      {#each visibleSections as section (section.id)}
        <div class="separator" role="separator"></div>
        <div class="section" role="group" aria-label={section.label} data-section-id={section.id}>
          {#each section.items as item (item.id)}
            {@render entry(item)}
          {/each}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .more-wrapper {
    position: relative;
    display: inline-flex;
  }

  .more-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    background: transparent;
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .more-trigger:hover,
  .more-trigger[aria-expanded='true'] {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .more-dots {
    font-size: 18px;
    line-height: 1;
  }

  .more-menu {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    left: 0;
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

  .section {
    display: flex;
    flex-direction: column;
  }

  .separator {
    height: 1px;
    margin: var(--spacing-02) 0;
    background: var(--border-subtle-01);
  }

  .menu-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    background: transparent;
    color: var(--text-primary);
    border: none;
    font-size: var(--type-body-compact-01-size);
    text-align: left;
    text-decoration: none;
    width: 100%;
    min-height: var(--touch-min);
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .menu-item:hover:not(.inactive),
  .menu-item:focus-visible {
    background: var(--layer-01);
  }

  .menu-item.inactive {
    color: var(--text-helper);
    cursor: not-allowed;
  }

  .menu-item-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    flex-shrink: 0;
    color: var(--text-secondary);
  }

  .menu-item-label {
    flex: 1;
  }

  .spinner {
    width: 12px;
    height: 12px;
    border: 2px solid var(--border-subtle-02);
    border-top-color: var(--text-secondary);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner {
      animation: none;
    }
  }

  @media print {
    .more-wrapper {
      display: none !important;
    }
  }
</style>
