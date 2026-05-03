<script lang="ts">
  interface Props {
    keys: string;
  }

  const { keys }: Props = $props();

  /**
   * Split the keys string on whitespace, then normalise common aliases.
   * The display is platform-agnostic; "Mod" is displayed as-is to keep
   * the component SSR-safe (no navigator access).
   */
  const keyList = $derived(
    keys
      .trim()
      .split(/\s+/)
      .filter((k) => k.length > 0),
  );
</script>

<span class="kbd-seq" aria-label={keys}>
  {#each keyList as key, i (i)}
    {#if i > 0}<span class="kbd-sep" aria-hidden="true">+</span>{/if}
    <kbd class="kbd-key">{key}</kbd>
  {/each}
</span>

<style>
  .kbd-seq {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-01);
    white-space: nowrap;
  }

  .kbd-sep {
    color: var(--text-helper);
    font-size: var(--type-code-01-size);
    user-select: none;
  }

  .kbd-key {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    line-height: var(--type-code-01-line);
    background: var(--layer-03);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-bottom-width: 2px;
    border-radius: var(--radius-sm);
    padding: 0 var(--spacing-02);
    min-width: 1.6em;
    text-align: center;
    box-shadow: 0 1px 0 rgba(0, 0, 0, 0.3);
    display: inline-block;
  }
</style>
