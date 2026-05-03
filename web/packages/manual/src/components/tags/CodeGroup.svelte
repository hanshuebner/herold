<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    children?: Snippet;
  }

  const { children }: Props = $props();
</script>

<!--
  CodeGroup wraps multiple fenced code blocks in a visually grouped container.
  Tab switching between blocks is handled by ManualPage via the rendered
  IncludedCode / fence children; this wrapper provides the group chrome.
-->
<div class="code-group">
  {@render children?.()}
</div>

<style>
  .code-group {
    margin: var(--spacing-05) 0;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
  }

  /* Strip the individual block margins when nested inside a group. */
  .code-group :global(.code-block) {
    margin: 0;
    border: none;
    border-radius: 0;
    border-top: 1px solid var(--border-subtle-01);
  }

  .code-group :global(.code-block:first-child) {
    border-top: none;
  }
</style>
