<script lang="ts">
  import SearchIcon from '../icons/SearchIcon.svelte';
  import HelpIcon from '../icons/HelpIcon.svelte';
  import SettingsIcon from '../icons/SettingsIcon.svelte';
  import FilterIcon from '../icons/FilterIcon.svelte';
  import AppSwitcherMenu from './AppSwitcherMenu.svelte';
  import AdvancedSearchPanel from '../mail/AdvancedSearchPanel.svelte';
  import { sync } from '../../lib/jmap/sync.svelte';
  import { router } from '../../lib/router/router.svelte';
  import { help } from '../help/help.svelte';

  interface Props {
    placeholder?: string;
  }
  let { placeholder = 'Search mail' }: Props = $props();

  // Local state mirrors the active search query in the URL when present.
  let query = $state('');

  $effect(() => {
    if (router.matches('mail', 'search')) {
      query = decodeURIComponent(router.parts[2] ?? '');
    } else if (!router.matches('mail', 'thread')) {
      // Navigating away from /mail/search clears the input. (Thread routes
      // come from a search result — keep the query around so the user
      // can return to it.)
      query = '';
    }
  });

  // Advanced-search panel toggle state. The panel floats as a fixed popover
  // below the bar so it does not push the sidebar or content columns down.
  let panelOpen = $state(false);

  function onSubmit(e: Event): void {
    e.preventDefault();
    const trimmed = query.trim();
    if (trimmed) {
      router.navigate(`/mail/search/${encodeURIComponent(trimmed)}`);
    } else {
      router.navigate('/mail');
    }
    // Close the panel after a free-text search so it doesn't sit open.
    panelOpen = false;
  }

  function togglePanel(): void {
    panelOpen = !panelOpen;
  }

  function handlePanelSearch(q: string): void {
    if (q.trim()) {
      query = q;
      router.navigate(`/mail/search/${encodeURIComponent(q)}`);
    } else {
      router.navigate('/mail');
    }
    panelOpen = false;
  }

  // The current URL query — passed to the panel so it can pre-populate.
  let currentQuery = $derived(
    router.matches('mail', 'search') ? decodeURIComponent(router.parts[2] ?? '') : '',
  );

  // Hide the indicator when connected; show it during reconnecting /
  // disconnected so the user knows live updates aren't flowing.
  let indicatorVisible = $derived(
    sync.connectionState === 'reconnecting' ||
      sync.connectionState === 'disconnected',
  );
  let indicatorLabel = $derived(
    sync.connectionState === 'disconnected' ? 'Disconnected' : 'Reconnecting…',
  );
</script>

<header class="global-bar">
  <div class="brand-area">
    <AppSwitcherMenu currentApp="mail" />
    <a class="brand" href="/" aria-label="Herold home">Herold</a>
  </div>

  <form class="search" onsubmit={onSubmit} role="search">
    <SearchIcon size={18} />
    <input
      type="search"
      {placeholder}
      bind:value={query}
      aria-label={placeholder}
      spellcheck="false"
    />
  </form>

  {#if indicatorVisible}
    <span class="conn" role="status" aria-live="polite">
      <span class="dot" aria-hidden="true"></span>
      {indicatorLabel}
    </span>
  {/if}

  <div class="controls">
    <button
      type="button"
      class="icon-btn"
      class:active={panelOpen}
      aria-label="Advanced search"
      aria-expanded={panelOpen}
      title="Advanced search"
      onclick={togglePanel}
    >
      <FilterIcon size={18} />
    </button>
    <button
      type="button"
      class="icon-btn"
      aria-label="Help"
      title="Keyboard shortcuts"
      onclick={() => help.toggle()}
    >
      <HelpIcon size={20} />
    </button>
    <button
      type="button"
      class="icon-btn"
      aria-label="Settings"
      onclick={() => router.navigate('/settings')}
    >
      <SettingsIcon size={20} />
    </button>
  </div>
</header>

{#if panelOpen}
  <!-- Transparent backdrop captures click-outside to dismiss the panel. -->
  <div
    class="search-panel-backdrop"
    aria-hidden="true"
    onclick={() => (panelOpen = false)}
  ></div>
  <div class="search-panel-popover">
    <AdvancedSearchPanel
      {currentQuery}
      onSearch={handlePanelSearch}
      onClose={() => (panelOpen = false)}
    />
  </div>
{/if}

<style>
  .global-bar {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: 0 var(--spacing-05) 0 0;
    height: var(--spacing-08);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  /* Brand area: fixed-width left column that aligns with the nav
     sidebar below it. The right border provides the visual separation
     between the brand and the search bar that mirrors the sidebar
     border seen in the content area. */
  .brand-area {
    flex: 0 0 240px;
    display: flex;
    align-items: center;
    height: 100%;
    border-right: 1px solid var(--border-subtle-01);
    gap: 0;
    overflow: hidden;
  }

  .brand {
    flex: 1;
    display: flex;
    align-items: center;
    height: 100%;
    padding: 0 var(--spacing-04) 0 var(--spacing-02);
    color: var(--text-primary);
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    letter-spacing: 0.02em;
    text-decoration: none;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .search {
    flex: 1;
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    color: var(--text-helper);
    /* Remove max-width so the search bar fills the remaining content area. */
  }
  .search input {
    flex: 1;
    background: none;
    border: none;
    outline: none;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    min-width: 0;
  }
  .search input::placeholder {
    color: var(--text-helper);
  }
  .search:focus-within {
    color: var(--text-secondary);
    box-shadow: 0 0 0 2px var(--focus);
  }
  .conn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-pill);
    background: var(--layer-02);
    color: var(--text-secondary);
    font-size: var(--type-code-01-size);
  }
  .conn .dot {
    width: 8px;
    height: 8px;
    border-radius: var(--radius-pill);
    background: var(--support-warning);
    animation: pulse 1.4s var(--easing-productive-enter) infinite;
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.4;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .conn .dot {
      animation: none;
    }
  }

  .controls {
    display: flex;
    gap: var(--spacing-01);
  }
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: var(--touch-min);
    min-height: var(--touch-min);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .icon-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .icon-btn.active {
    background: var(--layer-02);
    color: var(--interactive);
  }

  /* On narrow viewports the nav sidebar is hidden, so the brand area
     would waste horizontal space. Collapse it to just the burger icon. */
  @media (max-width: 768px) {
    .brand-area {
      flex: 0 0 auto;
      border-right: none;
    }
    .brand {
      display: none;
    }
  }

  /* ── Advanced-search popover ───────────────────────────────────────────────
     The panel is rendered as a fixed overlay anchored just below the global
     bar so it does not push the sidebar or the content columns down.

     On wide viewports (>768px) the sidebar is 240px wide, so we start the
     popover at left:240px to keep the sidebar fully visible.  On narrow
     viewports the sidebar is hidden so we start at left:0.
  */
  .search-panel-backdrop {
    position: fixed;
    inset: 0;
    /* Transparent — only captures pointer events to close the panel. */
    background: transparent;
    z-index: 200;
  }

  .search-panel-popover {
    position: fixed;
    top: var(--spacing-08); /* height of the global bar (40px) */
    left: 240px;
    right: 0;
    z-index: 201;
    /* Cap width so it doesn't stretch absurdly on very wide screens. */
    max-width: 960px;
  }

  @media (max-width: 768px) {
    .search-panel-popover {
      left: 0;
    }
  }
</style>
