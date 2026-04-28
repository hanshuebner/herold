<script lang="ts">
  import { mail } from './store.svelte';
  import CheckSquareIcon from '../icons/CheckSquareIcon.svelte';
  import ChevronDownIcon from '../icons/ChevronDownIcon.svelte';

  // The chooser sits at the top of the message list and lets the user
  // change the selection set without per-row checkbox bookkeeping. The
  // square button toggles between "all visible selected" and "none
  // selected"; the chevron opens a menu with the same options spelled
  // out plus Read/Unread/Starred/Unstarred filters (issue #10).

  let menuOpen = $state(false);
  let rootEl = $state<HTMLElement | null>(null);

  const selectedCount = $derived(mail.listSelectedIds.size);
  const visibleCount = $derived(mail.listEmails.length);
  const allSelected = $derived(visibleCount > 0 && selectedCount === visibleCount);
  const someSelected = $derived(selectedCount > 0 && !allSelected);

  function toggleAll(): void {
    if (selectedCount > 0) mail.clearSelection();
    else mail.selectAllVisible();
  }
  function close(): void {
    menuOpen = false;
  }
  function pick(action: 'all' | 'none' | 'read' | 'unread' | 'starred' | 'unstarred'): void {
    switch (action) {
      case 'all':
        mail.selectAllVisible();
        break;
      case 'none':
        mail.clearSelection();
        break;
      case 'read':
        mail.selectVisibleWhere((e) => Boolean(e.keywords.$seen));
        break;
      case 'unread':
        mail.selectVisibleWhere((e) => !e.keywords.$seen);
        break;
      case 'starred':
        mail.selectVisibleWhere((e) => Boolean(e.keywords.$flagged));
        break;
      case 'unstarred':
        mail.selectVisibleWhere((e) => !e.keywords.$flagged);
        break;
    }
    close();
  }

  function onDocClick(ev: MouseEvent): void {
    if (!menuOpen) return;
    const t = ev.target as Node | null;
    if (rootEl && t && !rootEl.contains(t)) close();
  }
  function onKey(ev: KeyboardEvent): void {
    if (ev.key === 'Escape' && menuOpen) {
      ev.stopPropagation();
      close();
    }
  }
</script>

<svelte:window onclick={onDocClick} onkeydown={onKey} />

<div class="chooser" bind:this={rootEl}>
  <button
    type="button"
    class="check-btn"
    aria-label={allSelected ? 'Deselect all' : someSelected ? 'Clear selection' : 'Select all'}
    aria-pressed={allSelected}
    title={allSelected ? 'Deselect all' : 'Select all'}
    onclick={toggleAll}
  >
    <CheckSquareIcon
      size={18}
      checked={allSelected}
      indeterminate={someSelected}
    />
  </button>
  <button
    type="button"
    class="menu-btn"
    aria-label="Select options"
    aria-haspopup="menu"
    aria-expanded={menuOpen}
    title="Select…"
    onclick={() => (menuOpen = !menuOpen)}
  >
    <ChevronDownIcon size={14} />
  </button>
  {#if menuOpen}
    <ul class="menu" role="menu">
      <li><button type="button" role="menuitem" onclick={() => pick('all')}>All</button></li>
      <li><button type="button" role="menuitem" onclick={() => pick('none')}>None</button></li>
      <li><button type="button" role="menuitem" onclick={() => pick('read')}>Read</button></li>
      <li><button type="button" role="menuitem" onclick={() => pick('unread')}>Unread</button></li>
      <li><button type="button" role="menuitem" onclick={() => pick('starred')}>Starred</button></li>
      <li><button type="button" role="menuitem" onclick={() => pick('unstarred')}>Unstarred</button></li>
    </ul>
  {/if}
</div>

<style>
  .chooser {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 0;
  }
  .check-btn,
  .menu-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 32px;
    color: var(--text-secondary);
    background: transparent;
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .check-btn {
    width: 32px;
  }
  .menu-btn {
    width: 22px;
    margin-left: -2px;
  }
  .check-btn:hover,
  .menu-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .menu {
    position: absolute;
    top: calc(100% + var(--spacing-01));
    left: 0;
    z-index: 200;
    list-style: none;
    margin: 0;
    padding: var(--spacing-02) 0;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    min-width: 9rem;
  }
  .menu li {
    margin: 0;
  }
  .menu button {
    width: 100%;
    text-align: left;
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-primary);
    background: transparent;
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .menu button:hover {
    background: var(--layer-03);
  }
</style>
