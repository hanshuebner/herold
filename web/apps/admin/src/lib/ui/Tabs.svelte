<!--
  Tabs wrapper around bits-ui's Tabs primitive.

  Usage:
    <Tabs bind:value tabs={[
      { value: 'profile', label: 'Profile' },
      { value: 'password', label: 'Password' },
    ]}>
      {#snippet panel('profile')}
        ...
      {/snippet}
    </Tabs>

  Alternatively use TabPanel children directly inside the root:
    <Tabs bind:value tabs={[...]}>
      {children}
    </Tabs>
  and render conditionally based on `value`.
-->
<script lang="ts">
  import { Tabs as BitsTabs } from 'bits-ui';

  export interface TabDef {
    value: string;
    label: string;
    disabled?: boolean;
  }

  interface Props {
    value?: string;
    tabs: TabDef[];
    children?: import('svelte').Snippet;
  }

  let { value = $bindable(''), tabs, children }: Props = $props();

  // Default to the first tab on first render.
  $effect.pre(() => {
    if (!value && tabs.length > 0) {
      const first = tabs[0];
      if (first !== undefined) {
        value = first.value;
      }
    }
  });
</script>

<BitsTabs.Root bind:value class="tabs-root">
  <BitsTabs.List class="tabs-list" aria-label="Tab navigation">
    {#each tabs as tab (tab.value)}
      <BitsTabs.Trigger
        value={tab.value}
        disabled={tab.disabled}
        class="tabs-trigger"
      >
        {tab.label}
      </BitsTabs.Trigger>
    {/each}
  </BitsTabs.List>

  {#each tabs as tab (tab.value)}
    <BitsTabs.Content value={tab.value} class="tabs-content">
      {#if value === tab.value}
        {@render children?.()}
      {/if}
    </BitsTabs.Content>
  {/each}
</BitsTabs.Root>

<style>
  :global(.tabs-root) {
    display: flex;
    flex-direction: column;
    width: 100%;
  }

  :global(.tabs-list) {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border-subtle-01);
    overflow-x: auto;
    scrollbar-width: none;
  }

  :global(.tabs-list::-webkit-scrollbar) {
    display: none;
  }

  :global(.tabs-trigger) {
    flex-shrink: 0;
    padding: var(--spacing-03) var(--spacing-05);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-secondary);
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    cursor: pointer;
    transition: color var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
    min-height: var(--touch-min);
  }

  :global(.tabs-trigger:hover:not([disabled])) {
    color: var(--text-primary);
  }

  :global(.tabs-trigger[data-state="active"]) {
    color: var(--text-primary);
    border-bottom-color: var(--interactive);
    font-weight: 600;
  }

  :global(.tabs-trigger[disabled]) {
    opacity: 0.4;
    cursor: not-allowed;
  }

  :global(.tabs-content) {
    padding-top: var(--spacing-06);
    outline: none;
  }
</style>
