<script lang="ts">
  import type { Snippet } from 'svelte';
  import { defaultT } from '../../chrome/strings.js';

  interface Props {
    type?: 'info' | 'warning' | 'caution';
    title?: string;
    t?: (key: string) => string;
    children?: Snippet;
  }

  const { type = 'info', title, t = defaultT, children }: Props = $props();

  const defaultTitle = $derived(t(`manual.callout.${type}`));
  const displayTitle = $derived(title ?? defaultTitle);
</script>

<aside class="callout callout--{type}" role="note" aria-label={displayTitle}>
  <div class="callout__header">
    <span class="callout__icon" aria-hidden="true">
      {#if type === 'warning'}
        !
      {:else if type === 'caution'}
        !!
      {:else}
        i
      {/if}
    </span>
    <span class="callout__title">{displayTitle}</span>
  </div>
  <div class="callout__body">
    {@render children?.()}
  </div>
</aside>

<style>
  .callout {
    border-left: 4px solid var(--support-info);
    background: color-mix(in srgb, var(--support-info) 8%, var(--layer-01));
    border-radius: 0 var(--radius-md) var(--radius-md) 0;
    padding: var(--spacing-04) var(--spacing-05);
    margin: var(--spacing-05) 0;
  }
  .callout--warning {
    border-left-color: var(--support-warning);
    background: color-mix(in srgb, var(--support-warning) 8%, var(--layer-01));
  }
  .callout--caution {
    border-left-color: var(--support-error);
    background: color-mix(in srgb, var(--support-error) 8%, var(--layer-01));
  }

  .callout__header {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    margin-bottom: var(--spacing-03);
  }

  .callout__icon {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    font-weight: 700;
    color: var(--support-info);
    background: color-mix(in srgb, var(--support-info) 20%, transparent);
    border-radius: var(--radius-pill);
    width: 20px;
    height: 20px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .callout--warning .callout__icon {
    color: var(--support-warning);
    background: color-mix(in srgb, var(--support-warning) 20%, transparent);
  }
  .callout--caution .callout__icon {
    color: var(--support-error);
    background: color-mix(in srgb, var(--support-error) 20%, transparent);
  }

  .callout__title {
    font-size: var(--type-heading-compact-01-size);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-primary);
  }

  .callout__body {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
  }

  .callout__body :global(p:first-child) {
    margin-top: 0;
  }
  .callout__body :global(p:last-child) {
    margin-bottom: 0;
  }
</style>
