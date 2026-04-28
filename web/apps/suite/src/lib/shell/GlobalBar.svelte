<script lang="ts">
  import SearchIcon from '../icons/SearchIcon.svelte';
  import HelpIcon from '../icons/HelpIcon.svelte';
  import SettingsIcon from '../icons/SettingsIcon.svelte';
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

  function onSubmit(e: Event): void {
    e.preventDefault();
    const trimmed = query.trim();
    if (trimmed) {
      router.navigate(`/mail/search/${encodeURIComponent(trimmed)}`);
    } else {
      router.navigate('/mail');
    }
  }

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

<style>
  .global-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-05);
    height: var(--spacing-08);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
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
    max-width: 720px;
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
</style>
